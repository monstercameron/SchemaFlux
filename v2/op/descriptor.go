package op

import (
	"encoding/json"
	"strconv"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/types"
)

// ID names a descriptor and the version of the question it asks. The version is
// there because an edit of a prompt changes what every caller gets back and
// changes no Go signature: it is the one place the edit is written down.
type ID struct {
	// Name is the descriptor's stable name, such as "extract-invoice". It is the
	// Op of the Record of every call made with the descriptor and the Op of its
	// errors, and the name an observer reports. Empty means the name "op.run",
	// "op.runbatch" or "op.runmany" of the function that was called.
	Name string

	// Version is bumped when the descriptor's behaviour changes in a way its
	// types do not show. It is part of the provenance of an answer. Empty means
	// unversioned.
	Version string
}

// String returns the identity as "name@version": "unnamed@version" for an empty
// name and "name@unversioned" for an empty version.
func (id ID) String() string { return id.internal().String() }

// internal is the internal identity of id.
func (id ID) internal() types.OperationID {
	return types.OperationID{Name: id.Name, Version: id.Version}
}

// Semantics says what a descriptor is allowed to do to its input and what it
// promises. It is the author's statement about the operation, kept with it for a
// reader or a planner: the library does not enforce what it says. The zero value
// says nothing.
type Semantics struct {
	// Category is the kind of thing the operation asks a model to do. The zero
	// value means none declared.
	Category Category

	// InferencePermitted says that the operation may produce values that are not
	// literally in its input, as an extraction may infer a missing field. False
	// says the operation is closed-world.
	InferencePermitted bool

	// PreservesIdentity says that the outputs correspond one to one with the
	// inputs by value, as a selection's do.
	PreservesIdentity bool

	// PreservesOrder says that the operation returns its items in the order it
	// received them.
	PreservesOrder bool

	// Stability is how firmly the contract is meant to hold. New refuses a stable
	// descriptor that does not say how it batches. The zero value means none
	// declared.
	Stability Stability
}

// internal is the internal semantics of s.
func (s Semantics) internal() types.Semantics {
	return types.Semantics{
		Category:           s.Category.internal(),
		InferencePermitted: s.InferencePermitted,
		PreservesIdentity:  s.PreservesIdentity,
		PreservesOrder:     s.PreservesOrder,
		Stability:          s.Stability.internal(),
	}
}

// Contract is what an answer has to satisfy and how a raw answer becomes a value
// of Out. Its functions are pure: they read what they are given and return, with
// no network, no retry and no knowledge of how many attempts came before, and
// the library calls them again for every attempt. A function of the contract that
// panics ends the call with an error of kind ErrInternal and never ends the
// process.
type Contract[Out any] struct {
	// SchemaName names the contract in logs and cache keys. It does not send a
	// schema: a request carries one only when the caller passes
	// Options.JSONSchema, and a descriptor that names a schema it never sends is
	// delivered at ContractJSONWellFormed, not at ContractSchemaConstrained.
	// Empty means no name, and the answer is held to no more than that it parses.
	SchemaName string

	// Decode turns the body of an answer into a value of Out, or says why it
	// cannot. It is required. An error it returns is shown to the model, with the
	// answer, and the model is asked again, up to Policy.RepairAttempts times, so
	// its text has to say what was wrong in terms the model can act on. The text
	// is also kept in Record.Checks, so it has to hold nothing that must not be
	// sent to the model or recorded: the position of a fault, not the value.
	Decode func(body string) (Out, error)

	// Invariants are checked in order after Decode succeeds, and the first that
	// returns an error refuses the answer as Decode's error does. They are what
	// makes a descriptor deliver ContractSchemaAndInvariantChecked. Nil means
	// none. An entry that is nil is refused before anything is sent.
	Invariants []func(Out) error

	// Normalize runs once after every invariant passed, and cannot fail: it is for
	// cleanup that must not turn an accepted answer into a refused one, such as
	// trimming or canonical case. Nil means none.
	Normalize func(Out) Out
}

// Batch says how a descriptor composes across many inputs, for RunBatch and for
// the batched shape of RunMany. The zero value declares nothing, and the
// descriptor then runs one request for each item.
//
// A batch is written one of two ways. Item makes it the library's id protocol:
// each item is sent with an id the request makes for it, the answer names the ids
// it answers, every id has to be answered once and Item decodes one item's
// answer. Encode and Merge make it whatever those functions do. The two ways
// exclude each other, and the first is what a descriptor should use: it is the
// one whose every answer is checked against the ids that were offered.
type Batch[In, Out any] struct {
	// Class says how the descriptor composes across many inputs. A descriptor
	// declared StabilityStable has to say; BatchNone says it does not.
	Class BatchClass

	// Kind says what shape the batch's answer has as a whole. A stable descriptor
	// whose Class is BatchItemwise or BatchAggregate has to say. The zero value
	// means none declared.
	Kind Algebra

	// Encode renders the system and the user prompt of one request that carries
	// every item of items. It is used when Item is nil, and has to be nil when Item
	// is set. An error it returns refuses the batch before anything is sent, with
	// the kind it carries, or with ErrConfiguration when it carries none.
	Encode func(items []In, opts Options) (system, user string, err error)

	// Merge turns the body of the answer to that request into one output for each
	// of items, in order. It is used when Item is nil, and has to be nil when Item
	// is set. The library cannot see into it: what it checks is what Merge checks.
	Merge func(body string, items []In) ([]Out, error)

	// GlobalValidate checks the outputs of a batch against its items as a whole,
	// for BatchAggregate: that a permutation holds every item once, that a
	// partition leaves none out. Nil means no whole-batch check. An error it
	// returns refuses the batch.
	GlobalValidate func(items []In, outputs []Out) error

	// Instructions is the system prompt of a batch that uses the id protocol: what
	// to do with each item. It is required with Item and has to be empty without
	// it.
	Instructions string

	// Item, when it is not nil, makes the batch the id protocol: it decodes the
	// answer to one item from the raw JSON the answer holds for it. Nil means the
	// batch is whatever Encode and Merge do.
	Item func(raw json.RawMessage) (Out, error)
}

// Policy is a descriptor's own settings, which apply when a caller states no
// opinion. The zero value is no setting.
type Policy struct {
	// RepairAttempts is how many times the descriptor lets the model be shown its
	// own refused answer and asked again, each a request that is paid for. Zero
	// means the client's Repairs, and one when the client states none. A negative
	// value means none: an answer that is refused is an error.
	RepairAttempts int
}

// Descriptor is what an operation is, apart from running it: its identity, what
// it promises, the contract its answer has to satisfy, how it batches and how it
// renders a request. It holds no context and no provider, only data and pure
// functions, so that a descriptor can be read, planned and priced before a
// request is made, and the same descriptor is run by Run, RunBatch and RunMany.
//
// A descriptor is a value: copy it, declare it at package level, and pass it to
// the functions of this package. Declare it with New or Must to have it checked
// when it is made and not when it is first run.
type Descriptor[In, Out any] struct {
	// ID names the descriptor and its version.
	ID ID

	// Semantics is what the descriptor says about itself.
	Semantics Semantics

	// Contract is what the answer has to satisfy. Its Decode is required.
	Contract Contract[Out]

	// Batch is how the descriptor composes across many inputs.
	Batch Batch[In, Out]

	// Policy is the descriptor's own settings.
	Policy Policy

	// BuildPrompt renders the system and the user prompt of the request for one
	// input. It is required. It is a pure function of the input and the options
	// the call resolved: it is called again for every attempt, and by Preflight
	// to price a run, and it must do nothing but return the prompts. The caller's
	// data belongs in the user prompt. A BuildPrompt that panics ends the call with
	// an error of kind ErrInternal and never ends the process.
	BuildPrompt func(input In, opts Options) (system, user string)
}

// New checks d and returns it. It refuses with ErrConfiguration a descriptor that
// can never run: one with no BuildPrompt or no Contract.Decode, an Invariants
// entry that is nil, a Batch that sets Item beside Encode or Merge, sets Item with
// no Instructions, or sets Instructions without Item, a Class, Kind, Category or
// Stability that is not one of the constants, and a descriptor declared
// StabilityStable that does not say how it batches. A stable descriptor is a
// promise about behaviour a caller can rely on, and how it behaves in a batch is
// part of it.
//
// Run, RunBatch, RunMany and Preflight make the first of those checks themselves
// on a descriptor that was never given to New, before anything is sent. New is
// how a program finds out when it starts.
//
// The error's Op is "op.new". New asks no model and adds no Record.
func New[In, Out any](d Descriptor[In, Out]) (Descriptor[In, Out], error) {
	if err := checkDescriptor(d, "op.new"); err != nil {
		return Descriptor[In, Out]{}, bind.ConvertError("op.new", err)
	}
	if err := checkStable(d, "op.new"); err != nil {
		return Descriptor[In, Out]{}, bind.ConvertError("op.new", err)
	}
	return d, nil
}

// Must is New for a descriptor declared at package level, where there is no
// caller to return an error to. It panics with the error New returns, so a binary
// whose descriptors are wrong fails to start and not later on one code path.
func Must[In, Out any](d Descriptor[In, Out]) Descriptor[In, Out] {
	checked, err := New(d)
	if err != nil {
		panic(err)
	}
	return checked
}

// checkDescriptor returns the internal error of kind configuration for a
// descriptor that cannot run, naming op, and nil for one that can. It is the
// check of every function that takes a descriptor. It reports what is wrong in
// the library's words and never a value of the descriptor.
func checkDescriptor[In, Out any](d Descriptor[In, Out], op string) error {
	switch {
	case d.BuildPrompt == nil:
		return types.Newf(types.KindConfiguration, op, "the descriptor has no BuildPrompt to render a request with")
	case d.Contract.Decode == nil:
		return types.Newf(types.KindConfiguration, op, "the descriptor has no Contract.Decode to read an answer with")
	}
	for i, invariant := range d.Contract.Invariants {
		if invariant == nil {
			return types.Newf(types.KindConfiguration, op, "Contract.Invariants entry %s is nil", strconv.Itoa(i))
		}
	}
	b := d.Batch
	switch {
	case !b.Class.known():
		return types.Newf(types.KindConfiguration, op, "Batch.Class is not one of BatchNone, BatchItemwise and BatchAggregate")
	case !b.Kind.known():
		return types.Newf(types.KindConfiguration, op, "Batch.Kind is not one of the Algebra constants")
	case !d.Semantics.Category.known():
		return types.Newf(types.KindConfiguration, op, "Semantics.Category is not one of the Category constants")
	case !d.Semantics.Stability.known():
		return types.Newf(types.KindConfiguration, op, "Semantics.Stability is not one of StabilityExperimental and StabilityStable")
	case b.Item != nil && (b.Encode != nil || b.Merge != nil):
		return types.Newf(types.KindConfiguration, op, "Batch.Item is set beside Batch.Encode or Batch.Merge: the id protocol supplies both")
	case b.Item != nil && b.Instructions == "":
		return types.Newf(types.KindConfiguration, op, "Batch.Item is set and Batch.Instructions is empty")
	case b.Item == nil && b.Instructions != "":
		return types.Newf(types.KindConfiguration, op, "Batch.Instructions is set without Batch.Item")
	}
	return nil
}

// checkStable returns the internal error of kind configuration for a descriptor
// that declares itself stable and does not say how it batches.
func checkStable[In, Out any](d Descriptor[In, Out], op string) error {
	if d.Semantics.Stability != StabilityStable {
		return nil
	}
	switch {
	case d.Batch.Class == BatchUnspecified:
		return types.Newf(types.KindConfiguration, op, "the descriptor is declared stable and has no Batch.Class: say how it batches, BatchNone if it does not")
	case (d.Batch.Class == BatchItemwise || d.Batch.Class == BatchAggregate) && d.Batch.Kind == 0:
		return types.Newf(types.KindConfiguration, op, "the descriptor is declared stable with a batch of several inputs and no Batch.Kind: say what shape the batch has")
	}
	return nil
}

// known reports whether c is no class or one of the three classes.
func (c BatchClass) known() bool { return c <= BatchAggregate }

// known reports whether a is no algebra or one of the seven.
func (a Algebra) known() bool { return a <= AlgebraSequential }

// known reports whether c is no category or one of the eight.
func (c Category) known() bool { return c <= CategoryValidate }

// known reports whether s is no stability or one of the two.
func (s Stability) known() bool { return s <= StabilityStable }
