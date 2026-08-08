package ops

import "github.com/monstercameron/schemaflux/internal/types"

// TC-004, partial. types.ContractLevel and types.Meta's
// RequestedContract/DeliveredContract already existed (result.go) but
// nothing in this package ever set them -- every Result[T] reported
// ContractPromptOnly regardless of what the operation actually enforced,
// which is the zero value doing double duty as "asked for nothing" and
// "nobody said". RunOpResult (op.go) now calls declaredContractLevel to fill
// both fields.
//
// What this does not do: track a provider falling back from native
// json_schema enforcement to a prompt-only mechanism mid-call. That signal
// lives in internal/llm and opextract.go's decode path, neither of which
// this task has permission to touch, so RequestedContract and
// DeliveredContract are equal for every successful call -- both reflect what
// the Op is declared to enforce, not a per-call negotiation. A failed call
// reports DeliveredContract as ContractPromptOnly: nothing was actually
// delivered, so nothing stronger can be claimed. Read Degraded() (result.go)
// against that: a caller checking it today learns "this call failed" for a
// hard failure, and learns nothing about a silent mechanism downgrade, which
// is the piece a future task closes by threading the provider's actual
// mechanism back through RunOp.

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
