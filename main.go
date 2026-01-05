package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
)

type Graph map[string]map[string]bool

type TsconfigCompilerOptions struct {
	Paths map[string][]string `json:"paths"`
	RootDir string `json:"rootDir"`
	BaseUrl string `json:"baseUrl"`
}

type TsconfigJSON struct {
	CompilerOptions TsconfigCompilerOptions `json:"compilerOptions"`
}

var trimLastDirConfig = []struct{
	orig    string
	replace string
}{
	{ "((?:components|hooks|store|utils?)/[^/]+)(?:/.+)?$", "$1" },
}
var trimLastDirRe = make([]struct{
	orig    *regexp.Regexp
	replace string
}, len(trimLastDirConfig))

var typeOnlyRe = regexp.MustCompile(`\btype\b`)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: ts-import-graph-go <project-root> [--ignore-externals] [--print-file-count]")
		os.Exit(1)
	}

	root := filepath.Clean(os.Args[1])

	for i, item := range trimLastDirConfig {
		trimLastDirRe[i].orig = regexp.MustCompile(item.orig)
		trimLastDirRe[i].replace = item.replace
	}

	// Read tsconfig.json if it exists
	tsconfigPath := filepath.Join(root, "tsconfig.json")
	var paths map[string][]string
	var baseUrl string

	if data, err := os.ReadFile(tsconfigPath); err == nil {
		var tsconfig TsconfigJSON
		if err := json.Unmarshal(data, &tsconfig); err == nil {
			paths = tsconfig.CompilerOptions.Paths
			baseUrl = tsconfig.CompilerOptions.BaseUrl
			if baseUrl == "" {
				baseUrl = "."
			}
		}
	}

	graph := make(Graph)
	fileCounts := make(map[string]int)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isTSFile(path) {
			parseFile(root, path, graph, paths, baseUrl, fileCounts)
		}
		return nil
	})

	if err != nil {
		panic(err)
	}

	ignoreExternals := false
	printFileCount := false
	format := "graphviz"
	for _, a := range os.Args[2:] {
		if a == "--ignore-externals" {
			ignoreExternals = true
		}
		if a == "--print-file-count" {
			printFileCount = true
		}
		if strings.HasPrefix(a, "--format=") {
			format = strings.TrimPrefix(a, "--format=")
		}
	}

	if format == "graphviz" {
		printGraphviz(graph, ignoreExternals, printFileCount, fileCounts)
	}
}

func isTSFile(path string) bool {
	return strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")
}

func parseFile(root, filePath string, graph Graph, paths map[string][]string, baseUrl string, fileCounts map[string]int) {
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
	relFromModule := getModule(relFrom)

	if _, ok := graph[relFromModule]; !ok {
		graph[relFromModule] = make(map[string]bool)
	}
	// count files per module
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
			to := resolveImport(root, filePath, raw, paths, baseUrl)
			if to != "" {
				toModule := getModule(to)
				if toModule != relFromModule && !graph[relFromModule][toModule] {
					graph[relFromModule][toModule] = true
					printEdge(relFromModule, toModule)
				}
			}
		}
	}
}

func isRelativeImport(path string) bool {
	return strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../")
}

func resolvePathAlias(root, importPath string, paths map[string][]string, baseUrl string) string {
	if len(paths) == 0 {
		return ""
	}

	// Try to match import path against path aliases
	for aliasPattern, aliasPaths := range paths {
		if matchAlias(importPath, aliasPattern) {
			// Get the actual path from alias configuration
			if len(aliasPaths) > 0 {
				actualPath := aliasPaths[0]
				// Replace the alias pattern with the actual path
				resolved := replaceAlias(importPath, aliasPattern, actualPath)

				// Resolve relative to baseUrl or root
				baseDir := filepath.Join(root, baseUrl)
				candidate := filepath.Join(baseDir, resolved)

				// Try different extensions
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
	// Handle patterns like "@/components/*" or "@/*"
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

func resolveImport(root, fromFile, imp string, paths map[string][]string, baseUrl string) string {
	// First try to resolve as path alias
	if resolved := resolvePathAlias(root, imp, paths, baseUrl); resolved != "" {
		return resolved
	}

	// Fall back to relative import resolution
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

func normalize(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func getModule(path string) string {
	result := normalize(path)
	for _, item := range trimLastDirRe {
		result = item.orig.ReplaceAllString(result, item.replace)
	}
	return result
}

func findRoots(graph Graph, ignoreExternals bool) map[string]bool {
	inDegree := make(map[string]int)

	for node := range graph {
		if ignoreExternals && isExternalModule(node) {
			continue
		}
		inDegree[node] = 0
	}

	for node := range graph {
		if ignoreExternals && isExternalModule(node) {
			continue
		}
		for target := range graph[node] {
			if ignoreExternals && isExternalModule(target) {
				continue
			}
			inDegree[target]++
		}
	}

	roots := make(map[string]bool)
	for node, degree := range inDegree {
		if degree == 0 {
			roots[node] = true
		}
	}

	roots["."] = true

	return roots
}

func isExternalModule(node string) bool {
	return strings.Contains(node, "..")
}

func computeRanks(graph Graph, roots map[string]bool, ignoreExternals bool) map[string]int {
	inDegree := make(map[string]int)
	outDegree := make(map[string]int)
	allNodes := make(map[string]bool)

	for node := range graph {
		if ignoreExternals && isExternalModule(node) {
			continue
		}
		allNodes[node] = true
		inDegree[node] = 0
		outDegree[node] = 0
	}

	for node := range graph {
		if ignoreExternals && isExternalModule(node) {
			continue
		}
		for target := range graph[node] {
			if ignoreExternals && isExternalModule(target) {
				continue
			}
			allNodes[target] = true
			inDegree[target]++
			outDegree[node]++
		}
	}

	ranks := make(map[string]int)
	queue := make([]string, 0)
	enqueued := make(map[string]bool)

	for root := range roots {
		queue = append(queue, root)
		ranks[root] = 0
		enqueued[root] = true
		printRank(root, 0)
	}

	currentRank := 0
	for len(queue) > 0 || len(ranks) < len(allNodes) {
		nextQueue := make([]string, 0)

		sort.Slice(queue, func (i, j int) bool {
			x := strings.Count(queue[i], "/")
			y := strings.Count(queue[j], "/")
			return x < y
		})
		for _, node := range queue {
			for target := range graph[node] {
				if ignoreExternals && isExternalModule(target) {
					continue
				}
				inDegree[target]--
				outDegree[node] = 0
				if enqueued[target] {
					continue
				}
				if strings.HasPrefix(target, "..") {
					ranks[target] = 9999
					enqueued[target] = true
					printRank(target, ranks[target])
					continue
				}
				if inDegree[target] == 0 {
					nextQueue = append(nextQueue, target)
					ranks[target] = currentRank + 1
					enqueued[target] = true
					printRank(target, ranks[target])
				}
			}
		}

		if len(nextQueue) == 0 && len(ranks) < len(allNodes) {
			minDirDepth := math.MaxInt32
			maxOutDegree := 0
			found := false
			for node := range allNodes {
				depth := strings.Count(node, "/")
				if _, ok := ranks[node]; !ok && !enqueued[node] && (outDegree[node] > maxOutDegree || outDegree[node] == maxOutDegree && depth < minDirDepth) {
					minDirDepth = depth
					maxOutDegree = outDegree[node]
					found = true
				}
			}
			if !found {
				continue
			}
			for maxNode := range allNodes {
				depth := strings.Count(maxNode, "/")
				if _, ok := ranks[maxNode]; !ok && !enqueued[maxNode] && depth == minDirDepth && outDegree[maxNode] == maxOutDegree {
					inDegree[maxNode]--
					outDegree[maxNode] = 0
					if strings.HasPrefix(maxNode, "..") {
						ranks[maxNode] = 999999
						enqueued[maxNode] = true
						printRank(maxNode, ranks[maxNode])
						continue
					}
					nextQueue = append(nextQueue, maxNode)
					ranks[maxNode] = currentRank + 1
					enqueued[maxNode] = true
					printRank(maxNode, ranks[maxNode])
				}
			}
		}

		queue = nextQueue
		currentRank++
	}

	return ranks
}

func isBackEdge(ranks map[string]int, from, to string) bool {
	return ranks[to] < ranks[from]
}

func printGraphviz(graph Graph, ignoreExternals bool, printFileCount bool, fileCounts map[string]int) {
	roots := findRoots(graph, ignoreExternals)
	ranks := computeRanks(graph, roots, ignoreExternals)

	fmt.Println("digraph G {")
	fmt.Println(`  rankdir=LR;`)
	fmt.Println(`  node [shape=box fontname="Arial"];`)

	rankGroups := make(map[int][]string)
	for node, rank := range ranks {
		rankGroups[rank] = append(rankGroups[rank], node)
	}
	rankGroupsKeys := make([]int, 0, len(rankGroups))
	for rank := range rankGroups {
		rankGroupsKeys = append(rankGroupsKeys, rank)
	}
	sort.Ints(rankGroupsKeys)

	for _, rank := range rankGroupsKeys {
		if nodes, ok := rankGroups[rank]; ok {
			fmt.Printf("  /* %d */ { rank=same; ", rank)
			for _, node := range nodes {
				disp := node
				if printFileCount {
					disp = fmt.Sprintf("%s (%d)", node, fileCounts[node])
				}
				if roots[node] {
					fmt.Printf(`"%s" [color=green style=filled fillcolor=lightgreen]; `, disp)
				} else if strings.HasPrefix(node, "..") {
					fmt.Printf(`"%s" [color=yellow style=filled fillcolor=lightyellow]; `, disp)
				} else {
					fmt.Printf(`"%s"; `, disp)
				}
			}
			fmt.Println("}")
		}
	}

	for from, targets := range graph {
		if ignoreExternals && isExternalModule(from) {
			continue
		}
		fromDisp := from
		if printFileCount {
			fromDisp = fmt.Sprintf("%s (%d)", from, fileCounts[from])
		}
		if len(targets) == 0 {
			if !ignoreExternals || !isExternalModule(from) {
				fmt.Printf(`  "%s";`+"\n", fromDisp)
			}
		}
		for to := range targets {
			if ignoreExternals && isExternalModule(to) {
				continue
			}
			toDisp := to
			if printFileCount {
				toDisp = fmt.Sprintf("%s (%d)", to, fileCounts[to])
			}
			if isBackEdge(ranks, from, to) {
				fmt.Printf(`  "%s" -> "%s" [color=red style=dashed];`+"\n", fromDisp, toDisp)
			} else {
				fmt.Printf(`  "%s" -> "%s";`+"\n", fromDisp, toDisp)
			}
		}
	}

	fmt.Println("}")
}

func printRank(root string, rank int) {
	// fmt.Printf("[rank] %s: %d\n", root, rank)
}

func printEdge(from, to string) {
	// fmt.Printf("%s -> %s\n", from, to)
}
