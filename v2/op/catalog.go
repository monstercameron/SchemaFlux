package op

import (
	"slices"
	"strings"
)

// Entry describes one operation of the library: the name its Records carry,
// what kind of thing it asks a model to do and how firmly its contract holds.
type Entry struct {
	// Name is the operation as its Record names it: the function's name in lower
	// case, with the subpackage as a prefix for the functions that are not in the
	// root, "extract", "exp.synthesize", "redact.text". The three functions of
	// this package are listed as "op.run", "op.runbatch" and "op.runmany", though
	// the Record of a call made with a named descriptor carries the descriptor's
	// own name.
	Name string

	// Category is the family the operation belongs to. The zero value means it
	// belongs to none: the runners of this package do whatever their descriptor
	// says.
	Category Category

	// Stability says how firmly the contract holds. StabilityStable means its
	// contract is defined, the library checks it in Go, and breaking it is a
	// breaking change. StabilityExperimental means it works and is tested and
	// what it promises about its answer may change.
	Stability Stability
}

// catalogue is the table, built once and sorted by name. Every function that asks
// a model and adds a Record of its own is in it, and nothing else is.
var catalogue = buildCatalogue()

// buildCatalogue lists the operations by family, as the library groups them.
func buildCatalogue() []Entry {
	type family struct {
		category  Category
		stability Stability
		names     string
	}
	families := []family{
		{CategoryExtract, StabilityStable, "extract parse infer"},
		{CategoryTransform, StabilityStable, "transform summarize summarizedetail streamsummarize rewrite rewritedetail streamrewrite " +
			"translate translatedetail streamtranslate expand expanddetail streamexpand compress normalize format merge"},
		{CategoryGenerate, StabilityStable, "generate complete completefield suggest annotate explain question"},
		{CategoryClassify, StabilityStable, "classify score compare similar diff cluster deduplicate " +
			"is isdetail which whichdetail whichornone whichornonedetail rate ratedetail assess assessdetail"},
		{CategorySelect, StabilityStable, "choose filter semanticmatch flow.decide flow.match"},
		{CategoryOrder, StabilityStable, "sort rank"},
		{CategoryReview, StabilityStable, "verify audit critique"},
		{CategoryValidate, StabilityStable, "validate"},
		{CategoryGenerate, StabilityExperimental, "exp.synthesize exp.predict exp.assemble exp.derive exp.interpolate"},
		{CategorySelect, StabilityExperimental, "exp.arbitrate exp.resolve exp.settle exp.settleadversarial"},
		{CategoryTransform, StabilityExperimental, "exp.conform exp.project exp.pivot exp.enrich exp.enrichinplace " +
			"exp.decompose exp.decomposetoslice redact.text"},
		{0, StabilityStable, "op.run op.runbatch op.runmany"},
	}
	var entries []Entry
	for _, f := range families {
		for _, name := range strings.Fields(f.names) {
			entries = append(entries, Entry{Name: name, Category: f.category, Stability: f.stability})
		}
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return entries
}

// Catalog returns every operation of the library that asks a model, sorted by
// name. The slice is the caller's: changing it changes nothing in the library.
//
// Use it to find out how far to build on an operation before depending on it:
// an experimental one may change what it promises.
func Catalog() []Entry { return slices.Clone(catalogue) }

// Lookup returns the Entry of the operation called name, and false when there is
// none. The name is the one a Record carries, and its case does not matter. The
// name of a custom descriptor is not in the catalogue.
func Lookup(name string) (Entry, bool) {
	name = strings.ToLower(name)
	i, found := slices.BinarySearchFunc(catalogue, name, func(e Entry, name string) int { return strings.Compare(e.Name, name) })
	if !found {
		return Entry{}, false
	}
	return catalogue[i], true
}
