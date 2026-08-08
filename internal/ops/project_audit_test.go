package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

type sourceRecord struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
	Address string `json:"address"`
}

type targetRecord struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Region   string `json:"region"`
}

func installProjectResponse(t *testing.T, body string) {
	t.Helper()
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		return body, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
}

func namesInclude(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// OP-302. Lost and Inferred were whatever the model said they were -- an audit
// trail written by the thing being audited. A model that drops a field and does
// not mention it produced a projection claiming to have lost nothing.
func TestLostAndInferredAreComputedNotReported(t *testing.T) {
	// The model drops phone and address, invents region, and reports none of it.
	installProjectResponse(t, `{
		"projected": {"full_name":"Ada Lovelace","email":"ada@example.com","region":"EU"},
		"mappings": [],
		"lost": [],
		"inferred": [],
		"confidence": 0.95
	}`)

	source := sourceRecord{
		Name:    "Ada Lovelace",
		Email:   "ada@example.com",
		Phone:   "305-555-1234",
		Address: "12 High Street",
	}

	result, err := Project[sourceRecord, targetRecord](source)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	for _, field := range []string{"phone", "address", "name"} {
		if !namesInclude(result.Lost, field) {
			t.Errorf("Lost = %v, want it to include %q -- the diff did not run", result.Lost, field)
		}
	}
	if !namesInclude(result.Inferred, "region") {
		t.Errorf("Inferred = %v, want it to include region", result.Inferred)
	}
	if namesInclude(result.Lost, "email") {
		t.Errorf("Lost = %v; email is present in both", result.Lost)
	}

	// The model's own account is kept, and the disagreement is visible.
	if len(result.ModelClaimedLost) != 0 {
		t.Errorf("ModelClaimedLost = %v, want the empty list the model actually sent", result.ModelClaimedLost)
	}
	if len(result.Lost) == len(result.ModelClaimedLost) {
		t.Error("the computed and claimed sets agree; this case exists because they should not")
	}
}

// A faithful projection reports nothing lost, so the check is not simply
// pessimistic.
func TestAFaithfulProjectionReportsNothingLost(t *testing.T) {
	type narrow struct {
		Email string `json:"email"`
	}

	installProjectResponse(t, `{
		"projected": {"email":"ada@example.com"},
		"mappings": [],
		"confidence": 0.99
	}`)

	result, err := Project[narrow, narrow](narrow{Email: "ada@example.com"})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(result.Lost) != 0 {
		t.Errorf("Lost = %v, want none", result.Lost)
	}
	if len(result.Inferred) != 0 {
		t.Errorf("Inferred = %v, want none", result.Inferred)
	}
}

// The diff reads what the projection contains, not what its type declares:
// omitempty means a declared field can be absent, and the question is what the
// caller actually received.
func TestTheDiffReadsWhatIsPresentNotWhatIsDeclared(t *testing.T) {
	type sparse struct {
		Kept    string `json:"kept"`
		Omitted string `json:"omitted,omitempty"`
	}

	installProjectResponse(t, `{
		"projected": {"kept":"value"},
		"mappings": [],
		"confidence": 0.9
	}`)

	result, err := Project[sourceRecord, sparse](sourceRecord{Name: "Ada"})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if namesInclude(result.Inferred, "omitted") {
		t.Errorf("Inferred = %v; a field the projection does not contain was reported as inferred", result.Inferred)
	}
}
