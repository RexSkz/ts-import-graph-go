package imports

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/tsx"

	"ts-import-graph-go/internal/graph"
)

type Options struct {
	Paths   map[string][]string
	BaseURL string
}

const trimConfigFileName = "ts-import-graph.json"

type trimRuleConfig struct {
	Orig    string `json:"orig"`
	Replace string `json:"replace"`
}

type trimConfigFile struct {
	TrimLastDirConfig []trimRuleConfig `json:"trimLastDirConfig"`
}

type trimRule struct {
	orig    *regexp.Regexp
	replace string
}

var defaultTrimLastDirConfig = []trimRuleConfig{
	{Orig: "((?:components|hooks|store|utils?)/[^/]+)(?:/.+)?$", Replace: "$1"},
}

var typeOnlyRe = regexp.MustCompile(`\btype\b`)

func BuildGraph(root string, options Options) (graph.Graph, map[string]int, error) {
	graphData := graph.New()
	fileCounts := make(map[string]int)
	trimRules, err := loadTrimRules(root)
	if err != nil {
		return nil, nil, err
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isTSFile(path) {
			parseFile(root, path, graphData, options.Paths, options.BaseURL, fileCounts, trimRules)
		}
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return graphData, fileCounts, nil
}

func loadTrimRules(root string) ([]trimRule, error) {
	configPath := filepath.Join(root, trimConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return compileTrimRules(defaultTrimLastDirConfig)
		}
		return nil, err
	}

	var config trimConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	if config.TrimLastDirConfig == nil {
		return compileTrimRules(defaultTrimLastDirConfig)
	}

	return compileTrimRules(config.TrimLastDirConfig)
}

func compileTrimRules(config []trimRuleConfig) ([]trimRule, error) {
	compiled := make([]trimRule, 0, len(config))
	for _, item := range config {
		compiledRule, err := regexp.Compile(item.Orig)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, trimRule{
			orig:    compiledRule,
			replace: item.Replace,
		})
	}
	return compiled, nil
}

func isTSFile(path string) bool {
	return strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")
}

func parseFile(root, filePath string, graphData graph.Graph, paths map[string][]string, baseURL string, fileCounts map[string]int, trimRules []trimRule) {
	src, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	parser := sitter.NewParser()
	parser.SetLanguage(tsx.GetLanguage())

	tree, err := parser.ParseCtx(context.Background(), nil, src)
	if err != nil {
		return
	}

	relFrom, _ := filepath.Rel(root, filePath)
	relFromModule := moduleName(relFrom, trimRules)

	graphData.EnsureNode(relFromModule)
	fileCounts[relFromModule]++

	query := `
(
  import_statement
    source: (string) @import
)
(
  call_expression
    function: (import)
    arguments: (arguments (string) @import)
)
`

	q, _ := sitter.NewQuery([]byte(query), tsx.GetLanguage())
	qc := sitter.NewQueryCursor()
	qc.Exec(q, tree.RootNode())

	for {
		m, ok := qc.NextMatch()
		if !ok {
			break
		}
		for _, cap := range m.Captures {
			// Skip imports that are type-only (e.g. `import type ...` or `import { type Foo } from '...'`)
			node := cap.Node
			for node != nil && node.Type() != "import_statement" && node.Type() != "call_expression" && node.Type() != "export_statement" {
				node = node.Parent()
			}
			if node != nil && (node.Type() == "import_statement" || node.Type() == "export_statement") {
				seg := src[node.StartByte():cap.Node.StartByte()]
				if typeOnlyRe.Match(seg) {
					continue
				}
			}

			raw := string(src[cap.Node.StartByte()+1 : cap.Node.EndByte()-1])
			to := resolveImport(root, filePath, raw, paths, baseURL)
			if to != "" {
				toModule := moduleName(to, trimRules)
				if toModule != relFromModule && graphData.AddEdge(relFromModule, toModule) {
					printEdge(relFromModule, toModule)
				}
			}
		}
	}
}

func isRelativeImport(path string) bool {
	return strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../")
}

func resolvePathAlias(root, importPath string, paths map[string][]string, baseURL string) string {
	if len(paths) == 0 {
		return ""
	}

	for aliasPattern, aliasPaths := range paths {
		if matchAlias(importPath, aliasPattern) {
			if len(aliasPaths) > 0 {
				actualPath := aliasPaths[0]
				resolved := replaceAlias(importPath, aliasPattern, actualPath)

				baseDir := filepath.Join(root, baseURL)
				candidate := filepath.Join(baseDir, resolved)

				extensions := []string{"", ".ts", ".tsx", "/index.ts", "/index.tsx"}
				for _, ext := range extensions {
					full := candidate + ext
					if exists(full) {
						rel, _ := filepath.Rel(root, full)
						return rel
					}
				}
			}
		}
	}

	return ""
}

func matchAlias(importPath, aliasPattern string) bool {
	if strings.HasSuffix(aliasPattern, "/*") {
		prefix := strings.TrimSuffix(aliasPattern, "/*")
		return strings.HasPrefix(importPath, prefix+"/") || importPath == prefix
	}
	return importPath == aliasPattern
}

func replaceAlias(importPath, aliasPattern, actualPath string) string {
	if strings.HasSuffix(aliasPattern, "/*") {
		prefix := strings.TrimSuffix(aliasPattern, "/*")
		actualPrefix := strings.TrimSuffix(actualPath, "/*")
		return strings.Replace(importPath, prefix, actualPrefix, 1)
	}
	return actualPath
}

func resolveImport(root, fromFile, imp string, paths map[string][]string, baseURL string) string {
	if resolved := resolvePathAlias(root, imp, paths, baseURL); resolved != "" {
		return resolved
	}

	if !isRelativeImport(imp) {
		return ""
	}

	base := filepath.Dir(fromFile)
	candidate := filepath.Join(base, imp)

	extensions := []string{".ts", ".tsx", "/index.ts", "/index.tsx"}

	for _, ext := range extensions {
		full := candidate + ext
		if exists(full) {
			rel, _ := filepath.Rel(root, full)
			return rel
		}
	}

	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func normalizePath(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func moduleName(path string, trimRules []trimRule) string {
	result := normalizePath(path)
	for _, item := range trimRules {
		result = item.orig.ReplaceAllString(result, item.replace)
	}
	return result
}

func printEdge(from, to string) {
	// fmt.Printf("%s -> %s\n", from, to)
}
