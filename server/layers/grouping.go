// This package reads an export reference graph (i.e. a graph representing the
// runtime dependencies of a set of derivations) created by Nix and groups it in
// a way that is likely to match the grouping for other derivation sets with
// overlapping dependencies.
//
// This is used to determine which derivations to include in which layers of a
// container image.
//
// # Inputs
//
// * a graph of Nix runtime dependencies, generated via exportReferenceGraph
// * popularity values of each package in the Nix package set (in the form of a
//   direct reference count)
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
// Otherwise, a merge rating is calculated for each layer. This is the
// product of the layer's total size and its root node's popularity.
//
// Layers are then merged in ascending order of merge ratings until
// they fit into the layer budget.
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
package layers

import (
	"crypto/sha1"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"gonum.org/v1/gonum/graph/flow"
	"gonum.org/v1/gonum/graph/simple"
)

// RuntimeGraph represents structured information from Nix about the runtime
// dependencies of a derivation.
//
// This is generated in Nix by using the exportReferencesGraph feature.
type RuntimeGraph struct {
	References struct {
		Graph []string `json:"graph"`
	} `json:"exportReferencesGraph"`

	Graph []struct {
		Size uint64   `json:"closureSize"`
		Path string   `json:"path"`
		Refs []string `json:"references"`
	} `json:"graph"`
}

// Popularity data for each Nix package that was calculated in advance.
//
// Popularity is a number from 1-100 that represents the
// popularity percentile in which this package resides inside
// of the nixpkgs tree.
type Popularity = map[string]int

// Layer represents the data returned for each layer that Nix should
// build for the container image.
type Layer struct {
	Contents    []string `json:"contents"`
	MergeRating uint64
}

// Hash the contents of a layer to create a deterministic identifier that can be
// used for caching.
func (l *Layer) Hash() string {
	sum := sha1.Sum([]byte(strings.Join(l.Contents, ":")))
	return fmt.Sprintf("%x", sum)
}

func (a Layer) merge(b Layer) Layer {
	a.Contents = append(a.Contents, b.Contents...)
	a.MergeRating += b.MergeRating
	return a
}

// closure as pointed to by the graph nodes.
type closure struct {
	GraphID    int64
	Path       string
	Size       uint64
	Refs       []string
	Popularity int
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
func (c *closure) bigOrPopular() bool {
	const sizeThreshold = 100 * 1000000 // 100MB

	if c.Size > sizeThreshold {
		return true
	}

	// The threshold value used here is currently roughly the
	// minimum number of references that only 1% of packages in
	// the entire package set have.
	//
	// TODO(tazjin): Do this more elegantly by calculating
	// percentiles for each package and using those instead.
	if c.Popularity >= 1000 {
		return true
	}

	return false
}

func insertEdges(graph *simple.DirectedGraph, cmap *map[string]*closure, node *closure) {
	// Big or popular nodes get a separate edge from the top to
	// flag them for their own layer.
	if node.bigOrPopular() && !graph.HasEdgeFromTo(0, node.ID()) {
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
func buildGraph(refs *RuntimeGraph, pop *Popularity) *simple.DirectedGraph {
	cmap := make(map[string]*closure)
	graph := simple.NewDirectedGraph()

	// Insert all closures into the graph, as well as a fake root
	// closure which serves as the top of the tree.
	//
	// A map from store paths to IDs is kept to actually insert
	// edges below.
	root := &closure{
		GraphID: 0,
		Path:    "image_root",
	}
	graph.AddNode(root)

	for idx, c := range refs.Graph {
		node := &closure{
			GraphID: int64(idx + 1), // inc because of root node
			Path:    c.Path,
			Size:    c.Size,
			Refs:    c.Refs,
		}

		if p, ok := (*pop)[node.DOTID()]; ok {
			node.Popularity = p
		} else {
			node.Popularity = 1
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
		insertEdges(graph, &cmap, c)
	}

	return graph
}

// Extracts a subgraph starting at the specified root from the
// dominator tree. The subgraph is converted into a flat list of
// layers, each containing the store paths and merge rating.
func groupLayer(dt *flow.DominatorTree, root *closure) Layer {
	size := root.Size
	contents := []string{root.Path}
	children := dt.DominatedBy(root.ID())

	// This iteration does not use 'range' because the list being
	// iterated is modified during the iteration (yes, I'm sorry).
	for i := 0; i < len(children); i++ {
		child := children[i].(*closure)
		size += child.Size
		contents = append(contents, child.Path)
		children = append(children, dt.DominatedBy(child.ID())...)
	}

	// Contents are sorted to ensure that hashing is consistent
	sort.Strings(contents)

	return Layer{
		Contents: contents,
		// TODO(tazjin): The point of this is to factor in
		// both the size and the popularity when making merge
		// decisions, but there might be a smarter way to do
		// it than a plain multiplication.
		MergeRating: uint64(root.Popularity) * size,
	}
}

// Calculate the dominator tree of the entire package set and group
// each top-level subtree into a layer.
//
// Layers are merged together until they fit into the layer budget,
// based on their merge rating.
func dominate(budget int, graph *simple.DirectedGraph) []Layer {
	dt := flow.Dominators(graph.Node(0), graph)

	var layers []Layer
	for _, n := range dt.DominatedBy(dt.Root().ID()) {
		layers = append(layers, groupLayer(&dt, n.(*closure)))
	}

	sort.Slice(layers, func(i, j int) bool {
		return layers[i].MergeRating < layers[j].MergeRating
	})

	if len(layers) > budget {
		log.Printf("Ideal image has %v layers, but budget is %v\n", len(layers), budget)
	}

	for len(layers) > budget {
		merged := layers[0].merge(layers[1])
		layers[1] = merged
		layers = layers[1:]
	}

	return layers
}

// GroupLayers applies the algorithm described above the its input and returns a
// list of layers, each consisting of a list of Nix store paths that it should
// contain.
func Group(refs *RuntimeGraph, pop *Popularity, budget int) []Layer {
	graph := buildGraph(refs, pop)
	return dominate(budget, graph)
}
