package op

import (
	schemaflux "github.com/monstercameron/schemaflux/v2"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Shape is how a run of one descriptor over its items is carried out: one request
// for each item, one request for many, or one request for everything. A Plan names
// the shape it chose, and PlanOptions.Force asks for one. The zero value is
// ShapeUnspecified.
type Shape uint8

const (
	// ShapeUnspecified means no shape was chosen. As PlanOptions.Force it lets the
	// planner choose.
	ShapeUnspecified Shape = iota

	// ShapeAtomic is one request for each item. Every descriptor can run this way.
	ShapeAtomic

	// ShapeBatched is one request for several items: each item is sent with an id
	// the request makes for it, and the answer is read back by those ids. It is
	// available to a descriptor whose Batch declares a class of BatchItemwise or
	// BatchAggregate and has the functions to run it.
	ShapeBatched

	// ShapeSplit is one datum taken through several requests, each asking about
	// the answer of the one before. It is the shape of a pipeline of stages and
	// not of one descriptor, so forcing it on a descriptor makes the plan
	// ineligible and RunMany refuses it.
	ShapeSplit

	// ShapeGlobal is a batch whose answer is checked as a whole: a descriptor
	// whose Batch.Class is BatchAggregate and has the functions to run it.
	ShapeGlobal
)

// String returns the shape's name in lower case: "atomic", "batched", "split",
// "global", and "unspecified" for the zero value and for a number that is not a
// shape. It is the Strategy of a Record that RunBatch and RunMany add.
func (s Shape) String() string {
	switch s {
	case ShapeAtomic:
		return "atomic"
	case ShapeBatched:
		return "batched"
	case ShapeSplit:
		return "split"
	case ShapeGlobal:
		return "global"
	}
	return "unspecified"
}

// valid reports whether s is one of the five shapes.
func (s Shape) valid() bool { return s <= ShapeGlobal }

// internal is the internal shape of s.
func (s Shape) internal() types.ExecutionShape {
	switch s {
	case ShapeAtomic:
		return types.ShapeAtomic
	case ShapeBatched:
		return types.ShapeMDSP
	case ShapeSplit:
		return types.ShapeSDMP
	case ShapeGlobal:
		return types.ShapeGlobal
	}
	return types.ShapeUnspecified
}

// shapeOf is the Shape of an internal one.
func shapeOf(s types.ExecutionShape) Shape {
	switch s {
	case types.ShapeAtomic:
		return ShapeAtomic
	case types.ShapeMDSP:
		return ShapeBatched
	case types.ShapeSDMP:
		return ShapeSplit
	case types.ShapeGlobal:
		return ShapeGlobal
	}
	return ShapeUnspecified
}

// Algebra says what a batch's answer has to be as a whole, which decides how a
// batched answer is checked. The zero value is no algebra declared.
type Algebra uint8

const (
	// AlgebraIndependent means each output depends only on its own input, so
	// batching is only a way to send the items together.
	AlgebraIndependent Algebra = iota + 1

	// AlgebraSubset means the outputs are some of the inputs, chosen by a rule.
	AlgebraSubset

	// AlgebraPermutation means the outputs are every input, in another order.
	AlgebraPermutation

	// AlgebraPartition means the outputs divide the inputs into groups that
	// share no input and leave none out.
	AlgebraPartition

	// AlgebraGraph means the outputs describe relationships between inputs.
	AlgebraGraph

	// AlgebraHierarchical means the outputs are made in stages, each reading the
	// answers of the one before.
	AlgebraHierarchical

	// AlgebraSequential means each output depends on the outputs before it, in a
	// fixed order.
	AlgebraSequential
)

// String returns the algebra's name in lower case, and "unspecified" for the
// zero value and for a number that is not an algebra.
func (a Algebra) String() string {
	switch a {
	case AlgebraIndependent:
		return "independent"
	case AlgebraSubset:
		return "subset"
	case AlgebraPermutation:
		return "permutation"
	case AlgebraPartition:
		return "partition"
	case AlgebraGraph:
		return "graph"
	case AlgebraHierarchical:
		return "hierarchical"
	case AlgebraSequential:
		return "sequential"
	}
	return "unspecified"
}

// internal is the internal kind of a.
func (a Algebra) internal() types.AlgebraKind {
	switch a {
	case AlgebraIndependent:
		return types.AlgebraIndependent
	case AlgebraSubset:
		return types.AlgebraSubset
	case AlgebraPermutation:
		return types.AlgebraPermutation
	case AlgebraPartition:
		return types.AlgebraPartition
	case AlgebraGraph:
		return types.AlgebraGraph
	case AlgebraHierarchical:
		return types.AlgebraHierarchical
	case AlgebraSequential:
		return types.AlgebraSequential
	}
	return types.AlgebraUnspecified
}

// BatchClass says how a descriptor composes across many inputs. The zero value
// is BatchUnspecified, which nobody has said.
type BatchClass uint8

const (
	// BatchUnspecified means the descriptor does not say. A descriptor declared
	// StabilityStable may not be left in this state: New refuses it.
	BatchUnspecified BatchClass = iota

	// BatchNone means the descriptor has no batch form: each input is its own
	// request and nothing merges them.
	BatchNone

	// BatchItemwise means several inputs are sent in one request and answered by
	// as many outputs, each standing on its own: no check spans the batch.
	BatchItemwise

	// BatchAggregate means the batch has a check that only makes sense across the
	// whole set, which Batch.GlobalValidate makes.
	BatchAggregate
)

// String returns the class's name in lower case, and "unspecified" for the zero
// value and for a number that is not a class.
func (c BatchClass) String() string {
	switch c {
	case BatchNone:
		return "none"
	case BatchItemwise:
		return "itemwise"
	case BatchAggregate:
		return "aggregate"
	}
	return "unspecified"
}

// internal is the internal class of c.
func (c BatchClass) internal() types.BatchClass {
	switch c {
	case BatchNone:
		return types.BatchNone
	case BatchItemwise:
		return types.BatchItemwise
	case BatchAggregate:
		return types.BatchAggregate
	}
	return types.BatchUnspecified
}

// Category says what kind of thing an operation asks a model to do. The zero
// value is no category declared.
type Category uint8

const (
	// CategoryExtract reads values out of something that already says them.
	CategoryExtract Category = iota + 1

	// CategoryTransform changes the form of something that is given.
	CategoryTransform

	// CategoryGenerate writes something new.
	CategoryGenerate

	// CategoryClassify sorts what it is given into kinds or scores it.
	CategoryClassify

	// CategorySelect chooses among what it is given.
	CategorySelect

	// CategoryOrder puts what it is given in an order.
	CategoryOrder

	// CategoryReview judges something that exists.
	CategoryReview

	// CategoryValidate checks something against rules.
	CategoryValidate
)

// String returns the category's name in lower case: "extract", "transform",
// "generate", "classify", "select", "order", "review", "validate", and
// "unspecified" for the zero value and for a number that is not a category.
func (c Category) String() string {
	switch c {
	case CategoryExtract:
		return "extract"
	case CategoryTransform:
		return "transform"
	case CategoryGenerate:
		return "generate"
	case CategoryClassify:
		return "classify"
	case CategorySelect:
		return "select"
	case CategoryOrder:
		return "order"
	case CategoryReview:
		return "review"
	case CategoryValidate:
		return "validate"
	}
	return "unspecified"
}

// internal is the internal category of c. The internal type has five values and
// this one eight, so the finer ones share: classifying, reviewing and validating
// are judgments, and ordering is a selection.
func (c Category) internal() types.Category {
	switch c {
	case CategoryExtract:
		return types.CategoryExtraction
	case CategoryTransform:
		return types.CategoryTransformation
	case CategoryGenerate:
		return types.CategoryGeneration
	case CategorySelect, CategoryOrder:
		return types.CategorySelection
	case CategoryClassify, CategoryReview, CategoryValidate:
		return types.CategoryJudgment
	}
	return types.CategoryUnspecified
}

// Stability is how firmly a descriptor's contract is meant to hold. The zero
// value is none declared.
type Stability uint8

const (
	// StabilityUnspecified means the descriptor does not say.
	StabilityUnspecified Stability = iota

	// StabilityExperimental means the descriptor works and its contract may
	// still change. New does not hold it to declaring a batch class.
	StabilityExperimental

	// StabilityStable means the descriptor's contract is defined and a change to
	// it is a breaking change. New refuses a stable descriptor that does not
	// declare its batch class, and one that declares a batch of several inputs
	// without saying what shape the batch has (Batch.Kind).
	StabilityStable
)

// String returns the stability's name in lower case, and "unspecified" for the
// zero value and for a number that is not a stability.
func (s Stability) String() string {
	switch s {
	case StabilityExperimental:
		return "experimental"
	case StabilityStable:
		return "stable"
	}
	return "unspecified"
}

// internal is the internal tier of s.
func (s Stability) internal() types.StabilityTier {
	switch s {
	case StabilityExperimental:
		return types.StabilityExperimental
	case StabilityStable:
		return types.StabilityStable
	}
	return types.StabilityUnspecified
}

// tierOf is the root's Tier of an internal speed, which the descriptor's functions
// are given in Options.
func tierOf(s types.Speed) schemaflux.Tier {
	switch s {
	case types.TierUnset:
		return 0
	case types.Smart:
		return schemaflux.TierSmart
	case types.Fast:
		return schemaflux.TierFast
	case types.Quick:
		return schemaflux.TierQuick
	}
	return schemaflux.Tier(s)
}

// modeOf is the root's Mode of an internal one.
func modeOf(m types.Mode) schemaflux.Mode {
	switch m {
	case types.ModeUnset:
		return 0
	case types.Strict:
		return schemaflux.ModeStrict
	case types.TransformMode:
		return schemaflux.ModeTransform
	case types.Creative:
		return schemaflux.ModeCreative
	}
	return schemaflux.Mode(m)
}
