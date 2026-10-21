package schemaflux

import (
	"context"
	"slices"
	"sync"

	"github.com/monstercameron/schemaflux/v2/internal/ops"
)

// Recorder collects a Record of each call made under a context it was placed
// on (WithRecorder) or on a client it was given to (Client.Recorder). The zero
// Recorder is ready to use and keeps every Record until Reset.
//
// A Record holds what happened and never what was said: no input, no prompt and
// no answer. Decisions and Bodies are the only way to change that, and each is
// off unless set.
//
// The exported fields are settings: set them before the recorder is used and do
// not change them after; a Recorder reads them once, at first use. A Recorder is
// safe for concurrent use and must not be copied after first use. Copies of a
// Client share the Recorder it points to, and the Recorder is not modified
// after first use.
//
// A recorder with no Handle keeps every Record until Reset: on a client, set
// Handle or call Reset.
type Recorder struct {
	// Decisions makes the Record of a decision (Is, Which, WhichOrNone, Rate,
	// Assess, their Detail forms, Whether and RouteBy) carry what was asked and
	// what was read: Record.Asked. These are the caller's questions and the
	// model's answers. False keeps them out. flow.Decide and flow.Match do not
	// fill Asked.
	Decisions bool

	// Bodies makes the Record of a call whose answer was refused carry that
	// answer: Record.Body, cut to MaxBody bytes with anything that looks like a
	// credential removed. It is the model's text and can quote the caller's
	// input. False keeps it out.
	Bodies bool

	// MaxBody is the most bytes of an answer kept when Bodies is set. Zero
	// means 4096. A negative value is a configuration error wherever the
	// recorder is attached: a call on a client that has it, or under a context
	// that carries it (WithRecorder), fails with ErrConfiguration before anything
	// is sent, and the recorder is given no Record of that call.
	MaxBody int

	// Handle, when not nil, is called with each Record when it is complete, and
	// the recorder then keeps nothing: Records and Total stay empty. For a call
	// it runs on the goroutine that made the call, before the call returns, so
	// it delays that return, and a panic in it is not recovered: it is the
	// caller's code, and as with a log/slog Handler it reaches the caller of the
	// operation. For a stream it runs when the stream ends, on a goroutine the
	// library started, where a panic could not be caught by anyone and would end
	// the program: there the library recovers it, logs its type and nothing of its
	// value, and the stream ends as it would have. A Handle that can panic should
	// recover for itself. No lock of the library's is held. Calls that run at once
	// call it at once. Nil means the Records are kept.
	Handle func(ctx context.Context, rec Record)

	once     sync.Once
	settings recorderSettings
	mu       sync.Mutex
	records  []Record
}

// recorderSettings are the exported fields of a Recorder as they were when it
// was first used.
type recorderSettings struct {
	decisions bool
	bodies    bool
	maxBody   int
	handle    func(ctx context.Context, rec Record)
}

// defaultMaxBody is MaxBody when it is zero.
const defaultMaxBody = 4096

// read returns the settings, taking them from the exported fields on first use.
func (r *Recorder) read() *recorderSettings {
	r.once.Do(func() {
		r.settings = recorderSettings{
			decisions: r.Decisions,
			bodies:    r.Bodies && r.MaxBody >= 0,
			maxBody:   r.MaxBody,
			handle:    r.Handle,
		}
		if r.settings.maxBody == 0 {
			r.settings.maxBody = defaultMaxBody
		}
	})
	return &r.settings
}

// check reports a setting of the recorder that no call can use, as the
// configuration error a call that would use it returns. It is for the code that
// checks a Client and for the call helper, which asks every recorder of a call.
func (r *Recorder) check() error {
	if r != nil && r.MaxBody < 0 {
		return &Error{Kind: ErrConfiguration, Message: "Recorder.MaxBody must not be negative"}
	}
	return nil
}

// sink is a Recorder as the call path sees one. It is the same struct under
// another name so that *Recorder converts to *sink and back, and so that the
// two methods the call path needs, Wants and Add, are not methods a caller
// sees on a Recorder.
type sink Recorder

// Check reports a setting of the recorder that no call can use, as the
// configuration error the call that would use it returns. The call helper asks
// every recorder of a call, those on the context included, and gives a recorder
// that fails it no Record.
func (s *sink) Check() error { return (*Recorder)(s).check() }

// Wants reports what the recorder asked for beyond the Record itself: the
// questions and answers of a decision, the refused body and its size limit.
func (s *sink) Wants() (decisions, bodies bool, maxBody int) {
	settings := (*Recorder)(s).read()
	return settings.decisions, settings.bodies, settings.maxBody
}

// Add makes the Record of one call from its outcome, holding only what this
// recorder asked for, and either hands it to Handle or keeps it.
func (s *sink) Add(ctx context.Context, o *ops.Outcome) {
	if o == nil {
		return
	}
	r := (*Recorder)(s)
	settings := r.read()
	rec := recordFromOutcome(o, settings.decisions, settings.bodies, settings.maxBody)
	if settings.handle != nil {
		settings.handle(ctx, rec)
		return
	}
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
}

// WithRecorder returns a context whose calls are recorded by r. Every operation
// called with the returned context, or with one derived from it, adds one
// Record to r when it returns, whether it succeeded or failed. A recorder placed
// on a context that already has one is added to it: every recorder on the
// context and the client's Recorder each receive the Record, once, the nearest
// first. A nil r returns ctx. A nil ctx panics, as context.WithValue does.
//
//	var rec schemaflux.Recorder
//	ctx := schemaflux.WithRecorder(ctx, &rec)
//	invoice, err := schemaflux.Extract[Invoice](ctx, text)
//	spent := rec.Total().Cost
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	if r == nil {
		return ctx
	}
	return ops.WithSink(ctx, (*sink)(r))
}

// Records returns the records collected so far, in the order their calls
// returned. The slice is the caller's. Empty when Handle is set.
func (r *Recorder) Records() []Record {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

// Total returns the records added up, for a caller who wants one figure for
// everything a context did. Requests, Retries, Repairs, CacheHits, every field
// of Tokens, Cost.Total, Cost.Input, Cost.Output, Cost.Cached, Cost.Reasoning,
// the durations of Timing and Timing.Calls are sums, held at the largest number
// there is where a sum would pass it. Start is the earliest, and Elapsed runs
// from it to the latest finish, so records that overlapped add up to the time
// that passed and not to more. Err is the first error, in the order of the
// records. Hedged and Cost.Estimated are true if true of any record.
// Cost.Priced is true if some request was sent and every request that was sent
// was priced: a call refused before anything was sent has no request to price and
// does not make the figure a floor, as an answer a cache served does not.
// Timing.Reused is true if some request was timed and every record that timed one
// says it reused its connection. Requested and Delivered are the
// weakest. Op, Provider, Model, RequestedModel, ServiceTier, RequestID,
// CorrelationID, SchemaID, Strategy, AnsweredBy, AskedFirst, Escalated,
// Cost.Currency and Cost.Source are set when every record agrees on them and are
// empty otherwise; Typed is true if true of all. Checks, Asked, Body,
// Provenance, AskedFirstConfidence and AskedFirstReported are not carried. The
// Total of none is the zero Record. Empty when Handle is set.
func (r *Recorder) Total() Record {
	if r == nil {
		return Record{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return sumRecords(r.records)
}

// Reset forgets every record collected so far.
func (r *Recorder) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.records = nil
	r.mu.Unlock()
}
