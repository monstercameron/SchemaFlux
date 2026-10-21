package ops

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

func TestSuggestOptions(t *testing.T) {
	opts := NewSuggestOptions()
	if err := opts.Validate(); err != nil {
		t.Errorf("Default options validation failed: %v", err)
	}

	if opts.Strategy != SuggestContextual {
		t.Errorf("Expected default strategy Contextual, got %v", opts.Strategy)
	}
	if opts.TopN != 5 {
		t.Errorf("Expected default TopN 5, got %d", opts.TopN)
	}
	if opts.Ranked != true {
		t.Errorf("Expected default Ranked true, got %v", opts.Ranked)
	}
}

func TestSuggestOptions_FluentAPI(t *testing.T) {
	opts := NewSuggestOptions().
		WithStrategy(SuggestHybrid).
		WithTopN(10).
		WithRanked(false).
		WithIncludeScores(true).
		WithIncludeReasons(true).
		WithDomain("data-processing").
		WithConstraints([]string{"must be efficient", "cost-effective"}).
		WithCategories([]string{"optimization", "scaling"})

	if opts.Strategy != SuggestHybrid {
		t.Errorf("Expected strategy Hybrid, got %v", opts.Strategy)
	}
	if opts.TopN != 10 {
		t.Errorf("Expected TopN 10, got %d", opts.TopN)
	}
	if opts.Ranked != false {
		t.Errorf("Expected Ranked false, got %v", opts.Ranked)
	}
	if opts.IncludeScores != true {
		t.Errorf("Expected IncludeScores true, got %v", opts.IncludeScores)
	}
	if opts.IncludeReasons != true {
		t.Errorf("Expected IncludeReasons true, got %v", opts.IncludeReasons)
	}
	if opts.Domain != "data-processing" {
		t.Errorf("Expected Domain 'data-processing', got %v", opts.Domain)
	}
	if len(opts.Constraints) != 2 {
		t.Errorf("Expected 2 constraints, got %d", len(opts.Constraints))
	}
	if len(opts.Categories) != 2 {
		t.Errorf("Expected 2 categories, got %d", len(opts.Categories))
	}
}

func TestSuggestOptions_Validation(t *testing.T) {
	tests := []struct {
		name    string
		opts    SuggestOptions
		wantErr bool
	}{
		{"valid defaults", NewSuggestOptions(), false},
		{"invalid topN", NewSuggestOptions().WithTopN(0), true},
		{"invalid strategy", SuggestOptions{Strategy: "invalid"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// suggestAction is the struct form of a suggestion, as a caller of Suggest[T]
// with a struct T would declare it.
type suggestAction struct {
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

// A null element is not a suggestion for a target that takes null without
// complaint: a number reads it as 0, a bool as false, a map as nil.
func TestSuggestRefusesANullElementForATargetThatTakesNull(t *testing.T) {
	swapLLMResponse(t, `[1, null]`)
	if got, err := Suggest[int]("anything", NewSuggestOptions()); err == nil {
		t.Fatalf("a null suggestion was read as the number 0: %v", got)
	}
	swapLLMResponse(t, `[1.5, null]`)
	if got, err := Suggest[float64]("anything", NewSuggestOptions()); err == nil {
		t.Fatalf("a null suggestion was read as the number 0: %v", got)
	}
	swapLLMResponse(t, `[true, null]`)
	if got, err := Suggest[bool]("anything", NewSuggestOptions()); err == nil {
		t.Fatalf("a null suggestion was read as false: %v", got)
	}
	swapLLMResponse(t, `[{"a":"b"}, null]`)
	if got, err := Suggest[map[string]string]("anything", NewSuggestOptions()); err == nil {
		t.Fatalf("a null suggestion was read as an empty map: %v", got)
	}
	// The same list without the null is accepted, so the refusals above are about
	// the null element and not about the target type.
	swapLLMResponse(t, `[1, 2]`)
	if got, err := Suggest[int]("anything", NewSuggestOptions()); err != nil || len(got) != 2 {
		t.Errorf("a list of two numbers: %v, %v", got, err)
	}
}

// A scripted answer is decoded the same way every time, and the three shapes a
// caller can ask for (a string, a struct and a pointer to one) are refused for
// the same answers: one that holds no suggestion is not one suggestion with
// every field empty.
func TestSuggestRefusesAnswersThatHoldNoUsableSuggestion(t *testing.T) {
	bodies := []struct{ name, body string }{
		{"an error object", `{"error":"rate limited"}`},
		{"an empty suggestions list", `{"suggestions":[]}`},
		{"a null suggestions list", `{"suggestions":null}`},
		{"an empty list", `[]`},
		{"a list with only null", `[null]`},
		{"null", `null`},
		{"an empty object", `{}`},
		{"a bare string", `"just text"`},
		{"a number", `42`},
		{"prose", `I could not think of anything.`},
		{"a list with one unusable element after a usable one", `[{"name":"a","priority":1},{"unrelated":1}]`},
		{"a suggestions list with only unrelated keys", `{"suggestions":[{"unrelated":1}]}`},
		{"a list of unrelated objects", `[{"unrelated":1}]`},
	}

	for _, body := range bodies {
		t.Run("struct/"+body.name, func(t *testing.T) {
			swapLLMResponse(t, body.body)
			got, err := Suggest[suggestAction]("context", NewSuggestOptions())
			if err == nil {
				t.Fatalf("an answer with no suggestion returned %+v", got)
			}
			if got != nil {
				t.Errorf("suggestions %+v came back beside the error", got)
			}
			if kind := types.KindOf(err); kind != types.KindSchemaViolation && kind != types.KindMalformedOutput {
				t.Errorf("kind = %v, want a classified refusal: %v", kind, err)
			}
		})
		t.Run("pointer/"+body.name, func(t *testing.T) {
			swapLLMResponse(t, body.body)
			if got, err := Suggest[*suggestAction]("context", NewSuggestOptions()); err == nil {
				t.Fatalf("an answer with no suggestion returned %+v", got)
			}
		})
	}

	stringBodies := []struct{ name, body string }{
		{"an error object", `{"error":"rate limited"}`},
		{"an empty suggestions list", `{"suggestions":[]}`},
		{"an empty list", `[]`},
		{"null", `null`},
		{"an empty object", `{}`},
		{"a blank string", `[""]`},
		{"a whitespace string", `["   "]`},
		{"a blank single suggestion", `{"suggestion":"  "}`},
		{"objects with no usable key", `[{"unrelated":"x"}]`},
		{"one object with no usable key among usable ones", `[{"name":"a"},{"other":"b"},{"text":"c"}]`},
		{"a number in the list", `["a",3]`},
	}
	for _, body := range stringBodies {
		t.Run("string/"+body.name, func(t *testing.T) {
			swapLLMResponse(t, body.body)
			got, err := Suggest[string]("context", NewSuggestOptions())
			if err == nil {
				t.Fatalf("an answer with no usable suggestion returned %q", got)
			}
			if got != nil {
				t.Errorf("suggestions %q came back beside the error", got)
			}
		})
	}
}

// The answers that do carry suggestions decode, in every shape that is accepted.
func TestSuggestDecodesEveryAcceptedShape(t *testing.T) {
	t.Run("struct", func(t *testing.T) {
		cases := []struct {
			name, body string
			want       []suggestAction
		}{
			{"a list", `[{"name":"a","priority":1},{"name":"b","priority":2}]`, []suggestAction{{"a", 1}, {"b", 2}}},
			{"a suggestions object", `{"suggestions":[{"name":"a","priority":1}]}`, []suggestAction{{"a", 1}}},
			{"a single suggestion", `{"name":"solo","priority":3}`, []suggestAction{{"solo", 3}}},
			{"an extra field is tolerated", `[{"name":"a","priority":1,"note":"x"}]`, []suggestAction{{"a", 1}}},
			{"fenced", "```json\n[{\"name\":\"a\",\"priority\":1}]\n```", []suggestAction{{"a", 1}}},
			{"prose around a list", "Here:\n[{\"name\":\"a\",\"priority\":1}]\nHope it helps.", []suggestAction{{"a", 1}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				swapLLMResponse(t, tc.body)
				got, err := Suggest[suggestAction]("context", NewSuggestOptions().WithTopN(5))
				if err != nil {
					t.Fatalf("Suggest: %v", err)
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("got %+v, want %+v", got, tc.want)
				}
			})
		}
	})

	t.Run("pointer to struct", func(t *testing.T) {
		swapLLMResponse(t, `[{"name":"a","priority":1}]`)
		got, err := Suggest[*suggestAction]("context", NewSuggestOptions())
		if err != nil || len(got) != 1 || got[0] == nil || got[0].Name != "a" {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("a number", func(t *testing.T) {
		swapLLMResponse(t, `[0, 1, 2]`)
		got, err := Suggest[int]("context", NewSuggestOptions())
		if err != nil || !reflect.DeepEqual(got, []int{0, 1, 2}) {
			t.Fatalf("got %v, %v: a zero is an answer for a number", got, err)
		}
	})

	t.Run("string from objects", func(t *testing.T) {
		swapLLMResponse(t, `[{"description":"d"},{"value":"v"},{"content":"c"},{"suggestion":"s"}]`)
		got, err := Suggest[string]("context", NewSuggestOptions().WithTopN(5))
		if err != nil || !reflect.DeepEqual(got, []string{"d", "v", "c", "s"}) {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

// More suggestions than asked for is refused and classified, and nothing comes
// back beside the error.
func TestSuggestRefusesMoreThanTopNWithAKind(t *testing.T) {
	swapLLMResponse(t, `[{"name":"a","priority":1},{"name":"b","priority":2},{"name":"c","priority":3}]`)
	got, err := Suggest[suggestAction]("context", NewSuggestOptions().WithTopN(2))
	if err == nil {
		t.Fatalf("three suggestions were accepted against a TopN of 2: %+v", got)
	}
	if got != nil {
		t.Errorf("suggestions %+v came back beside the error", got)
	}
	if kind := types.KindOf(err); kind != types.KindInvariantViolation {
		t.Errorf("kind = %v, want an invariant violation", kind)
	}
}

// SuggestWithResult asks the model for what Suggest asks for, and no more: it
// used to switch scores and reasons on, which it then could not read back.
func TestSuggestWithResultDoesNotAskForWhatItCannotRead(t *testing.T) {
	var steering string
	swapLLMCaller(t, func(_ context.Context, _, _ string, opts types.OpOptions) (string, error) {
		steering = opts.Steering
		return `["one","two"]`, nil
	})

	result, err := SuggestWithResult[string]("context", NewSuggestOptions().WithRanked(true))
	if err != nil {
		t.Fatalf("SuggestWithResult: %v", err)
	}
	if !reflect.DeepEqual(result.Suggestions, []string{"one", "two"}) {
		t.Errorf("Suggestions = %q", result.Suggestions)
	}
	if result.Scores != nil || result.Reasons != nil {
		t.Errorf("scores %v and reasons %v were filled from an answer that holds neither", result.Scores, result.Reasons)
	}
	if result.Metadata == nil || len(result.Metadata) != 0 {
		t.Errorf("Metadata = %v, want an empty map", result.Metadata)
	}
	for _, asked := range []string{"confidence scores", "reasoning"} {
		if strings.Contains(steering, asked) {
			t.Errorf("the model was asked for %q, which nothing reads back: %s", asked, steering)
		}
	}

	swapLLMResponse(t, `{"error":"x"}`)
	failed, err := SuggestWithResult[suggestAction]("context", NewSuggestOptions())
	if err == nil {
		t.Fatal("an answer with no suggestion was accepted")
	}
	if failed.Suggestions != nil || failed.Metadata != nil {
		t.Errorf("a result came back beside the error: %+v", failed)
	}
}

// The deadline of the options is the one the call runs under.
func TestSuggestUsesTheDeadlineOfItsOptions(t *testing.T) {
	var callDeadline, optionsDeadline time.Time
	swapLLMCaller(t, func(ctx context.Context, _, _ string, opts types.OpOptions) (string, error) {
		callDeadline, _ = ctx.Deadline()
		optionsDeadline, _ = opts.Context.Deadline()
		return `["a"]`, nil
	})
	opts := NewSuggestOptions()
	opts.Timeout = time.Minute
	if _, err := Suggest[string]("context", opts); err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if callDeadline.IsZero() || !callDeadline.Equal(optionsDeadline) {
		t.Errorf("the call runs under %v and the options hold %v", callDeadline, optionsDeadline)
	}
}

// Settings made on the embedded OpOptions reach the call. Suggest read only the
// CommonOptions half, so a pinned model or a context set there was ignored.
func TestSuggestReadsTheEmbeddedOpOptions(t *testing.T) {
	var model string
	swapLLMCaller(t, func(_ context.Context, _, _ string, opts types.OpOptions) (string, error) {
		model = opts.Model
		return `["a"]`, nil
	})

	opts := NewSuggestOptions()
	opts.OpOptions.Model = "pinned-model"
	if _, err := Suggest[string]("context", opts); err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if model != "pinned-model" {
		t.Errorf("the call went out on model %q, not the one pinned on OpOptions", model)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := NewSuggestOptions()
	cancelled.OpOptions.Context = ctx
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return `["a"]`, nil
	})
	if _, err := Suggest[string]("context", cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("a context cancelled on OpOptions did not stop the call: %v", err)
	}
}

// suggestNote has one member the model may leave out, which its type says with
// omitempty.
type suggestNote struct {
	Text string `json:"text"`
	Tag  string `json:"tag,omitempty"`
}

// A struct T is the caller's type, and a suggestion that leaves out a member of
// it is read with that member at its zero value: what a field has to carry is
// not for this operation to decide. The suggestion has to be about the type,
// which is to say share at least one member with it, and has to hold what it
// does carry in the kind the type reads.
func TestSuggestReadsAStructSuggestionAsTheCallersTypeAllows(t *testing.T) {
	cases := []struct {
		name, body string
		want       []suggestAction // nil when refused
		refusal    string
	}{
		{"a member left out", `[{"name":"a"}]`, []suggestAction{{"a", 0}}, ""},
		{"a later suggestion's member left out", `[{"name":"a","priority":1},{"priority":2}]`, []suggestAction{{"a", 1}, {"", 2}}, ""},
		{"a member that is null", `[{"name":"a","priority":null}]`, []suggestAction{{"a", 0}}, ""},
		{"a single suggestion with a member left out", `{"name":"solo"}`, []suggestAction{{"solo", 0}}, ""},
		{"a suggestions object with a member left out", `{"suggestions":[{"priority":1}]}`, []suggestAction{{"", 1}}, ""},
		{"a member that is zero, said out loud", `[{"name":"a","priority":0}]`, []suggestAction{{"a", 0}}, ""},
		{"a member of another case", `[{"NAME":"a"}]`, []suggestAction{{"a", 0}}, ""},

		{"an object that shares no member", `[{"unrelated":1}]`, nil, "suggestion 0 is not usable: the response has 1 member(s), none of them one the type asked for"},
		{"an empty object in a list", `[{"name":"a"},{}]`, nil, "suggestion 1 is not usable: the response was an empty object"},
		{"a single object that shares no member", `{"unrelated":1}`, nil, "holds no suggestion: the response has 1 member(s)"},
		{"a member of the wrong kind", `[{"name":"a","priority":"high"}]`, nil, "suggestion 0 is not usable: priority is not a whole number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapLLMResponse(t, tc.body)
			got, err := Suggest[suggestAction]("context", NewSuggestOptions().WithTopN(5))
			if tc.refusal == "" {
				if err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("got %+v, %v, want %+v", got, err, tc.want)
				}
				return
			}
			wantKind(t, err, types.KindSchemaViolation)
			if !strings.Contains(err.Error(), tc.refusal) || got != nil {
				t.Errorf("got %+v, err = %v, want it to say %q", got, err, tc.refusal)
			}
		})
	}

	swapLLMResponse(t, `[{"text":"a"}]`)
	if got, err := Suggest[suggestNote]("context", NewSuggestOptions()); err != nil || len(got) != 1 || got[0].Tag != "" {
		t.Errorf("a member the type marks omitempty, left out: %+v, %v", got, err)
	}
	swapLLMResponse(t, `[{"tag":"a"}]`)
	if got, err := Suggest[suggestNote]("context", NewSuggestOptions()); err != nil || len(got) != 1 || got[0].Text != "" || got[0].Tag != "a" {
		t.Errorf("a member the type does not mark omitempty, left out: %+v, %v", got, err)
	}
	// A type with nothing required still needs the object to be about it.
	type allOptional struct {
		A string `json:"a,omitempty"`
	}
	swapLLMResponse(t, `[{"unrelated":1}]`)
	if got, err := Suggest[allOptional]("context", NewSuggestOptions()); err == nil {
		t.Errorf("an unrelated object was read as a suggestion: %+v", got)
	}
	swapLLMResponse(t, `[{"a":"x"}]`)
	if got, err := Suggest[allOptional]("context", NewSuggestOptions()); err != nil || len(got) != 1 {
		t.Errorf("an object that is about it: %+v, %v", got, err)
	}
}

// A pointer to a struct is the same caller's type, and is read the same way.
func TestSuggestReadsAPointerToAStructSuggestionAsTheCallersTypeAllows(t *testing.T) {
	swapLLMResponse(t, `[{"name":"a"}]`)
	got, err := Suggest[*suggestAction]("context", NewSuggestOptions())
	if err != nil || len(got) != 1 || got[0] == nil || got[0].Name != "a" || got[0].Priority != 0 {
		t.Fatalf("a member left out: %+v, %v", got, err)
	}
	swapLLMResponse(t, `[{"unrelated":1}]`)
	if got, err := Suggest[*suggestAction]("context", NewSuggestOptions()); err == nil {
		t.Errorf("an unrelated object was read as a suggestion: %+v", got)
	}
}
