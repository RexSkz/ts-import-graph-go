package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ts-import-graph-go/internal/graph"
	"ts-import-graph-go/internal/imports"
	"ts-import-graph-go/internal/tsconfig"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: ts-import-graph-go <project-root> [--ignore-externals] [--print-file-count]")
		os.Exit(1)
	}

	root := filepath.Clean(os.Args[1])

	paths, baseURL := tsconfig.Load(root)

	graphData, fileCounts, err := imports.BuildGraph(root, imports.Options{
		Paths:   paths,
		BaseURL: baseURL,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build import graph: %v\n", err)
		os.Exit(1)
	}

	ignoreExternals, printFileCount, format := parseArgs(os.Args[2:])
	if format == "graphviz" {
		graph.PrintGraphviz(graphData, ignoreExternals, printFileCount, fileCounts)
	}
}

func parseArgs(args []string) (bool, bool, string) {
	ignoreExternals := false
	printFileCount := false
	format := "graphviz"
	for _, arg := range args {
		if arg == "--ignore-externals" {
			ignoreExternals = true
			continue
		}
		if arg == "--print-file-count" {
			printFileCount = true
			continue
		}
		if strings.HasPrefix(arg, "--format=") {
			format = strings.TrimPrefix(arg, "--format=")
		}
	}
	return ignoreExternals, printFileCount, format
}
