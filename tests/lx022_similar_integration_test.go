package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

// LX-022, through the exported API and the real OpenAI provider: the request
// declares a strict schema with no verdict field in it, and the verdict that
// comes back is the score compared with the threshold in Go.
func TestIntegrationSimilarIsDecidedFromTheScore(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		threshold float64
		wantErr   bool
		similar   bool
	}{
		{"above the threshold", `{"score":0.92,"matched_aspects":null,"differing_aspects":null,"explanation":"same fact"}`, 0.7, false, true},
		{"below the threshold", `{"score":0.31,"matched_aspects":null,"differing_aspects":null,"explanation":"different facts"}`, 0.7, false, false},
		{"at the threshold", `{"score":0.7,"matched_aspects":null,"differing_aspects":null,"explanation":"close"}`, 0.7, false, true},
		{"a verdict the model volunteers is not consulted", `{"is_similar":true,"score":0.2,"explanation":"x"}`, 0.7, false, false},
		{"a bare boolean", `true`, 0.7, true, false},
		{"no score", `{"explanation":"they look alike"}`, 0.7, true, false},
		{"a score off the scale", `{"score":4,"explanation":"x"}`, 0.7, true, false},
		{"prose", `These two sentences are quite similar.`, 0.7, true, false},
		{"a stricter threshold", `{"score":0.92,"explanation":"x"}`, 0.95, false, false},
		{"a looser threshold", `{"score":0.31,"explanation":"x"}`, 0.3, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			result, err := schemaflux.Similar(
				"The flight was cancelled due to fog.",
				"Fog forced the airline to cancel the flight.",
				schemaflux.NewSimilarOptions().WithSimilarityThreshold(tc.threshold))

			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted the response: IsSimilar=%v Score=%v", result.IsSimilar, result.Score)
				}
				if result.IsSimilar || result.Score != 0 {
					t.Fatalf("a failed call returned IsSimilar=%v Score=%v", result.IsSimilar, result.Score)
				}
			} else {
				if err != nil {
					t.Fatalf("Similar: %v", err)
				}
				if result.IsSimilar != tc.similar {
					t.Fatalf("IsSimilar = %v with Score %v against %v, want %v", result.IsSimilar, result.Score, tc.threshold, tc.similar)
				}
			}

			if len(*captured) == 0 {
				t.Fatal("no request reached the provider")
			}
			text, _ := (*captured)[0]["text"].(map[string]any)
			format, _ := text["format"].(map[string]any)
			if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "similarity" {
				t.Fatalf("expected the strict similarity schema on the wire, got %v", format)
			}
			schema, _ := format["schema"].(map[string]any)
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema on the wire breaks a strict-mode rule: %s", problem)
			}
			if _, asked := schema["properties"].(map[string]any)["is_similar"]; asked {
				t.Fatal("the model is asked for a verdict that Go computes")
			}
			// The threshold is Go's business. Told where the line is, a model
			// aims at it.
			wire, _ := json.Marshal((*captured)[0])
			if strings.Contains(strings.ToLower(string(wire)), "threshold") {
				t.Fatal("the request tells the model the threshold")
			}
		})
	}
}

// Named aspects, through the exported API: the request declares one required
// entry per aspect, and the two lists a caller reads are sorted in Go from each
// aspect's score.
func TestIntegrationSimilarScoresEachNamedAspect(t *testing.T) {
	aspects := []string{"problem description", "underlying issue", "user intent"}
	cases := []struct {
		name      string
		body      string
		wantErr   error
		matched   string
		differing string
	}{
		{"every aspect scored",
			`{"score":0.9,"aspects":{"a1":{"score":0.9,"reason":"same symptom"},"a2":{"score":0.3,"reason":"different cause"},"a3":{"score":0.8,"reason":"both want access"}},"explanation":"x"}`,
			nil, "problem description|user intent", "underlying issue"},
		{"all above the threshold",
			`{"score":0.9,"aspects":{"a1":{"score":0.9,"reason":null},"a2":{"score":0.7,"reason":null},"a3":{"score":1,"reason":null}},"explanation":"x"}`,
			nil, "problem description|underlying issue|user intent", ""},
		{"all below the threshold",
			`{"score":0.1,"aspects":{"a1":{"score":0.1,"reason":"x"},"a2":{"score":0,"reason":"x"},"a3":{"score":0.69,"reason":"x"}},"explanation":"x"}`,
			nil, "", "problem description|underlying issue|user intent"},
		{"keyed by the aspect's name",
			`{"score":0.9,"aspects":{"User Intent":{"score":0.9},"problem description":{"score":0.9},"underlying issue":{"score":0.2}},"explanation":"x"}`,
			nil, "problem description|user intent", "underlying issue"},
		{"the two lists of the old shape",
			`{"score":0.9,"matched_aspects":[{"aspect":"problem description","score":0.9},{"aspect":"underlying issue","score":0.2}],"differing_aspects":[{"aspect":"user intent","score":0.9}],"explanation":"x"}`,
			nil, "problem description|user intent", "underlying issue"},
		{"one aspect left out",
			`{"score":0.9,"aspects":{"a1":{"score":0.9},"a3":{"score":0.8}},"explanation":"x"}`,
			schemaflux.ErrInvariantViolation, "", ""},
		{"no aspects at all",
			`{"score":0.9,"explanation":"x"}`, schemaflux.ErrInvariantViolation, "", ""},
		{"a rephrased name",
			`{"score":0.9,"aspects":[{"aspect":"problem description","score":0.9},{"aspect":"underlying issue","score":0.9},{"aspect":"intent of the user","score":0.9}],"explanation":"x"}`,
			schemaflux.ErrInvariantViolation, "", ""},
		{"an aspect score off the scale",
			`{"score":0.9,"aspects":{"a1":{"score":9},"a2":{"score":0.3},"a3":{"score":0.8}},"explanation":"x"}`,
			schemaflux.ErrInvariantViolation, "", ""},
		{"an aspect with no score",
			`{"score":0.9,"aspects":{"a1":{"reason":"same"},"a2":{"score":0.3},"a3":{"score":0.8}},"explanation":"x"}`,
			schemaflux.ErrSchemaViolation, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, captured := capturingResponsesAPI(t, testfixtures.ResponsesMessage(tc.body))

			result, err := schemaflux.Similar(
				"Can't log in: it says my password is wrong.",
				"Unable to sign in, invalid credentials.",
				schemaflux.NewSimilarOptions().WithAspects(aspects))

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if result.IsSimilar || result.Score != 0 || len(result.MatchedAspects)+len(result.DifferingAspects) != 0 {
					t.Fatalf("a failed call returned a usable result: %+v", result)
				}
			} else {
				if err != nil {
					t.Fatalf("Similar: %v", err)
				}
				if got := lx022Names(result.MatchedAspects); got != tc.matched {
					t.Errorf("MatchedAspects = %q, want %q", got, tc.matched)
				}
				if got := lx022Names(result.DifferingAspects); got != tc.differing {
					t.Errorf("DifferingAspects = %q, want %q", got, tc.differing)
				}
			}

			if len(*captured) != 1 {
				t.Fatalf("%d requests reached the provider, want 1", len(*captured))
			}
			text, _ := (*captured)[0]["text"].(map[string]any)
			format, _ := text["format"].(map[string]any)
			schema, _ := format["schema"].(map[string]any)
			if problem := llm.StrictSchemaProblem(schema); problem != "" {
				t.Fatalf("the schema on the wire breaks a strict-mode rule: %s", problem)
			}
			declared, _ := schema["properties"].(map[string]any)["aspects"].(map[string]any)
			entries, _ := declared["properties"].(map[string]any)
			required, _ := declared["required"].([]any)
			if declared["type"] != "object" || len(entries) != len(aspects) || len(required) != len(aspects) {
				t.Fatalf("aspects on the wire = %v, want an object with %d required entries", declared, len(aspects))
			}
			// The declared schema carries nothing of the caller's, so an aspect
			// name cannot be read as an instruction there. The name is in the
			// user message, quoted, beside the key the answer has to use.
			message := userMessageText((*captured)[0])
			for i, name := range aspects {
				key := fmt.Sprintf("a%d", i+1)
				entry, _ := entries[key].(map[string]any)
				if description, described := entry["description"]; described {
					t.Errorf("entry %s is described as %v: the schema holds none of the caller's text", key, description)
				}
				if want := fmt.Sprintf("%s: %q", key, name); !strings.Contains(message, want) {
					t.Errorf("the user message does not carry %s", want)
				}
			}
		})
	}
}

// userMessageText joins every string the request carries in its input, which
// is the user message; the system prompt travels in "instructions".
func userMessageText(request map[string]any) string {
	var parts []string
	var walk func(value any)
	walk = func(value any) {
		switch v := value.(type) {
		case string:
			parts = append(parts, v)
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for _, key := range []string{"content", "text"} {
				walk(v[key])
			}
		}
	}
	walk(request["input"])
	return strings.Join(parts, "\n")
}

func lx022Names(aspects []schemaflux.AspectMatch) string {
	names := make([]string, len(aspects))
	for i, aspect := range aspects {
		names[i] = aspect.Aspect
	}
	return strings.Join(names, "|")
}

// ExampleSimilar shows the shape a caller writes: a comparison that reads as a
// condition. The verdict is the score against the threshold, so the two can
// never disagree. It runs under go test with a scripted provider: no
// credential, no spend.
func ExampleSimilar() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"score": 0.93, "explanation": "Both say fog caused the cancellation."}`))

	result, err := schemaflux.Similar(
		"The flight was cancelled due to fog.",
		"Fog forced the airline to cancel the flight.",
		schemaflux.NewSimilarOptions().WithSimilarityThreshold(0.8))
	if err != nil {
		fmt.Println("the comparison could not be made:", err)
		return
	}

	if result.IsSimilar {
		fmt.Printf("same report (score %.2f, threshold %.2f)\n", result.Score, result.Threshold)
	}

	// Output:
	// same report (score 0.93, threshold 0.80)
}

// ExampleSimilar_aspects names what to compare. Each aspect comes back scored
// once, under the name the caller gave it, and is sorted into matched or
// differing by the same threshold as the verdict.
func ExampleSimilar_aspects() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(
		`{"score": 0.82, "aspects": {"a1": {"score": 0.9, "reason": "both cannot sign in"}, "a2": {"score": 0.4, "reason": "one blames the password, one the account"}}, "explanation": "The same symptom with different suspected causes."}`))

	result, err := schemaflux.Similar(
		"Can't log in: it says my password is wrong.",
		"Unable to sign in, my account seems locked.",
		schemaflux.NewSimilarOptions().WithAspects([]string{"symptom", "suspected cause"}))
	if err != nil {
		fmt.Println("the comparison could not be made:", err)
		return
	}

	fmt.Println("duplicate:", result.IsSimilar)
	for _, aspect := range result.MatchedAspects {
		fmt.Printf("same %s: %s\n", aspect.Aspect, aspect.Reason)
	}
	for _, aspect := range result.DifferingAspects {
		fmt.Printf("different %s: %s\n", aspect.Aspect, aspect.Reason)
	}

	// Output:
	// duplicate: true
	// same symptom: both cannot sign in
	// different suspected cause: one blames the password, one the account
}

// ExampleSimilar_failsClosed is the counterpart: an answer with no score is an
// error, so `if result.IsSimilar` cannot be satisfied by a response that did
// not measure anything.
func ExampleSimilar_failsClosed() {
	schemaflux.NewClient("example-key").WithProviderInstance(testfixtures.NewScripted(`true`))

	result, err := schemaflux.Similar("a", "b", schemaflux.NewSimilarOptions())

	fmt.Println("error is nil:", err == nil)
	fmt.Println("similar:", result.IsSimilar)
	fmt.Println("score:", result.Score)

	// Output:
	// error is nil: false
	// similar: false
	// score: 0
}
