package graph

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type Graph map[string]map[string]bool

func New() Graph {
	return make(Graph)
}

func (g Graph) EnsureNode(node string) {
	if _, ok := g[node]; !ok {
		g[node] = make(map[string]bool)
	}
}

func (g Graph) AddEdge(from, to string) bool {
	g.EnsureNode(from)
	if g[from][to] {
		return false
	}
	g[from][to] = true
	return true
}

func PrintGraphviz(graph Graph, ignoreExternals bool, printFileCount bool, fileCounts map[string]int) {
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

		sort.Slice(queue, func(i, j int) bool {
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

func printRank(root string, rank int) {
	// fmt.Printf("[rank] %s: %d\n", root, rank)
}
