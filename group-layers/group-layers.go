// This program reads an export reference graph (i.e. a graph representing the
// runtime dependencies of a set of derivations) created by Nix and groups them
// in a way that is likely to match the grouping for other derivation sets with
// overlapping dependencies.
//
// This is used to determine which derivations to include in which layers of a
// container image.
//
// # Inputs
//
// * a graph of Nix runtime dependencies, generated via exportReferenceGraph
// * a file containing absolute popularity values of packages in the
//   Nix package set (in the form of a direct reference count)
// * a maximum number of layers to allocate for the image (the "layer budget")
//
// # Algorithm
//
// It works by first creating a (directed) dependency tree:
//
// img (root node)
// │
// ├───> A ─────┐
// │            v
// ├───> B ───> E
// │            ^
// ├───> C ─────┘
// │     │
// │     v
// └───> D ───> F
//       │
//       └────> G
//
// Each node (i.e. package) is then visited to determine how important
// it is to separate this node into its own layer, specifically:
//
// 1. Is the node within a certain threshold percentile of absolute
//    popularity within all of nixpkgs? (e.g. `glibc`, `openssl`)
//
// 2. Is the node's runtime closure above a threshold size? (e.g. 100MB)
//
// In either case, a bit is flipped for this node representing each
// condition and an edge to it is inserted directly from the image
// root, if it does not already exist.
//
// For the rest of the example we assume 'G' is above the threshold
// size and 'E' is popular.
//
// This tree is then transformed into a dominator tree:
//
// img
// │
// ├───> A
// ├───> B
// ├───> C
// ├───> E
// ├───> D ───> F
// └───> G
//
// Specifically this means that the paths to A, B, C, E, G, and D
// always pass through the root (i.e. are dominated by it), whilst F
// is dominated by D (all paths go through it).
//
// The top-level subtrees are considered as the initially selected
// layers.
//
// If the list of layers fits within the layer budget, it is returned.
//
// Otherwise layers are merged together in this order:
//
// * layers whose root meets neither condition above
// * layers whose root is popular
// * layers whose root is big
// * layers whose root meets both conditions
//
// # Threshold values
//
// Threshold values for the partitioning conditions mentioned above
// have not yet been determined, but we will make a good first guess
// based on gut feeling and proceed to measure their impact on cache
// hits/misses.
//
// # Example
//
// Using the logic described above as well as the example presented in
// the introduction, this program would create the following layer
// groupings (assuming no additional partitioning):
//
// Layer budget: 1
// Layers: { A, B, C, D, E, F, G }
//
// Layer budget: 2
// Layers: { G }, { A, B, C, D, E, F }
//
// Layer budget: 3
// Layers: { G }, { E }, { A, B, C, D, F }
//
// Layer budget: 4
// Layers: { G }, { E }, { D, F }, { A, B, C }
//
// ...
//
// Layer budget: 10
// Layers: { E }, { D, F }, { A }, { B }, { C }
package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
	"regexp"

	"gonum.org/v1/gonum/graph/simple"
	"gonum.org/v1/gonum/graph/flow"
	"gonum.org/v1/gonum/graph/encoding/dot"
)

// closureGraph represents the structured attributes Nix outputs when asking it
// for the exportReferencesGraph of a list of derivations.
type exportReferences struct {
	References struct {
		Graph []string `json:"graph"`
	} `json:"exportReferencesGraph"`

	Graph []struct {
		Size uint64 `json:"closureSize`
		Path string   `json:"path"`
		Refs []string `json:"references"`
	} `json:"graph"`
}

// Popularity data for each Nix package that was calculated in advance.
//
// Popularity is a number from 1-100 that represents the
// popularity percentile in which this package resides inside
// of the nixpkgs tree.
type pkgsMetadata = map[string]int

// closure as pointed to by the graph nodes.
type closure struct {
	GraphID int64
	Path string
	Size uint64
	Refs []string
	Popularity int
	// TODO(tazjin): popularity and other funny business
}

func (c *closure) ID() int64 {
	return c.GraphID
}

var nixRegexp = regexp.MustCompile(`^/nix/store/[a-z0-9]+-`)
func (c *closure) DOTID() string {
	return nixRegexp.ReplaceAllString(c.Path, "")
}

// bigOrPopular checks whether this closure should be considered for
// separation into its own layer, even if it would otherwise only
// appear in a subtree of the dominator tree.
func (c *closure) bigOrPopular(pkgs *pkgsMetadata) bool {
	const sizeThreshold = 100 * 1000000 // 100MB

	if c.Size > sizeThreshold {
		return true
	}

	// TODO(tazjin): After generating the full data, this should
	// be changed to something other than a simple inclusion
	// (currently the test-data only contains the top 200
	// packages).
	pop, ok := (*pkgs)[c.DOTID()]
	if ok {
		log.Printf("%q is popular!\n", c.DOTID())
	}
	c.Popularity = pop
	return ok
}

func insertEdges(graph *simple.DirectedGraph, pop *pkgsMetadata, cmap *map[string]*closure, node *closure) {
	// Big or popular nodes get a separate edge from the top to
	// flag them for their own layer.
	if node.bigOrPopular(pop) && !graph.HasEdgeFromTo(0, node.ID()) {
		edge := graph.NewEdge(graph.Node(0), node)
		graph.SetEdge(edge)
	}

	for _, c := range node.Refs {
		// Nix adds a self reference to each node, which
		// should not be inserted.
		if c != node.Path {
			edge := graph.NewEdge(node, (*cmap)[c])
			graph.SetEdge(edge)
		}
	}
}

// Create a graph structure from the references supplied by Nix.
func buildGraph(refs *exportReferences, pop *pkgsMetadata) *simple.DirectedGraph {
	cmap := make(map[string]*closure)
	graph := simple.NewDirectedGraph()

	// Insert all closures into the graph, as well as a fake root
	// closure which serves as the top of the tree.
	//
	// A map from store paths to IDs is kept to actually insert
	// edges below.
	root := &closure {
		GraphID: 0,
		Path: "image_root",
	}
	graph.AddNode(root)

	for idx, c := range refs.Graph {
		node := &closure {
			GraphID: int64(idx + 1), // inc because of root node
			Path: c.Path,
			Size: c.Size,
			Refs: c.Refs,
		}

		graph.AddNode(node)
		cmap[c.Path] = node
	}

	// Insert the top-level closures with edges from the root
	// node, then insert all edges for each closure.
	for _, p := range refs.References.Graph {
		edge := graph.NewEdge(root, cmap[p])
		graph.SetEdge(edge)
	}

	for _, c := range cmap {
		insertEdges(graph, pop, &cmap, c)
	}

	// gv, err := dot.Marshal(graph, "deps", "", "")
	// if err != nil {
	// 	log.Fatalf("Could not encode graph: %s\n", err)
	// }
	// fmt.Print(string(gv))
	// os.Exit(0)

	return graph
}

// Calculate the dominator tree of the entire package set and group
// each top-level subtree into a layer.
func dominate(graph *simple.DirectedGraph) {
	dt := flow.Dominators(graph.Node(0), graph)

	// convert dominator tree back into encodable graph
	dg := simple.NewDirectedGraph()

	for nodes := graph.Nodes(); nodes.Next(); {
		dg.AddNode(nodes.Node())
	}

	for nodes := dg.Nodes(); nodes.Next(); {
		node := nodes.Node()
		for _, child := range dt.DominatedBy(node.ID()) {
			edge := dg.NewEdge(node, child)
			dg.SetEdge(edge)
		}
	}

	gv, err := dot.Marshal(dg, "deps", "", "")
	if err != nil {
		log.Fatalf("Could not encode graph: %s\n", err)
	}
	ioutil.WriteFile("graph.dot", gv, 0644)
}

func main() {
	graphFile := flag.String("graph", ".attrs.json", "Input file containing graph")
	popFile := flag.String("pop", "popularity.json", "Package popularity data")
	flag.Parse()

	// Parse graph data
	file, err := ioutil.ReadFile(*graphFile)
	if err != nil {
		log.Fatalf("Failed to load input: %s\n", err)
	}

	var refs exportReferences
	err = json.Unmarshal(file, &refs)
	if err != nil {
		log.Fatalf("Failed to deserialise input: %s\n", err)
	}

	// Parse popularity data
	popBytes, err := ioutil.ReadFile(*popFile)
	if err != nil {
		log.Fatalf("Failed to load input: %s\n", err)
	}

	var pop pkgsMetadata
	err = json.Unmarshal(popBytes, &pop)
	if err != nil {
		log.Fatalf("Failed to deserialise input: %s\n", err)
	}

	log.Printf("%v\n", pop)

	graph := buildGraph(&refs, &pop)
	dominate(graph)
}
