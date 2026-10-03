package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// Every result reports the contract level its operation requested and the one
// it delivered (types.Meta's RequestedContract and DeliveredContract).
// RunOpResult calls declaredContractLevel to fill both.
//
// negotiatedContractLevel lowers a level a provider route cannot back: a
// provider falling back from native json_schema enforcement to a prompt-only
// mechanism would otherwise be invisible. It reads internal/llm's capabilities
// registry, which says what a resolved provider and model route is declared to
// support. A failed call still reports DeliveredContract as ContractPromptOnly:
// nothing was delivered, so nothing stronger can be claimed. Degraded()
// (result.go) reads against that.
//
// enforceContractPolicy is the other half: types.DataPolicy carries a
// MinimumContract, the floor a route may not deliver below, and a call whose
// negotiated DeliveredContract falls below it is refused with
// ErrContractDegraded instead of being handed back as a quieter answer.
// WithContractPolicy attaches one to a context.

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
//     ContractEvidenceChecked, reached only from a declared schema, since the
//     levels are cumulative and an evidence check on an answer that is not even
//     held to a schema has not established what the lower levels say.
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
	if evidencePolicy != types.EvidenceNone && level >= types.ContractSchemaConstrained {
		level = types.ContractEvidenceChecked
	}

	return level
}

// batchContractLevel is the level a batch of this operation establishes.
//
// It is what the batch path checks, and no more. A batch built on the id
// protocol (NewIDBatchAlgebra) reads the answer as JSON, requires every id
// once and decodes each output, and RunOpBatch holds every output to the
// invariants, so it stands for what the operation's contract declares short
// of the evidence check: that check is made between one input and the claims
// of its one output, which a batch's merge does not pair up.
//
// A batch with a merge of its own is vouched for no further than that the
// provider answered. The level used to be read off the contract, a schema's
// name among it, whatever the merge did: a merge that split the body on
// commas was delivered as schema constrained and invariant checked.
func batchContractLevel[In, Out any](op Op[In, Out]) types.ContractLevel {
	if !op.Batch.checksEachItem {
		return types.ContractPromptOnly
	}
	contract := op.Contract
	contract.EvidencePolicy = types.EvidenceNone
	contract.EvidenceRequired = false
	return declaredContractLevel(contract)
}

// plannedRoute is the provider and model a call is about to use, resolved the
// way callLLM resolves them. known is false when no provider is set, or the
// provider has no model for the tier: the call will fail on its own account,
// and there is no route to ask anything of.
func plannedRoute(ctx context.Context, opt types.OpOptions) (provider, model string, known bool) {
	chosen, _, err := routeFor(ctx)
	if err != nil || chosen == nil {
		return "", "", false
	}
	model = resolveModel(ctx, opt, chosen)
	return chosen.Name(), model, model != ""
}

// plannedContractLevel is the level a call can be delivered at on the route it
// is about to take: the declared level, lowered as negotiatedContractLevel
// lowers it when the route is known. With no route known it is the declared
// level, and the check after the call still stands.
func plannedContractLevel(ctx context.Context, declared types.ContractLevel, opt types.OpOptions) types.ContractLevel {
	if provider, model, known := plannedRoute(ctx, opt); known {
		return negotiatedContractLevel(declared, opt, provider, model)
	}
	return declared
}

// cappedByLineage enforces the rule that "where lineage breaks, the
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

// negotiatedContractLevel demotes level to what was actually asked of the
// resolved provider/model route, and to what that route is known to support.
//
// The level an operation declares is what it would earn if the answer were held
// to a schema, and a schema is held to only when one is sent. opt.JSONSchema --
// not op.Contract.SchemaName -- is the signal that one was: llm_helper.go's
// CallLLM copies opt.JSONSchema verbatim into llm.CompletionRequest.JSONSchema,
// which is what OpenAIProvider.Complete sends as a strict json_schema format
// (see buildResponsesRequestBody); an empty opt.JSONSchema sends json_object at
// best, i.e. exactly the "prompt-only" mechanism this function's caller is
// asking to detect. SchemaName alone is not that signal: it is the Op's static
// declaration, present for a caller who never populated opt.JSONSchema.
//
// So a level of schema constrained or above is lowered in two cases. With no
// schema sent, nothing enforced one, on any route: the delivered level is the
// declared one lowered to well formed. That was passed through as it was, so a
// descriptor that named a schema it never sent was delivered as schema
// constrained, and Degraded() could not tell it from one that sent it. With a
// schema sent, CapabilitiesFor(provider, model) is the observation:
// capabilities.go already tracks, per route, whether CapNativeJSONSchema is
// something that route is known to honor. "Never fail open" (AGENTS.md) governs
// both ways a route can fail this check -- explicitly declared NOT to support
// it, or never declared at all. A route this registry has never heard of is not
// assumed to have enforced the schema just because nothing here can prove it did
// not; the same rule llm.Meets and llm.Negotiate already apply to every other
// capability check in this codebase. Only a route explicitly registered WITH
// CapNativeJSONSchema keeps the level the Op declared.
//
// Demoted all the way to ContractJSONWellFormed, not one step down: without
// a provider-side enforcement mechanism, everything above "the answer
// parsed" -- that it fits the declared shape, that invariants hold, that
// evidence resolves -- was established solely by this library's own
// deterministic post-validation on whatever came back, which is real but is
// not what SchemaConstrained-and-above claims when a schema was supposed to
// be enforced going in.
func negotiatedContractLevel(level types.ContractLevel, opt types.OpOptions, provider, model string) types.ContractLevel {
	if level < types.ContractSchemaConstrained {
		return level
	}
	if len(opt.JSONSchema) == 0 {
		return types.ContractJSONWellFormed
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
// types.DataPolicy.MinimumContract is "the lowest ContractLevel an answer
// executed under this policy may be delivered at". This is where it is read
// against an actual call, following the same
// context-carried-attachment shape withCallRecording already uses in this
// package for the same reason: threading a new value through every
// operation's return type would be the T-01 mistake (two shapes that drift)
// one field over, and RunOpResult already accepts a context every caller
// controls.
//
// Nothing in this repository calls it outside tests. It is for callers of the
// library, and a client's own data policy carries the same floor.
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
	// A client's own data policy carries the same floor. It was read
	// by nothing, so a client that set MinimumContract was delivered whatever
	// the route negotiated.
	//
	// With a policy on the call and one on the client, the floor is the
	// stricter of the two. A policy on the call cannot loosen what the client
	// requires, which is the rule types.DataPolicy.Tighten states for every
	// field: the result is never less restrictive than either side.
	attached, onCall := ctx.Value(contractPolicyKey{}).(types.DataPolicy)
	clients, onClient := ExecDataPolicy(ctx)
	switch {
	case onCall && onClient:
		if clients.MinimumContract > attached.MinimumContract {
			attached.MinimumContract = clients.MinimumContract
		}
		return attached, true
	case onCall:
		return attached, true
	default:
		return clients, onClient
	}
}

// ErrContractDegraded reports a call whose DeliveredContract fell below a
// policy's MinimumContract. It is the disallowed degradation, refused
// outright: a caller who attached a floor via WithContractPolicy
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
		// Classified, so a caller outside this module can tell it from any
		// other failure: ErrContractDegraded is not exported at the root, and
		// a policy's floor not being met is a policy violation.
		return &types.OperationError{
			Kind:    types.KindPolicyViolation,
			Op:      "contract policy",
			Message: fmt.Sprintf("delivered %s, policy requires at least %s", delivered, policy.MinimumContract),
			Cause:   ErrContractDegraded,
		}
	}
	return nil
}

// checkContractFloor refuses a call, before it is made, whose answer cannot
// reach the contract floor of the caller's policy: the most this call can
// establish is below what the policy requires.
//
// enforceContractPolicy asks the same question afterwards, of what was
// actually delivered, and both are needed. This one is why a floor a call can
// never meet costs nothing: the six Result forms that establish a fixed level
// used to be checked only after the provider had answered and been paid, and
// against a level nobody had set, so any floor at all refused them.
func checkContractFloor(ctx context.Context, establishes types.ContractLevel) error {
	policy, ok := contractPolicyFrom(ctx)
	if !ok || establishes >= policy.MinimumContract {
		return nil
	}
	return &types.OperationError{
		Kind:    types.KindPolicyViolation,
		Op:      "contract policy",
		Message: fmt.Sprintf("this call can establish at most %s, policy requires at least %s; nothing was sent", establishes, policy.MinimumContract),
		Cause:   ErrContractDegraded,
	}
}

// CheckContractFloor is checkContractFloor for the fluent builders, whose
// RunResult forms build their envelope outside this package.
func CheckContractFloor(ctx context.Context, establishes types.ContractLevel) error {
	return checkContractFloor(ctx, establishes)
}

// The contract level each Result form establishes when it succeeds.
//
// A text operation's answer is parsed as JSON and its text field read, which
// is the answer being well formed and nothing more.
const textResultContract = types.ContractJSONWellFormed
