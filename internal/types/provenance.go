package types

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// TC-003. Provenance is how a result traces back to what produced it -- not
// the input, never the input. types.DescribeValue exists for describing an
// input without reproducing it, and this file follows the identical rule for
// everything it carries: a digest, an ID, a version string identify without
// disclosing.
//
// TC-002's EvidenceRef already makes the same trade for one claim's source
// span (offsets and a digest, never the quoted text); Provenance is the same
// idea over a whole result, and over a pipeline of results, each one
// resolving back to the one before it through ParentResultIDs.

// Provenance is the record of what produced one result: its own identity,
// what (if anything) it was derived from, and what the runtime measured
// about how it got there.
type Provenance struct {
	// ResultID identifies this result. Opaque and unique per call --
	// NewResultID generates it -- so a later stage, a log line, or a stored
	// record can name this exact answer without repeating any of it.
	ResultID string

	// ParentResultIDs names the results this one was derived from, in a
	// pipeline where one stage's output feeds the next stage's input. Empty
	// for a result with no upstream stage. A caller threads this by setting
	// OpOptions.ParentResultIDs to the previous stage's
	// Result.Meta.Provenance.ResultID before making the next call --
	// nothing does this automatically, because only the caller knows which
	// results are actually related.
	ParentResultIDs []string

	// InputDigest is a digest of the value the operation was given, never
	// the value itself -- the same discipline DigestSource (evidence.go)
	// applies to a claim's source text, generalized to an arbitrary Go
	// value via DigestValue.
	InputDigest string

	// SchemaDigest is the resolved schema's identity string (name, version,
	// hash, dialect -- see ops.SchemaDescriptor.String()), when the
	// operation computed one. Empty for an operation with no schema
	// identity.
	SchemaDigest string

	// OperationVersion is the operation's own identity string
	// (OperationID.String(): "name@version"), so a stored result says
	// exactly which contract produced it, not just which operation ran.
	OperationVersion string

	// PromptVersion is the operation's contract version in isolation --
	// OperationID.Version alone. In this codebase the operation version and
	// the prompt version are the same value today (OperationID.Version's
	// own doc comment names extractPromptVersion as the existing instance
	// of this idea); the field is kept separate because the two are
	// conceptually distinct facts that happen to share a source now, and an
	// operation whose prompt can change independently of its declared
	// contract version needs somewhere to say so without a breaking change
	// to this struct.
	PromptVersion string

	// ResolvedModel is the model that actually answered, which can differ
	// from what a tier mapping would suggest when a provider substitutes,
	// or reports a different revision string back than what was requested.
	ResolvedModel string

	// NormalizersApplied names the shape-preserving cleanup functions that
	// ran on the decoded value, by their Go symbol name -- never their
	// output, which could be the caller's data reshaped. Empty when the
	// operation declared no normalization step.
	NormalizersApplied []string

	// ItemRecoveryPath describes how the result reached an accepted state:
	// "first attempt" when nothing needed fixing, or a description of the
	// repair path otherwise. It carries no repair content -- the model's
	// rejected attempts are exactly the kind of payload AGENTS.md forbids
	// logging or embedding in a result.
	ItemRecoveryPath string

	// CacheUsed reports whether the provider served this result from its
	// own prompt cache, measured from CachedTokens > 0 (the same
	// observation types.Meta.CacheHitRatio is built from), never inferred
	// from timing or assumed.
	CacheUsed bool

	// LibraryVersion and AdapterVersion identify what code produced this
	// result. LibraryVersion is this library's own build-time identity
	// (Version, below); AdapterVersion is the provider name the response
	// reported, which is what this codebase has to identify an adapter
	// with -- there is no separate, finer adapter-version string anywhere
	// in llm.Provider or llm.CompletionResponse to read instead.
	LibraryVersion string
	AdapterVersion string
}

// FullyTraced reports whether a result's lineage is complete enough to
// support ContractFullyGoverned (result.go): every link back to source
// actually has to be present, not merely a field that exists on the struct.
// TC-003's verify line -- "where lineage breaks, the delivered contract
// cannot be FullyGoverned" -- is enforced by a caller checking this before
// treating DeliveredContract as ContractFullyGoverned; Provenance and
// ContractLevel are computed independently in this codebase, and this is
// where the two are meant to be read together.
func (p Provenance) FullyTraced() bool {
	return p.ResultID != "" &&
		p.InputDigest != "" &&
		p.OperationVersion != "" &&
		p.ResolvedModel != ""
}

// Version is this library's own version string, for
// Provenance.LibraryVersion. A literal rather than a debug.ReadBuildInfo
// read: building this module from its own source tree (the normal case for
// `go build`/`go test` without -ldflags) leaves Main.Version at "(devel)",
// which is meaningless as provenance -- this constant is at least a
// deliberate, single place to bump when it stops being one.
const Version = "v1.0.0"

// NewResultID generates an opaque, unique identifier for one result.
// Sixteen random bytes, hex-encoded: enough that two results colliding is
// not a thing that happens, and it carries no information about the result
// it names -- unlike, say, a hash of the output, which would still be
// reproducing the output's shape one layer down.
func NewResultID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing means the platform itself is broken. A silent
		// zero-value ID would collide with every other zero-value ID this
		// call ever produces; naming the failure in the ID itself is worse
		// for readability but does not pretend nothing happened.
		return "unavailable-" + err.Error()
	}
	return hex.EncodeToString(buf[:])
}

// DigestValue computes a content digest of an arbitrary Go value without
// retaining the value -- the same trade DigestSource (evidence.go) makes for
// source text, generalized to whatever an operation's In type happens to be.
//
// A string is hashed directly, the common case for this library's inputs.
// Anything else is marshalled to JSON first -- a stable, order-independent
// (encoding/json sorts map keys) representation for the struct and map
// shapes an In type actually is -- and a value that cannot be marshalled
// falls back to a Go-syntax representation (fmt's %#v), which is still
// deterministic for a given value. Neither the JSON nor the %#v text is ever
// returned; only its hash is, through DigestSource.
func DigestValue(value any) string {
	if value == nil {
		return DigestSource("")
	}
	if text, ok := value.(string); ok {
		return DigestSource(text)
	}
	if data, err := json.Marshal(value); err == nil {
		return DigestSource(string(data))
	}
	return DigestSource(fmt.Sprintf("%#v", value))
}
