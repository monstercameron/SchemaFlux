package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// TC-003. Provenance through pipelines: result IDs, parent links, input and
// schema digests, operation and prompt versions, resolved model, normalizers
// applied, item recovery path, cache usage, and library and adapter
// versions. Provenance carries no caller payload -- these tests feed a
// marker string through and assert its absence, the same discipline
// TC-002's evidence tests hold EvidenceRef to.

// 1. DigestValue is deterministic: the same value always digests the same.
func TestDigestValueIsDeterministic(t *testing.T) {
	a := types.DigestValue("the exact same input")
	b := types.DigestValue("the exact same input")
	if a != b {
		t.Fatalf("DigestValue() is not deterministic: %q != %q", a, b)
	}
}

// 2. DigestValue distinguishes different values -- otherwise it could not
// serve as an identity at all.
func TestDigestValueDistinguishesDifferentInputs(t *testing.T) {
	a := types.DigestValue("input one")
	b := types.DigestValue("input two")
	if a == b {
		t.Fatalf("DigestValue() collided for two different inputs: %q", a)
	}
}

// 3. The payload rule, checked directly against DigestValue: a marker string
// fed in must never appear in the digest returned.
func TestDigestValueNeverContainsThePayload(t *testing.T) {
	const marker = "SSN-000-00-0000-do-not-leak-this"
	digest := types.DigestValue(marker)
	if strings.Contains(digest, marker) {
		t.Fatalf("DigestValue() leaked the payload: %q", digest)
	}
}

// 4. The same rule for a struct input, marshalled through the JSON path
// rather than the direct-string path.
func TestDigestValueNeverContainsThePayloadForAStruct(t *testing.T) {
	const marker = "confidential-field-value-42"
	type record struct{ Secret string }
	digest := types.DigestValue(record{Secret: marker})
	if strings.Contains(digest, marker) {
		t.Fatalf("DigestValue() leaked a struct field: %q", digest)
	}
}

// 5. NewResultID produces unique, non-empty identifiers.
func TestNewResultIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := types.NewResultID()
		if id == "" {
			t.Fatal("NewResultID() returned empty string")
		}
		if seen[id] {
			t.Fatalf("NewResultID() produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}

// 6. FullyTraced reports false when a required link is missing, true only
// when every required field is present -- so a caller checking
// ContractFullyGoverned eligibility cannot be fooled by a partially filled
// struct.
func TestProvenanceFullyTraced(t *testing.T) {
	cases := []struct {
		name string
		prov types.Provenance
		want bool
	}{
		{"zero value", types.Provenance{}, false},
		{"only ResultID", types.Provenance{ResultID: "r1"}, false},
		{"missing ResolvedModel", types.Provenance{
			ResultID: "r1", InputDigest: "d1", OperationVersion: "op@v1",
		}, false},
		{"every required field present", types.Provenance{
			ResultID: "r1", InputDigest: "d1", OperationVersion: "op@v1", ResolvedModel: "fake-model",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.prov.FullyTraced(); got != tc.want {
				t.Errorf("FullyTraced() = %v, want %v", got, tc.want)
			}
		})
	}
}

// 7. RunOpResult fills in a Provenance for an ordinary, single-stage call:
// a fresh ResultID, an InputDigest matching the actual input, the
// operation's own identity string, the resolved model from the envelope,
// and "first attempt" recovery -- exercised through the real provider path
// (scriptedProvider via WithProvider) so Model is genuinely populated, the
// same reason op_kernel_seam_test.go's envelope tests avoid setLLMCaller.
func TestRunOpResultPopulatesProvenanceOnFirstAttempt(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"final"}}
	ctx := WithProvider(context.Background(), provider)

	op := echoOp(func(value string) error {
		if value != "final" {
			return errFor("not final")
		}
		return nil
	})
	op.Contract.SchemaName = "echo"

	result, err := RunOpResult(ctx, op, "the-input", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}

	prov := result.Meta.Provenance
	if prov.ResultID == "" {
		t.Error("ResultID is empty")
	}
	if want := types.DigestValue("the-input"); prov.InputDigest != want {
		t.Errorf("InputDigest = %q, want %q", prov.InputDigest, want)
	}
	if prov.OperationVersion != op.ID.String() {
		t.Errorf("OperationVersion = %q, want %q", prov.OperationVersion, op.ID.String())
	}
	if prov.PromptVersion != op.ID.Version {
		t.Errorf("PromptVersion = %q, want %q", prov.PromptVersion, op.ID.Version)
	}
	if prov.ResolvedModel != "fake-model" {
		t.Errorf("ResolvedModel = %q, want %q", prov.ResolvedModel, "fake-model")
	}
	if prov.ItemRecoveryPath != "first attempt" {
		t.Errorf("ItemRecoveryPath = %q, want %q", prov.ItemRecoveryPath, "first attempt")
	}
	if prov.LibraryVersion != types.Version {
		t.Errorf("LibraryVersion = %q, want %q", prov.LibraryVersion, types.Version)
	}
	if prov.AdapterVersion != "scripted" {
		t.Errorf("AdapterVersion = %q, want %q (the provider name)", prov.AdapterVersion, "scripted")
	}
	if len(prov.ParentResultIDs) != 0 {
		t.Errorf("ParentResultIDs = %v, want empty for a call with no declared parent", prov.ParentResultIDs)
	}
}

// 8. A repaired answer's recovery path says so, distinctly from the
// first-attempt case -- and still carries no content from the rejected
// first attempt.
func TestRunOpResultProvenanceRecordsRepairPath(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"draft", "final"}}
	ctx := WithProvider(context.Background(), provider)

	op := echoOp(func(value string) error {
		if value != "final" {
			return errFor("not final yet")
		}
		return nil
	})

	result, err := RunOpResult(ctx, op, "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Meta.Provenance.ItemRecoveryPath != "repaired, 2 attempt(s)" {
		t.Errorf("ItemRecoveryPath = %q, want %q", result.Meta.Provenance.ItemRecoveryPath, "repaired, 2 attempt(s)")
	}
	// The rejected first attempt's content ("draft") never appears in the
	// recovery path description.
	if strings.Contains(result.Meta.Provenance.ItemRecoveryPath, "draft") {
		t.Errorf("ItemRecoveryPath leaked the rejected attempt's content: %q", result.Meta.Provenance.ItemRecoveryPath)
	}
}

// 9. A three-stage pipeline: each stage's Provenance.ResultID becomes the
// next stage's declared parent via OpOptions.ParentResultIDs, so the final
// stage's Provenance names the immediately prior stage, which in turn named
// the one before it -- the chain a caller (or an audit) walks back through.
func TestThreeStagePipelineProvenanceChainsThroughParentResultIDs(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	op := echoOp() // no invariants: every answer is accepted on the first try

	// Stage 1: no parent.
	provider1 := &scriptedProvider{bodies: []string{"stage-1-output"}}
	stage1, err := RunOpResult(WithProvider(context.Background(), provider1), op, "raw-input", types.OpOptions{})
	if err != nil {
		t.Fatalf("stage 1: %v", err)
	}
	if len(stage1.Meta.Provenance.ParentResultIDs) != 0 {
		t.Fatalf("stage 1 ParentResultIDs = %v, want empty", stage1.Meta.Provenance.ParentResultIDs)
	}

	// Stage 2: declares stage 1 as its parent.
	provider2 := &scriptedProvider{bodies: []string{"stage-2-output"}}
	stage2, err := RunOpResult(WithProvider(context.Background(), provider2), op, "stage-1-output", types.OpOptions{
		ParentResultIDs: []string{stage1.Meta.Provenance.ResultID},
	})
	if err != nil {
		t.Fatalf("stage 2: %v", err)
	}
	if got := stage2.Meta.Provenance.ParentResultIDs; len(got) != 1 || got[0] != stage1.Meta.Provenance.ResultID {
		t.Fatalf("stage 2 ParentResultIDs = %v, want [%s]", got, stage1.Meta.Provenance.ResultID)
	}

	// Stage 3: declares stage 2 as its parent.
	provider3 := &scriptedProvider{bodies: []string{"stage-3-output"}}
	stage3, err := RunOpResult(WithProvider(context.Background(), provider3), op, "stage-2-output", types.OpOptions{
		ParentResultIDs: []string{stage2.Meta.Provenance.ResultID},
	})
	if err != nil {
		t.Fatalf("stage 3: %v", err)
	}
	if got := stage3.Meta.Provenance.ParentResultIDs; len(got) != 1 || got[0] != stage2.Meta.Provenance.ResultID {
		t.Fatalf("stage 3 ParentResultIDs = %v, want [%s]", got, stage2.Meta.Provenance.ResultID)
	}

	// Walking the chain from stage 3 back to stage 1 resolves, and every ID
	// in it is distinct -- a real chain, not three copies of the same ID.
	if stage3.Meta.Provenance.ParentResultIDs[0] != stage2.Meta.Provenance.ResultID {
		t.Fatal("stage 3 does not resolve back to stage 2")
	}
	if stage2.Meta.Provenance.ParentResultIDs[0] != stage1.Meta.Provenance.ResultID {
		t.Fatal("stage 2 does not resolve back to stage 1")
	}
	ids := []string{stage1.Meta.Provenance.ResultID, stage2.Meta.Provenance.ResultID, stage3.Meta.Provenance.ResultID}
	if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Fatalf("pipeline stages share a ResultID: %v", ids)
	}
}

// 10. Mutating the OpOptions slice a caller passed in after the call does
// not retroactively change the Provenance already returned -- buildProvenance
// copies ParentResultIDs rather than aliasing the caller's backing array.
func TestProvenanceParentResultIDsAreCopiedNotAliased(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"out"}}
	ctx := WithProvider(context.Background(), provider)

	parents := []string{"parent-a"}
	opt := types.OpOptions{ParentResultIDs: parents}

	result, err := RunOpResult(ctx, echoOp(), "x", opt)
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}

	parents[0] = "mutated-after-the-call"

	if result.Meta.Provenance.ParentResultIDs[0] != "parent-a" {
		t.Fatalf("Provenance.ParentResultIDs was aliased to the caller's slice: got %q after mutation",
			result.Meta.Provenance.ParentResultIDs[0])
	}
}

// 11. A declared Normalize step is recorded by its Go symbol name; an
// operation with none records nothing.
func TestProvenanceRecordsNormalizerSymbol(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"final"}}
	op := echoOp()
	op.Contract.Normalize = func(s string) string { return strings.TrimSpace(s) }

	result, err := RunOpResult(WithProvider(context.Background(), provider), op, "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if len(result.Meta.Provenance.NormalizersApplied) != 1 {
		t.Fatalf("NormalizersApplied = %v, want exactly one entry", result.Meta.Provenance.NormalizersApplied)
	}
	if result.Meta.Provenance.NormalizersApplied[0] == "" {
		t.Fatal("the recorded normalizer symbol is empty")
	}

	provider2 := &scriptedProvider{bodies: []string{"final"}}
	plain := echoOp()
	plainResult, err := RunOpResult(WithProvider(context.Background(), provider2), plain, "x", types.OpOptions{})
	if err != nil {
		t.Fatalf("RunOpResult (no normalizer): %v", err)
	}
	if len(plainResult.Meta.Provenance.NormalizersApplied) != 0 {
		t.Fatalf("NormalizersApplied = %v, want empty when Contract.Normalize is nil", plainResult.Meta.Provenance.NormalizersApplied)
	}
}

// 12. A result carries a Provenance even on failure -- ResultID and
// InputDigest still identify the attempt, even though nothing usable was
// produced. This is what lets a caller correlate a failed call in a log
// with the exact input digest that failed, without the log ever holding the
// input itself.
func TestRunOpResultProvenancePopulatedOnFailure(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"never-final", "still-never-final"}}
	op := echoOp(func(string) error { return errFor("always fails") })

	result, err := RunOpResult(WithProvider(context.Background(), provider), op, "the-failing-input", types.OpOptions{})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if result.Meta.Provenance.ResultID == "" {
		t.Error("ResultID is empty on a failed call")
	}
	if want := types.DigestValue("the-failing-input"); result.Meta.Provenance.InputDigest != want {
		t.Errorf("InputDigest = %q, want %q", result.Meta.Provenance.InputDigest, want)
	}
}

// 13. SchemaDigest carries the resolved schema identity when the caller's
// options set one -- the same SchemaID every operation that knows its
// schema already computes (S-002) -- and stays empty when there is none.
func TestProvenanceSchemaDigestFromOpOptions(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")

	provider := &scriptedProvider{bodies: []string{"final"}}
	result, err := RunOpResult(WithProvider(context.Background(), provider), echoOp(), "x",
		types.OpOptions{SchemaID: "widget/v1@abc123def456"})
	if err != nil {
		t.Fatalf("RunOpResult: %v", err)
	}
	if result.Meta.Provenance.SchemaDigest != "widget/v1@abc123def456" {
		t.Errorf("SchemaDigest = %q, want %q", result.Meta.Provenance.SchemaDigest, "widget/v1@abc123def456")
	}
}

// 11. Lineage caps the delivered contract. This drives cappedByLineage
// directly rather than through RunOpResult because no Op-level path yields
// ContractFullyGoverned yet (see contractlevel.go) -- testing it through
// RunOpResult would be testing that a branch never fires, which proves
// nothing about what happens when it does.
func TestBrokenLineageCannotDeliverFullyGoverned(t *testing.T) {
	complete := types.Provenance{
		ResultID:         "r-1",
		InputDigest:      "d-1",
		OperationVersion: "echo@v1",
		ResolvedModel:    "fake-model",
	}

	if got := cappedByLineage(types.ContractFullyGoverned, complete); got != types.ContractFullyGoverned {
		t.Errorf("complete lineage was demoted to %v; nothing was missing", got)
	}

	// Each field on its own is enough to break the chain, and each has to be,
	// or a result missing its model or its input digest still claims the
	// strongest guarantee.
	missing := []struct {
		name  string
		blank func(*types.Provenance)
	}{
		{"ResultID", func(p *types.Provenance) { p.ResultID = "" }},
		{"InputDigest", func(p *types.Provenance) { p.InputDigest = "" }},
		{"OperationVersion", func(p *types.Provenance) { p.OperationVersion = "" }},
		{"ResolvedModel", func(p *types.Provenance) { p.ResolvedModel = "" }},
	}

	for _, tc := range missing {
		t.Run("without"+tc.name, func(t *testing.T) {
			prov := complete
			tc.blank(&prov)

			got := cappedByLineage(types.ContractFullyGoverned, prov)
			if got == types.ContractFullyGoverned {
				t.Errorf("a result with no %s still delivers FullyGoverned", tc.name)
			}
			if got != types.ContractEvidenceChecked {
				t.Errorf("demoted to %v, want ContractEvidenceChecked", got)
			}
		})
	}
}

// A weaker level is not raised or lowered by the cap: this clamps one
// guarantee, it is not a second contract calculation.
func TestLineageCapLeavesWeakerLevelsAlone(t *testing.T) {
	empty := types.Provenance{}

	for _, level := range []types.ContractLevel{
		types.ContractPromptOnly,
		types.ContractJSONWellFormed,
		types.ContractSchemaConstrained,
		types.ContractSchemaAndInvariantChecked,
		types.ContractEvidenceChecked,
	} {
		if got := cappedByLineage(level, empty); got != level {
			t.Errorf("%v was changed to %v by the lineage cap", level, got)
		}
	}
}
