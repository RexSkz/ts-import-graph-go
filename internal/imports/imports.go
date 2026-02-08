package imports

import (
	"context"
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

var trimLastDirConfig = []struct {
	orig    string
	replace string
}{
	{"((?:components|hooks|store|utils?)/[^/]+)(?:/.+)?$", "$1"},
}

var trimLastDirRe = compileTrimLastDirConfig()

var typeOnlyRe = regexp.MustCompile(`\btype\b`)

func BuildGraph(root string, options Options) (graph.Graph, map[string]int, error) {
	graphData := graph.New()
	fileCounts := make(map[string]int)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isTSFile(path) {
			parseFile(root, path, graphData, options.Paths, options.BaseURL, fileCounts)
		}
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return graphData, fileCounts, nil
}

func compileTrimLastDirConfig() []struct {
	orig    *regexp.Regexp
	replace string
} {
	compiled := make([]struct {
		orig    *regexp.Regexp
		replace string
	}, 0, len(trimLastDirConfig))
	for _, item := range trimLastDirConfig {
		compiled = append(compiled, struct {
			orig    *regexp.Regexp
			replace string
		}{
			orig:    regexp.MustCompile(item.orig),
			replace: item.replace,
		})
	}
	return compiled
}

func isTSFile(path string) bool {
	return strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")
}

func parseFile(root, filePath string, graphData graph.Graph, paths map[string][]string, baseURL string, fileCounts map[string]int) {
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
	relFromModule := moduleName(relFrom)

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
				toModule := moduleName(to)
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

func moduleName(path string) string {
	result := normalizePath(path)
	for _, item := range trimLastDirRe {
		result = item.orig.ReplaceAllString(result, item.replace)
	}
	return result
}

func printEdge(from, to string) {
	// fmt.Printf("%s -> %s\n", from, to)
}
