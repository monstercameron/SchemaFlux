package tests

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// These tests run a staged plan through the root: the structured output of one
// stage is what the next one is sent, the lineage names the stage before, and a
// stage that a deterministic check settles makes no call.

type ticket struct {
	Subject  string `json:"subject"`
	Priority string `json:"priority"`
}

const (
	stageOneInstruction = "stage one: tidy the subject"
	stageTwoInstruction = "stage two: set the priority"
)

// ticketStage is a stage over a ticket: its operation sends the ticket as JSON
// under instruction and reads the answer as a ticket.
func ticketStage(name, instruction string) schemaflux.Stage[ticket] {
	return schemaflux.Stage[ticket]{Op: schemaflux.Op[ticket, ticket]{
		ID:        schemaflux.OperationID{Name: name, Version: "v1"},
		Semantics: schemaflux.Semantics{Stability: schemaflux.StabilityExperimental},
		Contract: schemaflux.OutputContract[ticket]{
			Decode: func(body string) (ticket, error) {
				var out ticket
				if err := json.Unmarshal([]byte(body), &out); err != nil {
					return ticket{}, err
				}
				return out, nil
			},
		},
		Batch: schemaflux.BatchAlgebra[ticket, ticket]{Class: schemaflux.BatchNone},
		BuildPrompt: func(in ticket, _ schemaflux.OpOptions) (string, string) {
			body, _ := json.Marshal(in)
			return instruction, string(body)
		},
	}}
}

// providerForTickets answers stage one with a tidied subject and, when
// priorityFromStageOne is set, a priority already; and stage two with the ticket
// it was sent plus a priority.
func providerForTickets(t *testing.T, priorityFromStageOne string) *schemafluxtest.Provider {
	return schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		// The library puts its own preface ahead of an operation's instruction.
		switch {
		case strings.HasSuffix(req.SystemPrompt, stageOneInstruction):
			return `{"subject":"Printer on fire","priority":"` + priorityFromStageOne + `"}`, nil
		case strings.HasSuffix(req.SystemPrompt, stageTwoInstruction):
			var sent ticket
			if err := json.Unmarshal([]byte(req.UserPrompt), &sent); err != nil {
				t.Errorf("stage two was not sent a ticket: %v", err)
			}
			sent.Priority = "high"
			body, _ := json.Marshal(sent)
			return string(body), nil
		}
		t.Errorf("a call with the instruction %q", req.SystemPrompt)
		return "{}", nil
	})
}

func TestRunStagedPlanSendsEachStageTheStageBeforeItsOutput(t *testing.T) {
	provider := providerForTickets(t, "")
	schemafluxtest.Install(t, provider)

	plan := schemaflux.StagedPlan[ticket]{
		ID: schemaflux.OperationID{Name: "triage", Version: "v1"},
		Stages: []schemaflux.Stage[ticket]{
			ticketStage("tidy", stageOneInstruction),
			ticketStage("prioritise", stageTwoInstruction),
		},
	}
	result, err := schemaflux.RunStagedPlan(context.Background(), plan, ticket{Subject: "printer broke lol"}, schemaflux.OpOptions{})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	if want := (ticket{Subject: "Printer on fire", Priority: "high"}); result.Value != want {
		t.Errorf("Value = %+v, want %+v", result.Value, want)
	}
	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("the provider was called %d times, want one per stage", len(requests))
	}
	// The second stage is sent what the first produced, and not the source.
	if !strings.Contains(requests[1].UserPrompt, "Printer on fire") || strings.Contains(requests[1].UserPrompt, "printer broke lol") {
		t.Errorf("stage two was sent %q: it has to be stage one's output and not the original", requests[1].UserPrompt)
	}

	if len(result.Stages) != 2 {
		t.Fatalf("Stages = %v, want one outcome for each of the two stages", result.Stages)
	}
	first, second := result.Stages[0], result.Stages[1]
	if first.Skipped || second.Skipped {
		t.Error("a stage with no deterministic check was skipped")
	}
	if first.StageID.Name != "tidy" || second.StageID.Name != "prioritise" {
		t.Errorf("stage ids %v and %v: an unnamed stage is named for its operation", first.StageID, second.StageID)
	}
	if first.Meta.Attempts == 0 || second.Meta.Attempts == 0 {
		t.Errorf("a stage that called its operation reports %d and %d attempts", first.Meta.Attempts, second.Meta.Attempts)
	}
	if first.Provenance.ResultID == "" {
		t.Fatal("the first stage has no lineage")
	}
	if got := second.Provenance.ParentResultIDs; len(got) != 1 || got[0] != first.Provenance.ResultID {
		t.Errorf("the second stage's parents are %v, want the first stage's result %q", got, first.Provenance.ResultID)
	}
	if len(first.Provenance.ParentResultIDs) != 0 {
		t.Errorf("the first stage has parents %v", first.Provenance.ParentResultIDs)
	}
}

func TestRunStagedPlanSkipsAStageThatADeterministicCheckSettles(t *testing.T) {
	// Stage one already sets a priority, so the check on stage two has nothing
	// left to ask a model.
	provider := providerForTickets(t, "low")
	schemafluxtest.Install(t, provider)

	checked := ticketStage("prioritise", stageTwoInstruction)
	checked.RequiredContract = schemaflux.ContractSchemaAndInvariantChecked
	checked.DeterministicCheck = func(in ticket) (ticket, schemaflux.ContractLevel, bool) {
		if in.Priority == "" {
			return in, schemaflux.ContractPromptOnly, false
		}
		return in, schemaflux.ContractFullyGoverned, true
	}
	plan := schemaflux.StagedPlan[ticket]{
		ID: schemaflux.OperationID{Name: "triage", Version: "v1"},
		Stages: []schemaflux.Stage[ticket]{
			ticketStage("tidy", stageOneInstruction),
			checked,
		},
	}
	result, err := schemaflux.RunStagedPlan(context.Background(), plan, ticket{Subject: "printer broke lol"}, schemaflux.OpOptions{})
	if err != nil {
		t.Fatalf("RunStagedPlan: %v", err)
	}

	// Counted, because a call that happened to agree with the check is
	// indistinguishable from a skip by the value.
	if got := len(provider.Requests()); got != 1 {
		t.Fatalf("the provider was called %d times, want 1: the second stage was settled in Go", got)
	}
	skipped := result.Stages[1]
	if !skipped.Skipped || skipped.DeliveredContract != schemaflux.ContractFullyGoverned {
		t.Errorf("second stage: skipped %v, delivered %v", skipped.Skipped, skipped.DeliveredContract)
	}
	if skipped.Meta.Attempts != 0 || skipped.Meta.Cost.TotalCost != 0 {
		t.Errorf("a stage that made no call reports attempts %d and cost %v", skipped.Meta.Attempts, skipped.Meta.Cost.TotalCost)
	}
	if want := (ticket{Subject: "Printer on fire", Priority: "low"}); result.Value != want {
		t.Errorf("Value = %+v, want %+v", result.Value, want)
	}
	if got := skipped.Provenance.ParentResultIDs; len(got) != 1 || got[0] != result.Stages[0].Provenance.ResultID {
		t.Errorf("a skipped stage still names its parent: %v", got)
	}
}

func TestRunStagedPlanEndsWithTheFailureOfAStageAndReturnsNoValueButWhatWasSpent(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		if strings.HasSuffix(req.SystemPrompt, stageTwoInstruction) {
			return "", errors.New("the provider could not serve this stage")
		}
		return `{"subject":"Printer on fire","priority":""}`, nil
	})
	schemafluxtest.Install(t, provider)

	plan := schemaflux.StagedPlan[ticket]{
		ID: schemaflux.OperationID{Name: "triage", Version: "v1"},
		Stages: []schemaflux.Stage[ticket]{
			ticketStage("tidy", stageOneInstruction),
			ticketStage("prioritise", stageTwoInstruction),
		},
	}
	result, err := schemaflux.RunStagedPlan(context.Background(), plan, ticket{Subject: "printer broke lol"}, schemaflux.OpOptions{})
	if err == nil {
		t.Fatal("a plan whose second stage failed returned no error")
	}
	if result.Value != (ticket{}) {
		t.Errorf("value %+v beside an error: a failed plan has no value", result.Value)
	}
	// What the stage that ran cost, and what the one that failed did, are not lost
	// with the value.
	if len(result.Stages) != 2 || result.Stages[0].Err != nil || result.Stages[0].Meta.Attempts != 1 ||
		result.Stages[1].Err == nil || result.Stages[1].Meta.Attempts < 1 {
		t.Errorf("outcomes %+v, want the stage that completed and the one that failed, each with its attempts", result.Stages)
	}
	if schemaflux.KindOf(err) == types.KindUnknown {
		t.Errorf("the failure %v is unclassified", err)
	}
	if text := err.Error(); !strings.Contains(text, "prioritise") || strings.Contains(text, "printer broke lol") || strings.Contains(text, "Printer on fire") {
		t.Errorf("the error %q has to name the stage and hold none of the datum", text)
	}
}

// A panic in a stage's own Decode is that stage's failure, not the end of the
// process, and the result beside the error holds what the paid stage cost and what
// the failed one did.
func TestRunStagedPlanAPanicInAStageKeepsWhatTheEarlierStagesCost(t *testing.T) {
	provider := providerForTickets(t, "")
	schemafluxtest.Install(t, provider)
	first, second := ticketStage("tidy", stageOneInstruction), ticketStage("prioritise", stageTwoInstruction)
	second.Op.Contract.Decode = func(string) (ticket, error) { panic("the caller's decoder broke on a customer note") }
	plan := schemaflux.StagedPlan[ticket]{ID: schemaflux.OperationID{Name: "tickets"}, Stages: []schemaflux.Stage[ticket]{first, second}}

	result, err := schemaflux.RunStagedPlan(context.Background(), plan, ticket{Subject: "printer fire"}, schemaflux.OpOptions{ParentResultIDs: []string{"from-the-caller"}})
	if err == nil || !errors.Is(err, schemaflux.ErrInvariantViolation) || strings.Contains(err.Error(), "customer note") {
		t.Fatalf("err = %v (%v), want an internal failure with no panic value in it", err, schemaflux.KindOf(err))
	}
	if len(result.Stages) != 2 || result.Stages[0].Meta.Attempts != 1 || result.Stages[1].Meta.Attempts != 1 || result.Stages[1].Err == nil {
		t.Fatalf("stages = %+v, want the two requests that were paid for", result.Stages)
	}
	if got := result.Stages[0].Provenance.ParentResultIDs; len(got) != 1 || got[0] != "from-the-caller" {
		t.Errorf("the first stage's parents are %v, want the caller's", got)
	}
	if len(provider.Requests()) != 2 {
		t.Errorf("%d requests were sent, want 2", len(provider.Requests()))
	}
}
