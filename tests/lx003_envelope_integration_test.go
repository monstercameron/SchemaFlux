package tests

import (
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// LX-003, through the exported API and the real OpenAI provider.
//
// Closing an envelope changes what the model is allowed to send back: a field
// it used to omit it must now send, as a value when the caller asked for it and
// as null otherwise. These run each operation whose envelope was rewritten
// against a response in that shape and check two things: the schema that went
// out is sent as strict and breaks none of the rules the live API enforces, and
// the answer still decodes to the right result.

func strictSchemaOnTheWire(t *testing.T, captured *[]map[string]any, name string) {
	t.Helper()
	if len(*captured) == 0 {
		t.Fatal("no request reached the provider")
	}
	body := (*captured)[len(*captured)-1]
	text, _ := body["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true {
		t.Fatalf("expected a strict json_schema format, got %v", format)
	}
	if format["name"] != name {
		t.Fatalf("schema name = %v, want %q", format["name"], name)
	}
	schema, _ := format["schema"].(map[string]any)
	if problem := llm.StrictSchemaProblem(schema); problem != "" {
		t.Fatalf("the schema on the wire is not strict-valid: %s", problem)
	}
}

func TestIntegrationQuestionEnvelopeIsStrictAndAcceptsNulls(t *testing.T) {
	_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(`{"answer":true,"confidence":0.9,"reasoning":"over eighteen","evidence":["age 24"]}`))

	out, err := schemaflux.Question[map[string]any, bool](map[string]any{"age": 24}, schemaflux.NewQuestionOptions("Is this an adult?"))
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if !out.Answer {
		t.Fatalf("Answer = false, want true")
	}
	strictSchemaOnTheWire(t, captured, "question_answer")
}

func TestIntegrationQuestionEnvelopeWithEveryOptionalField(t *testing.T) {
	_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(`{"answer":"forty dollars","confidence":0.9,"reasoning":"stated in the text","evidence":[]}`))

	opts := schemaflux.NewQuestionOptions("What is the total?")
	opts.IncludeConfidence, opts.IncludeReasoning, opts.IncludeEvidence = true, true, true
	out, err := schemaflux.Question[string, string]("The invoice total is forty dollars.", opts)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if out.Answer != "forty dollars" {
		t.Fatalf("Answer = %q", out.Answer)
	}
	if out.Reasoning != "stated in the text" || out.ModelConfidence != 0.9 {
		t.Fatalf("the requested fields did not come back: reasoning %q, confidence %v", out.Reasoning, out.ModelConfidence)
	}
	strictSchemaOnTheWire(t, captured, "question_answer")
}

func TestIntegrationCompressEnvelopeIsStrictAndAcceptsNulls(t *testing.T) {
	_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(`{"compressed":"Launch plan and owners.","preserved_info":null,"removed_info":null}`))

	out, err := schemaflux.Compress("This update explains the launch timeline, rollout steps, fallback plan, and ownership for each dependency.", schemaflux.NewCompressOptions())
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if out.Compressed != "Launch plan and owners." {
		t.Fatalf("Compressed = %q", out.Compressed)
	}
	strictSchemaOnTheWire(t, captured, "compression")
}

func TestIntegrationPredictEnvelopeIsStrictAndAcceptsNulls(t *testing.T) {
	_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(
		`{"prediction":124.5,"confidence":0.7,"interval":null,"scenarios":null,"factors":null,"reasoning":"steady growth","assumptions":null,"risks":null}`))

	opts := schemaflux.NewPredictOptions()
	opts.Horizon = "next month"
	out, err := schemaflux.Predict[float64]([]map[string]any{{"period": "January", "value": 100.0}, {"period": "February", "value": 112.0}}, opts)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if out.Prediction != 124.5 {
		t.Fatalf("Prediction = %v, want 124.5", out.Prediction)
	}
	if out.Interval != nil || len(out.Scenarios) != 0 || len(out.Factors) != 0 {
		t.Fatalf("null optional parts should decode to empty")
	}
	strictSchemaOnTheWire(t, captured, "prediction")
}

func TestIntegrationPredictEnvelopeCarriesTheOptionalPartsWhenPresent(t *testing.T) {
	_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(
		`{"prediction":124.5,"confidence":0.7,
		  "interval":{"lower":110,"upper":140,"confidence_level":0.8},
		  "scenarios":[{"name":"Optimistic","description":"demand holds","prediction":140,"probability":0.25,"conditions":["no churn"]}],
		  "factors":[{"name":"seasonality","impact":"positive","weight":0.3,"reasoning":null}],
		  "reasoning":"steady growth","assumptions":["no price change"],"risks":["one large customer"]}`))

	opts := schemaflux.NewPredictOptions()
	opts.Horizon = "next month"
	out, err := schemaflux.Predict[float64]([]map[string]any{{"period": "January", "value": 100.0}}, opts)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if out.Interval == nil || out.Interval.Upper != 140 {
		t.Fatalf("Interval = %+v, want an upper bound of 140", out.Interval)
	}
	if len(out.Scenarios) != 1 || out.Scenarios[0].Name != "Optimistic" {
		t.Fatalf("Scenarios = %+v", out.Scenarios)
	}
	if len(out.Factors) != 1 || out.Factors[0].Weight != 0.3 {
		t.Fatalf("Factors = %+v", out.Factors)
	}
	strictSchemaOnTheWire(t, captured, "prediction")
}

func TestIntegrationDecomposeEnvelopeIsStrictAndAcceptsNulls(t *testing.T) {
	_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(
		`{"parts":[{"id":"p1","name":"Design","description":null,"content":"Design the page","parent_id":null,"children":null,"dependencies":null,"depth":0,"order":1,"estimate":null},
		           {"id":"p2","name":"Billing","description":"","content":"Integrate billing","parent_id":null,"children":null,"dependencies":["p1"],"depth":0,"order":2,"estimate":null}],
		  "root_parts":null}`))

	out, err := schemaflux.Decompose("Design the page, then integrate billing.", schemaflux.NewDecomposeOptions())
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if out.TotalParts != 2 {
		t.Fatalf("TotalParts = %d, want 2", out.TotalParts)
	}
	strictSchemaOnTheWire(t, captured, "decomposition")
}
