package schemaflux

import (
	"encoding/json"
	"time"
)

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
