package schemaflux

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// A Recorder collects a Record of each call made under a context it was placed
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

// A Record is what happened during one call of one operation: who answered,
// what it took and what it cost. It is not part of the call's answer and never
// carries the caller's data or the model's text, except for Asked and Body,
// which a Recorder fills only when it was told to.
type Record struct {
	// Op is the operation, in lower case: "extract", "classify", "is". Empty
	// only in a Record that no call made.
	Op string `json:"op"`

	// Start is when the call began; the zero time means it is not known.
	// Elapsed is how long the call took from then until it returned; zero means
	// it took no measurable time or is not known.
	Start   time.Time     `json:"start,omitzero"`
	Elapsed time.Duration `json:"-"`

	// Err is the error the call returned, an *Error, or nil, which means the
	// call succeeded.
	Err error `json:"error,omitempty"`

	// Provider and Model are who answered, as the provider reported it. They
	// are empty when nothing answered.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// RequestedModel is the model the call asked for. It differs from Model
	// when the provider substituted one, or reported a dated revision of the
	// one asked for. Empty when nothing was asked.
	RequestedModel string `json:"requested_model,omitempty"`

	// ServiceTier is the service tier the provider says the answer was served
	// on, when it says. Empty means it said none.
	ServiceTier string `json:"service_tier,omitempty"`

	// RequestID identifies the call in the library's logs and errors. A call
	// that sent several requests under ids of its own reports the last.
	// CorrelationID is the id the caller's context carried, or the request id.
	// An id is the caller's own; do not make one from data. Both are empty when
	// the call sent nothing.
	RequestID     string `json:"request_id,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`

	// SchemaID identifies the shape the answer was held to: name, version and
	// digest. Empty for a text answer.
	SchemaID string `json:"schema_id,omitempty"`

	// Requests is how many requests were sent to a provider for this call: the
	// first, each retry and each repair. A call the library refused before
	// sending anything has none. An answer a cache served is not counted.
	Requests int `json:"requests"`

	// Retries is how many of them repeated a request that failed in transit.
	// Zero means none did.
	Retries int `json:"retries,omitempty"`

	// Repairs is how many of them sent a refused answer back to the model with
	// what was wrong. Zero means none did.
	Repairs int `json:"repairs,omitempty"`

	// CacheHits is how many answers came from a response cache (mw.Cache) with
	// no provider called. They are not in Requests and cost nothing.
	CacheHits int `json:"cache_hits,omitempty"`

	// Hedged reports that a second copy of a request was sent because the first
	// was slow (mw.Hedge). False means no second copy was sent.
	Hedged bool `json:"hedged,omitempty"`

	// Tokens is what the providers reported using, summed over Requests.
	// Tokens.Cached is what was read from a provider's prompt cache. The zero
	// value means nothing was reported.
	Tokens Usage `json:"tokens"`

	// Cost is what that came to at the library's prices. Cost.Priced is false
	// when any request ran on a model the library has no price for, and the
	// figure is then a floor.
	Cost Cost `json:"cost"`

	// Timing is where the time went, summed over the requests that were timed.
	// Zero when nothing was timed.
	Timing Timing `json:"timing"`

	// Requested and Delivered are the guarantee the call asked for and the one
	// its answer carries. Delivered below Requested means the answer is weaker
	// than was asked. Both are zero, ContractPromptOnly, for an operation that
	// establishes no level.
	Requested ContractLevel `json:"requested_contract"`
	Delivered ContractLevel `json:"delivered_contract"`

	// Checks are the checks the library made of the answer in Go and reports,
	// each with its outcome. Only Extract, Sort and the op package's runners
	// report theirs; empty means not reported, not "none made".
	Checks []Check `json:"checks,omitempty"`

	// Strategy names the path taken by an operation that has more than one: for
	// Sort, "trivial", "whole-list", "scoring" or "scoring-fallback". Empty
	// otherwise.
	Strategy string `json:"strategy,omitempty"`

	// Provenance is the lineage of the answer, for Extract and the op package's
	// runners; nil for every other operation.
	Provenance *Provenance `json:"provenance,omitempty"`

	// AnsweredBy is the provider that gave the answers of a decision, as its
	// response named it. Empty for a call that is not a decision and for a
	// decision that failed.
	AnsweredBy string `json:"answered_by,omitempty"`

	// Typed is true when the answers of a decision came typed from a decision
	// provider, and false when a provider that generates text wrote them.
	Typed bool `json:"typed,omitempty"`

	// AskedFirst is the decision provider that was asked before AnsweredBy,
	// when the answers that were read are not its own: its probability was below
	// the floor, or it failed or refused, and the provider that generates text
	// was asked the same questions. Empty when no decision provider was passed
	// over.
	AskedFirst string `json:"asked_first,omitempty"`

	// AskedFirstConfidence is the lowest probability AskedFirst reported for its
	// answers, when AskedFirstReported is true: that model's claim about the
	// answer that was not used. Zero when none was reported.
	AskedFirstConfidence float64 `json:"asked_first_confidence,omitempty"`

	// AskedFirstReported is false when AskedFirst reported no probability,
	// which is not a probability of zero.
	AskedFirstReported bool `json:"asked_first_reported,omitempty"`

	// Escalated is why AskedFirst's answer was passed over: "below the floor",
	// "unavailable" or "unsupported". Empty when AskedFirst is.
	Escalated string `json:"escalated,omitempty"`

	// Asked is what a decision put to the model and what it read back, in the
	// order asked. It is filled only when the Recorder's Decisions is set. A
	// decision that failed lists its questions with nil answers.
	Asked []Asked `json:"asked,omitempty"`

	// Body is the answer that was refused, cut and scrubbed. It is filled only
	// when the Recorder's Bodies is set and the call failed on its answer.
	Body string `json:"body,omitempty"`
}

// Asked is one question of a decision and the answer read for it.
type Asked struct {
	// Question is the question as the caller wrote it. Empty only in an Asked
	// that no decision made.
	Question string `json:"question"`

	// Options are the labels of a choice, in the order they were put. Nil for a
	// yes-or-no and for a rating.
	Options []string `json:"options,omitempty"`

	// Low and High are the ends of a rating's scale, both included. Zero for
	// anything else.
	Low  int `json:"low,omitempty"`
	High int `json:"high,omitempty"`

	// Answer is a bool for a yes-or-no, the label chosen (a string) for a
	// choice and the level (an int) for a rating. It is nil when the decision
	// failed.
	Answer any `json:"answer"`
}

// Cost is what a call came to, in the library's price table. The zero value is
// no cost known, which is not a cost of zero: see Priced.
//
// Every token is priced once, so Total is the sum of Input, Output, Cached and
// Reasoning: the tokens the provider read from its prompt cache are priced in
// Cached and left out of Input, and the tokens spent reasoning are priced in
// Reasoning and left out of Output.
type Cost struct {
	// Total is the whole cost: Input plus Output plus Cached plus Reasoning.
	Total float64 `json:"total"`

	// Input is the cost of the request tokens that were not read from the
	// provider's prompt cache.
	Input float64 `json:"input"`

	// Output is the cost of the answer tokens that were not spent reasoning.
	Output float64 `json:"output"`

	// Cached is the cost of the request tokens read from a provider's prompt
	// cache, at their lower price. It is not part of Input.
	Cached float64 `json:"cached"`

	// Reasoning is the cost of the tokens spent reasoning. It is not part of
	// Output.
	Reasoning float64 `json:"reasoning"`

	// Currency is the currency of the figures: "USD" for every price the library
	// holds, and always set when Priced is. Empty when no price was found and the
	// call path stated none.
	Currency string `json:"currency,omitempty"`

	// Priced reports that every request was priced. False means Total is a
	// floor, not that the call was free. A call that sent no request is Priced
	// with no cost: there was nothing to price.
	Priced bool `json:"priced"`

	// Estimated reports that a price in the sum is an estimate (a family's
	// price applied to an unlisted model).
	Estimated bool `json:"estimated,omitempty"`

	// Source names the price table entry the figures came from. Empty when no
	// price was found.
	Source string `json:"source,omitempty"`
}

// Timing says where the time of the requests to a model went. For one request
// the phases are in the order they happen. Summed over several requests (Calls
// says how many) it is a sum and not a duration on the clock: requests that ran
// at once add up to more than the time that passed. The zero value means
// nothing was timed.
type Timing struct {
	// Calls is how many timed requests this covers.
	Calls int `json:"calls,omitempty"`

	// Reused reports that every timed request went out on a connection that was
	// already open.
	Reused bool `json:"reused,omitempty"`

	// Queued is the time spent waiting for a slot in the client's Scheduler.
	Queued time.Duration `json:"-"`

	// Lookup is the name lookup.
	Lookup time.Duration `json:"-"`

	// Connect is opening the connection.
	Connect time.Duration `json:"-"`

	// Handshake is the TLS handshake.
	Handshake time.Duration `json:"-"`

	// Send is writing the request.
	Send time.Duration `json:"-"`

	// Wait is from the request written to the first byte of the answer: the
	// provider's own time.
	Wait time.Duration `json:"-"`

	// Read is reading the answer.
	Read time.Duration `json:"-"`

	// Network is the whole of the HTTP calls, start to finish.
	Network time.Duration `json:"-"`
}

// MarshalJSON writes the Timing as the rest of the library writes a duration: as
// a string such as "1.5s", under a lower-case name, and left out when zero. An
// Error writes its wait the same way.
func (t Timing) MarshalJSON() ([]byte, error) {
	type plain Timing
	return json.Marshal(struct {
		plain
		Queued    string `json:"queued,omitempty"`
		Lookup    string `json:"lookup,omitempty"`
		Connect   string `json:"connect,omitempty"`
		Handshake string `json:"handshake,omitempty"`
		Send      string `json:"send,omitempty"`
		Wait      string `json:"wait,omitempty"`
		Read      string `json:"read,omitempty"`
		Network   string `json:"network,omitempty"`
	}{
		plain(t), durationText(t.Queued), durationText(t.Lookup), durationText(t.Connect), durationText(t.Handshake),
		durationText(t.Send), durationText(t.Wait), durationText(t.Read), durationText(t.Network),
	})
}

// durationText is d as a string such as "1.5s", and the empty string for zero.
func durationText(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// MarshalJSON writes the Record with lower-case snake_case names, the elapsed
// time as a string such as "25.85ms", and the levels as their names, which is
// how an Error writes itself: a program that sends Records and Errors to one
// place gets one schema. It carries what the Record carries and no more.
func (r Record) MarshalJSON() ([]byte, error) {
	type plain Record
	return json.Marshal(struct {
		plain
		Elapsed string `json:"elapsed,omitempty"`
	}{plain(r), durationText(r.Elapsed)})
}

// Measured reports whether anything was timed. A provider that does not time
// its calls leaves a Timing zero, and zero is not a measurement of an instant
// call.
func (t Timing) Measured() bool { return t.Network > 0 || t.Queued > 0 }

// Setup is what was spent getting a connection: Lookup, Connect and Handshake.
// It is what Client.Warm saves.
func (t Timing) Setup() time.Duration { return t.Lookup + t.Connect + t.Handshake }

// A Check is one check the library made of an answer in Go.
type Check struct {
	// Name says which check: "schema", "subset", "permutation", "numeric
	// fidelity", ...
	Name string `json:"name"`

	// Passed is the outcome. False means the check failed.
	Passed bool `json:"passed"`

	// Detail is why it failed, in shapes and counts; never the answer. Empty
	// for a check that passed.
	Detail string `json:"detail,omitempty"`
}

// Provenance identifies an answer and what produced it, for a pipeline that
// chains calls. It holds digests, ids and versions, and never the input.
type Provenance struct {
	// ResultID is this answer's id; a later stage names it as a parent.
	ResultID string `json:"result_id,omitempty"`

	// ParentResultIDs are the ids given with op.WithParents. Nil means the
	// answer has no upstream stage.
	ParentResultIDs []string `json:"parent_result_ids,omitempty"`

	// InputDigest is a digest of the input; never the input.
	InputDigest string `json:"input_digest,omitempty"`

	// SchemaDigest is the identity of the shape the answer was held to. Empty
	// when the operation has no schema identity.
	SchemaDigest string `json:"schema_digest,omitempty"`

	// OperationVersion is the operation and its version.
	OperationVersion string `json:"operation_version,omitempty"`

	// PromptVersion is the version of its prompt.
	PromptVersion string `json:"prompt_version,omitempty"`

	// ResolvedModel is the model that answered.
	ResolvedModel string `json:"resolved_model,omitempty"`

	// NormalizersApplied names the cleanup functions that ran on the decoded
	// value, by their Go symbol name; op runners only. Nil means none ran.
	NormalizersApplied []string `json:"normalizers_applied,omitempty"`

	// ItemRecoveryPath is how a repaired answer was arrived at: "first attempt"
	// when nothing needed fixing; op runners only. Empty otherwise.
	ItemRecoveryPath string `json:"item_recovery_path,omitempty"`

	// CacheUsed reports that the provider read part of the prompt from its
	// cache.
	CacheUsed bool `json:"cache_used,omitempty"`

	// LibraryVersion is the version of this library that made the answer.
	LibraryVersion string `json:"library_version,omitempty"`

	// AdapterVersion identifies the provider that answered.
	AdapterVersion string `json:"adapter_version,omitempty"`
}

// ContractLevel is how strong a guarantee an answer carries. The zero value is
// ContractPromptOnly, the weakest.
type ContractLevel uint8

const (
	// ContractPromptOnly: the model was asked; nothing was enforced.
	ContractPromptOnly ContractLevel = iota

	// ContractJSONWellFormed: the answer parsed.
	ContractJSONWellFormed

	// ContractSchemaConstrained: the answer fits the declared type.
	ContractSchemaConstrained

	// ContractSchemaAndInvariantChecked: and the operation's own rules hold: a
	// subset is a subset.
	ContractSchemaAndInvariantChecked

	// ContractEvidenceChecked: and its claims point into the source, and were
	// checked.
	ContractEvidenceChecked

	// ContractFullyGoverned: and provenance, capability and data policy were
	// applied.
	ContractFullyGoverned
)

// String returns the level's name in lower case: "schema constrained". A value
// out of range prints "unknown".
func (c ContractLevel) String() string {
	switch c {
	case ContractPromptOnly:
		return "prompt only"
	case ContractJSONWellFormed:
		return "json well-formed"
	case ContractSchemaConstrained:
		return "schema constrained"
	case ContractSchemaAndInvariantChecked:
		return "schema and invariants checked"
	case ContractEvidenceChecked:
		return "evidence checked"
	case ContractFullyGoverned:
		return "fully governed"
	}
	return "unknown"
}

// MarshalText returns the level's name, so that a level is written as its name
// by encoding/json, as an Error writes its kind.
func (c ContractLevel) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// contractFromInternal is the level for an internal level.
func contractFromInternal(c types.ContractLevel) ContractLevel {
	switch c {
	case types.ContractPromptOnly:
		return ContractPromptOnly
	case types.ContractJSONWellFormed:
		return ContractJSONWellFormed
	case types.ContractSchemaConstrained:
		return ContractSchemaConstrained
	case types.ContractSchemaAndInvariantChecked:
		return ContractSchemaAndInvariantChecked
	case types.ContractEvidenceChecked:
		return ContractEvidenceChecked
	case types.ContractFullyGoverned:
		return ContractFullyGoverned
	}
	return ContractLevel(c)
}

// timingToInternal is the internal timing for a Timing.
func timingToInternal(t Timing) types.Timing {
	return types.Timing{
		Calls:     t.Calls,
		Reused:    t.Reused,
		Queued:    t.Queued,
		Lookup:    t.Lookup,
		Connect:   t.Connect,
		Handshake: t.Handshake,
		Send:      t.Send,
		Wait:      t.Wait,
		Read:      t.Read,
		Network:   t.Network,
	}
}

// timingFromInternal is the Timing for an internal timing.
func timingFromInternal(t types.Timing) Timing {
	return Timing{
		Calls:     t.Calls,
		Reused:    t.Reused,
		Queued:    t.Queued,
		Lookup:    t.Lookup,
		Connect:   t.Connect,
		Handshake: t.Handshake,
		Send:      t.Send,
		Wait:      t.Wait,
		Read:      t.Read,
		Network:   t.Network,
	}
}

// costFromInternal is the Cost for an internal cost. The per-token prices, the
// billing period, the organisation and the project are not carried: a price per
// token is the price table's to state, and the other three are not a call's.
func costFromInternal(c types.CostInfo) Cost {
	// The currency is the one the call path states. A price that was found and
	// stated none is in the one currency the library prices in, "USD", so a priced
	// figure always says what it is in.
	currency := c.Currency
	if c.Priced && currency == "" {
		currency = "USD"
	}
	return Cost{
		Total:     c.TotalCost,
		Input:     c.PromptCost,
		Output:    c.CompletionCost,
		Cached:    c.CachedCost,
		Reasoning: c.ReasoningCost,
		Currency:  currency,
		Priced:    c.Priced,
		Estimated: c.Quality == types.PricingEstimated,
		Source:    c.PricingSource,
	}
}

// costToInternal is the internal cost for a Cost, the inverse of
// costFromInternal for the fields a Cost has: a Cost that is not Priced is no
// price found, and one that is Estimated says so.
func costToInternal(c Cost) types.CostInfo {
	info := types.CostInfo{
		TotalCost:      c.Total,
		PromptCost:     c.Input,
		CompletionCost: c.Output,
		CachedCost:     c.Cached,
		ReasoningCost:  c.Reasoning,
		Currency:       c.Currency,
		Priced:         c.Priced,
		PricingSource:  c.Source,
	}
	switch {
	case !c.Priced:
		info.Quality = types.PricingUnknown
	case c.Estimated:
		info.Quality = types.PricingEstimated
	default:
		info.Quality = types.PricingExact
	}
	return info
}

// checksFromInternal is the Checks for internal checks, nil for none.
func checksFromInternal(checks []types.Check) []Check {
	if len(checks) == 0 {
		return nil
	}
	converted := make([]Check, len(checks))
	for i, c := range checks {
		converted[i] = Check{Name: c.Name, Passed: c.Passed, Detail: c.Detail}
	}
	return converted
}

// provenanceFromInternal is the Provenance for an internal one, nil when the
// internal one is empty: the operation built none.
func provenanceFromInternal(p types.Provenance) *Provenance {
	if p.ResultID == "" && len(p.ParentResultIDs) == 0 && p.InputDigest == "" && p.SchemaDigest == "" &&
		p.OperationVersion == "" && p.PromptVersion == "" && p.ResolvedModel == "" &&
		len(p.NormalizersApplied) == 0 && p.ItemRecoveryPath == "" && !p.CacheUsed &&
		p.LibraryVersion == "" && p.AdapterVersion == "" {
		return nil
	}
	return &Provenance{
		ResultID:           p.ResultID,
		ParentResultIDs:    cloneStrings(p.ParentResultIDs),
		InputDigest:        p.InputDigest,
		SchemaDigest:       p.SchemaDigest,
		OperationVersion:   p.OperationVersion,
		PromptVersion:      p.PromptVersion,
		ResolvedModel:      p.ResolvedModel,
		NormalizersApplied: cloneStrings(p.NormalizersApplied),
		ItemRecoveryPath:   p.ItemRecoveryPath,
		CacheUsed:          p.CacheUsed,
		LibraryVersion:     p.LibraryVersion,
		AdapterVersion:     p.AdapterVersion,
	}
}

// askedFromInternal is the Asked for the questions and answers of a decision,
// nil for none. The options are copied.
func askedFromInternal(asked []ops.AnsweredQuestion) []Asked {
	if len(asked) == 0 {
		return nil
	}
	converted := make([]Asked, len(asked))
	for i, q := range asked {
		converted[i] = Asked{
			Question: q.Question,
			Options:  cloneStrings(q.Options),
			Low:      q.Low,
			High:     q.High,
			Answer:   q.Answer,
		}
	}
	return converted
}

// decisionFacts are who answered a decision and who was asked first, as the
// outcome of the call reports them.
type decisionFacts struct {
	answeredBy           string
	typed                bool
	askedFirst           string
	escalated            string
	askedFirstConfidence float64
	askedFirstReported   bool
}

// decisionFactsOf reads who answered a decision from its outcome. The outcome
// holds them only when the call made decisions and every one of them said the
// same (ops.Outcome.NoteDecision); a probability the decision provider asked
// first did not report is nil there and is not reported here, which is not a
// probability of zero.
func decisionFactsOf(o *ops.Outcome) decisionFacts {
	facts := decisionFacts{
		answeredBy: o.AnsweredBy,
		typed:      o.Typed,
		askedFirst: o.AskedFirst,
		escalated:  o.Escalated,
	}
	if o.AskedFirstModelConfidence != nil {
		facts.askedFirstConfidence = *o.AskedFirstModelConfidence
		facts.askedFirstReported = true
	}
	return facts
}

// apply writes the facts into a Record.
func (f decisionFacts) apply(rec *Record) {
	rec.AnsweredBy = f.answeredBy
	rec.Typed = f.typed
	rec.AskedFirst = f.askedFirst
	rec.Escalated = f.escalated
	rec.AskedFirstConfidence = f.askedFirstConfidence
	rec.AskedFirstReported = f.askedFirstReported
}

// recordFromOutcome makes the Record of one call from its outcome. It carries
// the questions and answers of a decision only when wantDecisions is set, and
// the refused body, cut to maxBody bytes, only when wantBodies is set: what a
// recorder did not ask for is not in the Record it is given.
//
// Requests is the attempts the call made less the answers a cache served, since
// the provider was not called for those. Every field of types.Meta is either
// read here or listed as not carried in the test of this function.
func recordFromOutcome(o *ops.Outcome, wantDecisions, wantBodies bool, maxBody int) Record {
	m := &o.Meta
	rec := Record{
		Op:             m.Operation,
		Start:          o.Start,
		Elapsed:        m.Elapsed,
		Provider:       m.Provider,
		Model:          m.Model,
		RequestedModel: m.RequestedModel,
		ServiceTier:    m.ServiceTier,
		RequestID:      m.RequestID,
		CorrelationID:  m.CorrelationID,
		SchemaID:       m.SchemaID,
		Requests:       max(0, m.Attempts-m.CacheHits),
		Retries:        m.Retries,
		Repairs:        m.Repairs,
		CacheHits:      m.CacheHits,
		Hedged:         m.Hedged,
		Tokens:         usageFromInternal(m.Usage),
		Cost:           costFromInternal(m.Cost),
		Timing:         timingFromInternal(m.Timing),
		Requested:      contractFromInternal(m.RequestedContract),
		Delivered:      contractFromInternal(m.DeliveredContract),
		Checks:         checksFromInternal(m.Checks),
		Strategy:       m.Strategy,
		Provenance:     provenanceFromInternal(m.Provenance),
	}
	if o.Err != nil {
		rec.Err = convertError(rec.Op, o.Err)
	}
	decisionFactsOf(o).apply(&rec)
	if wantDecisions {
		rec.Asked = askedFromInternal(o.Asked)
	}
	if wantBodies {
		rec.Body = bind.CutBody(o.Body, maxBody)
	}
	return rec
}

// sumRecords adds records up as Recorder.Total says.
func sumRecords(records []Record) Record {
	if len(records) == 0 {
		return Record{}
	}
	total := Record{
		Requested: records[0].Requested,
		Delivered: records[0].Delivered,
		Typed:     true,
	}
	// pricedAll and reusedAll are what every record that sent a request, and every
	// record that timed one, said; sent and timed are whether there was any.
	pricedAll, reusedAll, sent, timed := true, true, false, false
	var earliest, latest time.Time
	var unstamped time.Duration
	for _, rec := range records {
		if total.Err == nil {
			total.Err = rec.Err
		}
		if !rec.Start.IsZero() {
			if earliest.IsZero() || rec.Start.Before(earliest) {
				earliest = rec.Start
			}
			if finish := rec.Start.Add(rec.Elapsed); finish.After(latest) {
				latest = finish
			}
		} else {
			unstamped = max(unstamped, rec.Elapsed)
		}
		total.Requests = types.SumInt(total.Requests, rec.Requests)
		total.Retries = types.SumInt(total.Retries, rec.Retries)
		total.Repairs = types.SumInt(total.Repairs, rec.Repairs)
		total.CacheHits = types.SumInt(total.CacheHits, rec.CacheHits)
		total.Hedged = total.Hedged || rec.Hedged
		total.Requested = min(total.Requested, rec.Requested)
		total.Delivered = min(total.Delivered, rec.Delivered)
		total.Typed = total.Typed && rec.Typed

		total.Tokens.Input = types.SumInt(total.Tokens.Input, rec.Tokens.Input)
		total.Tokens.Output = types.SumInt(total.Tokens.Output, rec.Tokens.Output)
		total.Tokens.Total = types.SumInt(total.Tokens.Total, rec.Tokens.Total)
		total.Tokens.Cached = types.SumInt(total.Tokens.Cached, rec.Tokens.Cached)
		total.Tokens.CacheWrite = types.SumInt(total.Tokens.CacheWrite, rec.Tokens.CacheWrite)
		total.Tokens.Reasoning = types.SumInt(total.Tokens.Reasoning, rec.Tokens.Reasoning)

		total.Cost.Total += rec.Cost.Total
		total.Cost.Input += rec.Cost.Input
		total.Cost.Output += rec.Cost.Output
		total.Cost.Cached += rec.Cost.Cached
		total.Cost.Reasoning += rec.Cost.Reasoning
		if rec.Requests > 0 {
			sent = true
			pricedAll = pricedAll && rec.Cost.Priced
		}
		total.Cost.Estimated = total.Cost.Estimated || rec.Cost.Estimated

		total.Timing.Calls = types.SumInt(total.Timing.Calls, rec.Timing.Calls)
		if rec.Timing.Calls > 0 {
			timed = true
			reusedAll = reusedAll && rec.Timing.Reused
		}
		total.Timing.Queued = types.SumDuration(total.Timing.Queued, rec.Timing.Queued)
		total.Timing.Lookup = types.SumDuration(total.Timing.Lookup, rec.Timing.Lookup)
		total.Timing.Connect = types.SumDuration(total.Timing.Connect, rec.Timing.Connect)
		total.Timing.Handshake = types.SumDuration(total.Timing.Handshake, rec.Timing.Handshake)
		total.Timing.Send = types.SumDuration(total.Timing.Send, rec.Timing.Send)
		total.Timing.Wait = types.SumDuration(total.Timing.Wait, rec.Timing.Wait)
		total.Timing.Read = types.SumDuration(total.Timing.Read, rec.Timing.Read)
		total.Timing.Network = types.SumDuration(total.Timing.Network, rec.Timing.Network)
	}
	total.Cost.Priced = sent && pricedAll
	total.Timing.Reused = timed && reusedAll
	total.Start = earliest
	total.Elapsed = max(latest.Sub(earliest), unstamped)

	total.Op = agreed(records, func(r Record) string { return r.Op })
	total.Provider = agreed(records, func(r Record) string { return r.Provider })
	total.Model = agreed(records, func(r Record) string { return r.Model })
	total.RequestedModel = agreed(records, func(r Record) string { return r.RequestedModel })
	total.ServiceTier = agreed(records, func(r Record) string { return r.ServiceTier })
	total.RequestID = agreed(records, func(r Record) string { return r.RequestID })
	total.CorrelationID = agreed(records, func(r Record) string { return r.CorrelationID })
	total.SchemaID = agreed(records, func(r Record) string { return r.SchemaID })
	total.Strategy = agreed(records, func(r Record) string { return r.Strategy })
	total.AnsweredBy = agreed(records, func(r Record) string { return r.AnsweredBy })
	total.AskedFirst = agreed(records, func(r Record) string { return r.AskedFirst })
	total.Escalated = agreed(records, func(r Record) string { return r.Escalated })
	total.Cost.Currency = agreed(records, func(r Record) string { return r.Cost.Currency })
	total.Cost.Source = agreed(records, func(r Record) string { return r.Cost.Source })
	return total
}

// agreed returns the value field gives for every record when it is the same for
// all of them, and the empty string otherwise.
func agreed(records []Record, field func(Record) string) string {
	first := field(records[0])
	for _, rec := range records[1:] {
		if field(rec) != first {
			return ""
		}
	}
	return first
}
