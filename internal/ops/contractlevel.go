package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-004. types.ContractLevel and types.Meta's
// RequestedContract/DeliveredContract already existed (result.go) but
// nothing in this package ever set them -- every Result[T] reported
// ContractPromptOnly regardless of what the operation actually enforced,
// which is the zero value doing double duty as "asked for nothing" and
// "nobody said". RunOpResult (op.go) now calls declaredContractLevel to fill
// both fields.
//
// The half that used to be missing -- a provider falling back from native
// json_schema enforcement to a prompt-only mechanism mid-call, invisibly --
// is what negotiatedContractLevel below closes, using
// internal/llm/capabilities.go's registry as the observation source: what a
// resolved provider/model route is actually declared to support, not a
// number this package invents. A failed call still reports DeliveredContract
// as ContractPromptOnly: nothing was actually delivered, so nothing stronger
// can be claimed. Read Degraded() (result.go) against that: a caller
// checking it today learns "this call failed" for a hard failure, and now
// also learns about a silent mechanism downgrade on a call that otherwise
// looked like a full success.
//
// enforceContractPolicy is the other half TC-004 asks for: "any degradation
// requires policy approval rather than a log line." types.DataPolicy already
// carries MinimumContract (CP-002, internal/types/policy.go) -- the floor a
// route may not deliver below -- but nothing before this read it against an
// actual RunOpResult call. WithContractPolicy attaches one to a context; a
// call whose negotiated DeliveredContract falls below it is refused with
// ErrContractDegraded instead of being handed back as a quieter answer.

// declaredContractLevel reports the strongest guarantee an Op's own
// declaration supports, from what OutputContract actually carries:
//
//   - a schema name declared: at least ContractSchemaConstrained: RunOp
//     already refuses to decode without a schema-shaped target (opextract.go
//     generates one for every T), so a named schema is only absent when an
//     Op chose not to declare one, which this reads as "no stronger
//     guarantee than the answer parsing" -- ContractJSONWellFormed.
//   - at least one Invariants entry, all of which RunOp requires to pass
//     before a candidate is accepted: ContractSchemaAndInvariantChecked.
//   - an evidence policy stronger than types.EvidenceNone, which RunOp
//     enforces via StrictEvidence before a candidate is accepted:
//     ContractEvidenceChecked.
//
// ContractFullyGoverned is never returned here: it additionally requires
// capability negotiation and data-policy enforcement (CP-001, CP-002), which
// this Op-level path does not perform.
func declaredContractLevel[Out any](contract OutputContract[Out]) types.ContractLevel {
	level := types.ContractJSONWellFormed
	if contract.SchemaName != "" {
		level = types.ContractSchemaConstrained
	}
	if len(contract.Invariants) > 0 && level >= types.ContractSchemaConstrained {
		level = types.ContractSchemaAndInvariantChecked
	}

	evidencePolicy := contract.EvidencePolicy
	if evidencePolicy == types.EvidenceNone && contract.EvidenceRequired {
		evidencePolicy = types.EvidenceAllModelDerived
	}
	if evidencePolicy != types.EvidenceNone {
		level = types.ContractEvidenceChecked
	}

	return level
}

// cappedByLineage enforces TC-003's last clause: "where lineage breaks, the
// delivered contract cannot be FullyGoverned." A result whose provenance does
// not resolve -- no result ID, no input digest, no operation version, no
// resolved model -- is demoted to ContractEvidenceChecked rather than being
// allowed to advertise the strongest guarantee this library defines, because a
// claim that cannot be traced back to what produced it is precisely what
// FullyGoverned is meant to rule out.
//
// It exists as a named function, and is tested directly, for an honest reason:
// declaredContractLevel never returns ContractFullyGoverned today (that needs
// CP-001's capability negotiation and CP-002's data-policy enforcement), so
// wired into RunOpResult this cannot currently fire. An `if` in the middle of
// RunOpResult that no input reaches is untestable and would rot silently --
// exactly what types.Provenance.FullyTraced already was, a predicate declared
// and read by nothing. This way the rule is written down once, checked by a
// test that can construct the case, and already in the path CP-001 will make
// reachable rather than being a step somebody has to remember to add later.
func cappedByLineage(level types.ContractLevel, prov types.Provenance) types.ContractLevel {
	if level >= types.ContractFullyGoverned && !prov.FullyTraced() {
		return types.ContractEvidenceChecked
	}
	return level
}

// negotiatedContractLevel demotes level to what the resolved provider/model
// route is actually known to support, when the call asked that route to
// enforce a schema natively.
//
// opt.JSONSchema -- not op.Contract.SchemaName -- is the signal a schema was
// actually requested of the provider: llm_helper.go's CallLLM copies
// opt.JSONSchema verbatim into llm.CompletionRequest.JSONSchema, which is
// what OpenAIProvider.Complete sends as a strict json_schema format (see
// buildResponsesRequestBody); an empty opt.JSONSchema sends json_object at
// best, i.e. exactly the "prompt-only" mechanism this function's caller is
// asking to detect. SchemaName alone is not that signal -- it is the Op's
// static declaration, present even for a caller who never populated
// opt.JSONSchema, and gating on it would demote every such call as if it had
// requested and lost native enforcement it never asked for in the first
// place.
//
// Given that a schema was requested, CapabilitiesFor(provider, model) is the
// observation: capabilities.go already tracks, per route, whether
// CapNativeJSONSchema is something that route is known to honor.
// "Never fail open" (AGENTS.md) governs both ways a route can fail this
// check -- explicitly declared NOT to support it, or never declared at all.
// A route this registry has never heard of is not assumed to have enforced
// the schema just because nothing here can prove it did not; the same rule
// llm.Meets and llm.Negotiate already apply to every other capability check
// in this codebase. Only a route explicitly registered WITH
// CapNativeJSONSchema keeps the level the Op declared.
//
// Demoted all the way to ContractJSONWellFormed, not one step down: without
// a provider-side enforcement mechanism, everything above "the answer
// parsed" -- that it fits the declared shape, that invariants hold, that
// evidence resolves -- was established solely by this library's own
// deterministic post-validation on whatever came back, which is real but is
// not what SchemaConstrained-and-above claims when a schema was supposed to
// be enforced going in.
//
// This is dormant against every call site in this repository today: nothing
// that reaches RunOpResult currently populates opt.JSONSchema (Extract's own
// contract envelope, extractresult.go, computes its delivered level a
// different way and never calls RunOp/RunOpResult at all). It is still
// written as a named, directly tested function rather than left until a
// caller exists to exercise it, for the reason cappedByLineage's doc comment
// already gives for the same situation: an unreachable branch inline in
// RunOpResult cannot be tested and rots silently, and CP-001 wiring
// opt.JSONSchema into the Op[In, Out] path is what makes this reachable, not
// what makes it correct.
func negotiatedContractLevel(level types.ContractLevel, opt types.OpOptions, provider, model string) types.ContractLevel {
	if level < types.ContractSchemaConstrained || len(opt.JSONSchema) == 0 {
		return level
	}
	caps, known := llm.CapabilitiesFor(provider, model)
	if !known || !caps.Has(llm.CapNativeJSONSchema) {
		return types.ContractJSONWellFormed
	}
	return level
}

// contractPolicyKey is the context key WithContractPolicy/contractPolicyFrom
// use, unexported so nothing outside this package can collide with it or
// set the value through any path other than WithContractPolicy.
type contractPolicyKey struct{}

// WithContractPolicy attaches policy to ctx so a subsequent RunOpResult call
// (using a context derived from the one returned here) refuses to complete
// when its negotiated DeliveredContract falls below policy.MinimumContract.
//
// types.DataPolicy.MinimumContract already existed (CP-002,
// internal/types/policy.go) as "the lowest ContractLevel an answer executed
// under this policy may be delivered at" -- a real field with nothing that
// read it against an actual call. This is that read, following the same
// context-carried-attachment shape withCallRecording already uses in this
// package for the same reason: threading a new value through every
// operation's return type would be the T-01 mistake (two shapes that drift)
// one field over, and RunOpResult already accepts a context every caller
// controls.
func WithContractPolicy(ctx context.Context, policy types.DataPolicy) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contractPolicyKey{}, policy)
}

// contractPolicyFrom reads back what WithContractPolicy attached. The second
// return value distinguishes "no policy was ever attached" from "a policy
// was attached and its MinimumContract happens to be the zero value,
// ContractPromptOnly" -- the latter is a real, if permissive, policy and
// must not be treated as absent.
func contractPolicyFrom(ctx context.Context) (types.DataPolicy, bool) {
	if ctx == nil {
		return types.DataPolicy{}, false
	}
	policy, ok := ctx.Value(contractPolicyKey{}).(types.DataPolicy)
	return policy, ok
}

// ErrContractDegraded reports a call whose DeliveredContract fell below a
// policy's MinimumContract. It is the "disallowed degradation" TC-004 asks
// to refuse outright: a caller who attached a floor via WithContractPolicy
// gets an error instead of a Result[T] that quietly delivered less than the
// policy permits.
var ErrContractDegraded = errors.New("delivered contract level is below the policy's minimum")

// enforceContractPolicy refuses delivered when ctx carries a types.DataPolicy
// (via WithContractPolicy) whose MinimumContract it does not meet. A call
// with no attached policy -- every call site in this repository today, since
// nothing yet calls WithContractPolicy in production code -- is unaffected:
// a caller who never declared a floor has not disallowed anything, which is
// the correct reading of "no policy" and not the same as "policy of
// MinimumContract: PromptOnly" (contractPolicyFrom's second return value is
// what keeps those two apart).
func enforceContractPolicy(ctx context.Context, delivered types.ContractLevel) error {
	policy, ok := contractPolicyFrom(ctx)
	if !ok {
		return nil
	}
	if delivered < policy.MinimumContract {
		return fmt.Errorf("%w: delivered %s, policy requires at least %s", ErrContractDegraded, delivered, policy.MinimumContract)
	}
	return nil
}
