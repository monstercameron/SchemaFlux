package ops

import (
	"context"
	"reflect"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A Sink is a recorder as this package sees one. A client holds one on its
// snapshot (ExecConfig.Sink), and a caller may add more to a context with
// WithSink; each is given the outcome of every call made under it.
type Sink interface {
	// Wants reports what the sink asked for beyond the record itself.
	Wants() (decisions, bodies bool, maxBody int)

	// Add is given the outcome of one call. It is called once per call.
	Add(ctx context.Context, o *Outcome)
}

// An Outcome is what happened during one call of an operation.
type Outcome struct {
	Meta  types.Meta
	Start time.Time

	// Err is the error as the call returns it.
	Err error

	// Asked holds the questions and answers of a decision, when a sink wants
	// decisions.
	Asked []AnsweredQuestion

	// Body is the refused answer, cut and scrubbed, when a sink wants bodies.
	Body string

	// The facts of who answered a decision, as DecisionRecord reports them
	// (NoteDecision fills them). They are set only when the call made decisions
	// and every one of them says the same: a call that asked two decisions and
	// had them answered differently says nothing here, and each decision's own
	// facts are on the records the decision observer is told.
	//
	// AnsweredBy is the provider that gave the answers that were read. Typed is
	// true when they came typed from a decision provider. AskedFirst is the
	// decision provider that was asked first and passed over, and Escalated
	// says why. AskedFirstModelConfidence is the probability that provider
	// reported for the answer that was not used, when it reported one.
	AnsweredBy                string
	Typed                     bool
	AskedFirst                string
	Escalated                 string
	AskedFirstModelConfidence *float64

	// decisions is how many decisions NoteDecision has been given, and
	// disagreed is set when two of them did not say the same of who answered.
	decisions int
	disagreed bool
}

// NoteDecision folds the record of one decision into the outcome: its questions
// and answers into Asked when keepAsked is set, and who answered into the facts
// above. The first decision of a call sets them; a later one that says
// something else of who answered clears them, since an outcome of several
// decisions has no one answer to attribute.
func (o *Outcome) NoteDecision(record DecisionRecord, keepAsked bool) {
	if o == nil {
		return
	}
	if keepAsked {
		o.Asked = append(o.Asked, record.Asked...)
	}
	if o.decisions > 0 && !o.disagreed && !o.sameAnswerer(record) {
		o.disagreed = true
	}
	o.decisions++
	if o.disagreed {
		o.AnsweredBy, o.Typed, o.AskedFirst, o.Escalated, o.AskedFirstModelConfidence = "", false, "", "", nil
		return
	}
	o.AnsweredBy, o.Typed, o.AskedFirst, o.Escalated = record.AnsweredBy, record.Typed, record.AskedFirst, record.Escalated
	o.AskedFirstModelConfidence = nil
	if record.AskedFirstModelConfidence != nil {
		confidence := *record.AskedFirstModelConfidence
		o.AskedFirstModelConfidence = &confidence
	}
}

// sameAnswerer reports whether record says what the outcome already says of
// who answered.
func (o *Outcome) sameAnswerer(record DecisionRecord) bool {
	if o.AnsweredBy != record.AnsweredBy || o.Typed != record.Typed || o.AskedFirst != record.AskedFirst || o.Escalated != record.Escalated {
		return false
	}
	if (o.AskedFirstModelConfidence == nil) != (record.AskedFirstModelConfidence == nil) {
		return false
	}
	return o.AskedFirstModelConfidence == nil || *o.AskedFirstModelConfidence == *record.AskedFirstModelConfidence
}

// sinkKey and sinkNode hold the sinks of a context as a list, the most recent
// first, so adding one is a single context value that points at the sinks
// before it and reading them allocates nothing.
type sinkKey struct{}

type sinkNode struct {
	sink Sink
	next *sinkNode
}

func sinksFrom(ctx context.Context) *sinkNode {
	if ctx == nil {
		return nil
	}
	head, _ := ctx.Value(sinkKey{}).(*sinkNode)
	return head
}

// WithSink returns a context whose calls also report to s, beside every sink
// already on it. A nil s returns ctx. A nil ctx panics, as context.WithValue
// does.
func WithSink(ctx context.Context, s Sink) context.Context {
	if s == nil {
		return ctx
	}
	if ctx == nil {
		panic("cannot create context from nil parent")
	}
	return context.WithValue(ctx, sinkKey{}, &sinkNode{sink: s, next: sinksFrom(ctx)})
}

// HasSink reports whether any sink would receive a call made under ctx on cfg.
// It allocates nothing.
func HasSink(ctx context.Context, cfg *ExecConfig) bool {
	return sinksFrom(ctx) != nil || (cfg != nil && cfg.Sink != nil)
}

// EachSink calls yield with every sink of a call: those on the context, the
// nearest first, then cfg's; a sink that appears twice is given once.
func EachSink(ctx context.Context, cfg *ExecConfig, yield func(Sink)) {
	if yield == nil {
		return
	}
	// A call has a handful of sinks at most, so the ones already given are
	// kept in an array on the stack and the list is searched, not hashed.
	var buffer [8]Sink
	given := buffer[:0]
	for node := sinksFrom(ctx); node != nil; node = node.next {
		if !sinkIn(given, node.sink) {
			given = append(given, node.sink)
			yield(node.sink)
		}
	}
	if cfg != nil && cfg.Sink != nil && !sinkIn(given, cfg.Sink) {
		yield(cfg.Sink)
	}
}

// sinkIn reports whether s is one of given. Two sinks are the same sink when
// they have one type and compare equal. A sink of a type that cannot be
// compared (a struct holding a slice or a map) is never the same as another,
// since == on it would panic: it is given again, which is a repeat the caller
// can see, and not a crash it cannot.
func sinkIn(given []Sink, s Sink) bool {
	value := reflect.ValueOf(s)
	if !value.IsValid() || !value.Comparable() {
		return false
	}
	for _, earlier := range given {
		other := reflect.ValueOf(earlier)
		if other.Type() == value.Type() && other.Comparable() && earlier == s {
			return true
		}
	}
	return false
}

type decisionObserverKey struct{}

// WithDecisionObserver returns a context whose decisions are also told to fn,
// beside every observer already on the context: the nearest is told first. A
// nil fn returns ctx. A nil ctx panics, as context.WithValue does.
func WithDecisionObserver(ctx context.Context, fn func(context.Context, DecisionRecord)) context.Context {
	if fn == nil {
		return ctx
	}
	if ctx == nil {
		panic("cannot create context from nil parent")
	}
	// The observers are joined once, here, so that telling them is the call
	// of one function and not a walk that allocates for every decision.
	if outer := decisionObserverFrom(ctx); outer != nil {
		inner := fn
		fn = func(c context.Context, record DecisionRecord) {
			inner(c, record)
			outer(c, record)
		}
	}
	return context.WithValue(ctx, decisionObserverKey{}, fn)
}

// decisionObserverFrom returns the observer WithDecisionObserver put on ctx
// (the nearest, joined to those before it), or nil when there is none.
func decisionObserverFrom(ctx context.Context) func(context.Context, DecisionRecord) {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(decisionObserverKey{}).(func(context.Context, DecisionRecord))
	return fn
}

type parentResultIDsKey struct{}

// WithParentResultIDs returns a context whose calls name ids as the parents of
// what they produce. It replaces the ids of the context it is given, and with
// no ids it says the calls have none. The ids are copied. A nil ctx panics, as
// context.WithValue does.
func WithParentResultIDs(ctx context.Context, ids ...string) context.Context {
	return context.WithValue(ctx, parentResultIDsKey{}, append([]string(nil), ids...))
}

// ParentResultIDs returns a copy of the ids WithParentResultIDs put on ctx, or
// nil when there are none.
func ParentResultIDs(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	ids, _ := ctx.Value(parentResultIDsKey{}).([]string)
	return append([]string(nil), ids...)
}
