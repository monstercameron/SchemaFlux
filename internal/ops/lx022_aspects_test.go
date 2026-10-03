package ops

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// similarCall runs Similar against a scripted answer and returns what the
// provider was sent.
type similarCall struct {
	system, user string
	opts         types.OpOptions
}

func similarWith(t *testing.T, response string, opts SimilarOptions) (SimilarResult, error, similarCall) {
	t.Helper()
	var sent similarCall
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(_ context.Context, system, user string, opt types.OpOptions) (string, error) {
		sent = similarCall{system: system, user: user, opts: opt}
		return response, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	result, err := Similar("The flight was cancelled due to fog.", "Fog grounded the flight.", opts)
	return result, err, sent
}

// LX-022, second pass. The prompt told the model where the threshold was and
// to "place the score on the side of that line you mean", which asks for a
// verdict first and a number to match it. The same pair then scored differently
// at 0.7 and at 0.9. Go decides, so the model is not told the threshold at all,
// and the system prompt is the same bytes for every call.
func TestSimilarDoesNotTellTheModelTheThreshold(t *testing.T) {
	const answer = `{"score":0.8,"explanation":"x"}`
	var prompts []string
	for _, threshold := range []float64{0.7, 0.9, 0.123456} {
		_, err, sent := similarWith(t, answer, NewSimilarOptions().WithSimilarityThreshold(threshold))
		if err != nil {
			t.Fatalf("Similar: %v", err)
		}
		prompts = append(prompts, sent.system)
		everything := strings.ToLower(sent.system + "\n" + sent.user + "\n" + sent.opts.Steering)
		for _, leak := range []string{"threshold", "0.9", "0.123456", "side of"} {
			if strings.Contains(everything, leak) {
				t.Errorf("threshold %v: the request mentions %q", threshold, leak)
			}
		}
	}
	if prompts[0] != prompts[1] || prompts[1] != prompts[2] {
		t.Error("the system prompt differs between thresholds, so no two thresholds share a cached prefix")
	}

	// Naming aspects does not change it either: they are per-call.
	_, err, sent := similarWith(t, `{"score":0.8,"aspects":{"a1":{"score":0.9,"reason":"x"}},"explanation":"x"}`,
		NewSimilarOptions().WithAspects([]string{"tone"}))
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if sent.system != prompts[0] {
		t.Error("the system prompt differs when aspects are named")
	}
	if !strings.Contains(sent.user, `a1: "tone"`) {
		t.Errorf("the aspect is not in the per-call instructions: %q", sent.user)
	}
}

// The verdict follows the threshold and the score follows the answer.
func TestSimilarVerdictMovesWithTheThresholdAlone(t *testing.T) {
	cases := []struct {
		threshold float64
		score     string
		similar   bool
	}{
		{0.7, "0.8", true}, {0.9, "0.8", false}, {0.8, "0.8", true}, {0.800001, "0.8", false},
		{0, "0", true}, {1, "1", true}, {1, "0.999", false}, {0.5, "0.5", true}, {0.5, "0.49", false}, {0.3, "0.31", true},
	}
	for _, tc := range cases {
		result, err, _ := similarWith(t, `{"score":`+tc.score+`,"explanation":"x"}`, NewSimilarOptions().WithSimilarityThreshold(tc.threshold))
		if err != nil {
			t.Fatalf("threshold %v score %s: %v", tc.threshold, tc.score, err)
		}
		if result.IsSimilar != tc.similar {
			t.Errorf("threshold %v score %s: IsSimilar = %v, want %v", tc.threshold, tc.score, result.IsSimilar, tc.similar)
		}
	}
}

// Named aspects are declared as keys, one required entry each. A list let the
// model skip an aspect, name it twice, or rephrase it, and a rephrased name was
// a paid failure.
func TestSimilarDeclaresOneRequiredEntryPerNamedAspect(t *testing.T) {
	aspects := []string{"problem description", "underlying issue", "user intent"}
	_, err, sent := similarWith(t,
		`{"score":0.8,"aspects":{"a1":{"score":0.9,"reason":"x"},"a2":{"score":0.9,"reason":"x"},"a3":{"score":0.2,"reason":"x"}},"explanation":"x"}`,
		NewSimilarOptions().WithAspects(aspects))
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if problem := llm.StrictSchemaProblem(sent.opts.JSONSchema); problem != "" {
		t.Fatalf("the schema breaks a strict-mode rule: %s", problem)
	}
	schema := sent.opts.JSONSchema
	declared, _ := schema["properties"].(map[string]any)["aspects"].(map[string]any)
	if declared["type"] != "object" {
		t.Fatalf("aspects is declared as %v, want an object with one key per aspect", declared["type"])
	}
	properties, _ := declared["properties"].(map[string]any)
	if len(properties) != len(aspects) {
		t.Fatalf("aspects declares %d entries, want %d", len(properties), len(aspects))
	}
	for i, name := range aspects {
		entry, _ := properties[similarAspectKey(i)].(map[string]any)
		if _, described := entry["description"]; entry == nil || described {
			t.Errorf("entry %s = %v, want one with no description: the aspect %q is in the request, not in the schema", similarAspectKey(i), entry, name)
		}
		if !isRequiredNonNull(declared, similarAspectKey(i)) {
			t.Errorf("entry %s may be left out", similarAspectKey(i))
		}
	}
	if !isRequiredNonNull(sent.opts.JSONSchema, "aspects") {
		t.Error("aspects may be null although aspects were named")
	}

	// With no aspects named the model is not asked for any.
	_, err, sent = similarWith(t, `{"score":0.8,"explanation":"x"}`, NewSimilarOptions())
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	schema = sent.opts.JSONSchema
	if _, asked := schema["properties"].(map[string]any)["aspects"]; asked {
		t.Error("aspects are asked for although none were named")
	}
}

// Which list an aspect goes in is decided in Go from its score, in the order
// the caller named them, under the caller's spelling.
func TestSimilarSortsAspectsByTheirScore(t *testing.T) {
	cases := []struct {
		name      string
		aspects   []string
		threshold float64
		response  string
		matched   []string
		differing []string
	}{
		{"keyed by position", []string{"meaning", "tone"}, 0.7,
			`{"score":0.9,"aspects":{"a1":{"score":0.95,"reason":"same"},"a2":{"score":0.4,"reason":"formal"}},"explanation":"x"}`,
			[]string{"meaning"}, []string{"tone"}},
		{"keys out of order", []string{"meaning", "tone"}, 0.7,
			`{"score":0.9,"aspects":{"a2":{"score":0.4,"reason":"formal"},"a1":{"score":0.95,"reason":"same"}},"explanation":"x"}`,
			[]string{"meaning"}, []string{"tone"}},
		{"keyed by name", []string{"meaning", "tone"}, 0.7,
			`{"score":0.9,"aspects":{"Tone":{"score":0.4},"meaning":{"score":0.95}},"explanation":"x"}`,
			[]string{"meaning"}, []string{"tone"}},
		{"a list of named aspects", []string{"meaning", "tone"}, 0.7,
			`{"score":0.9,"aspects":[{"aspect":"tone","score":0.4},{"aspect":"meaning","score":0.95}],"explanation":"x"}`,
			[]string{"meaning"}, []string{"tone"}},
		{"the model's own two lists", []string{"meaning", "tone"}, 0.7,
			`{"score":0.9,"matched_aspects":[{"aspect":"meaning","score":0.95}],"differing_aspects":[{"aspect":"tone","score":0.4}],"explanation":"x"}`,
			[]string{"meaning"}, []string{"tone"}},
		{"a low score the model listed as matched", []string{"meaning", "tone"}, 0.7,
			`{"score":0.9,"matched_aspects":[{"aspect":"meaning","score":0.95},{"aspect":"tone","score":0.1}],"differing_aspects":[],"explanation":"x"}`,
			[]string{"meaning"}, []string{"tone"}},
		{"a high score the model listed as differing", []string{"meaning"}, 0.7,
			`{"score":0.9,"matched_aspects":[],"differing_aspects":[{"aspect":"meaning","score":0.95}],"explanation":"x"}`,
			[]string{"meaning"}, nil},
		{"the threshold decides", []string{"meaning", "tone"}, 0.96,
			`{"score":0.9,"aspects":{"a1":{"score":0.95},"a2":{"score":0.97}},"explanation":"x"}`,
			[]string{"tone"}, []string{"meaning"}},
		{"at the threshold is matched", []string{"tone"}, 0.7,
			`{"score":0.9,"aspects":{"a1":{"score":0.7}},"explanation":"x"}`, []string{"tone"}, nil},
		{"the caller's spelling is kept", []string{"User Intent"}, 0.7,
			`{"score":0.9,"aspects":[{"aspect":" user intent ","score":0.9}],"explanation":"x"}`, []string{"User Intent"}, nil},
		{"an aspect nobody asked for is left out", []string{"tone"}, 0.7,
			`{"score":0.9,"aspects":[{"aspect":"tone","score":0.9},{"aspect":"length","score":0.9}],"explanation":"x"}`, []string{"tone"}, nil},
		{"the same aspect twice with one score", []string{"tone"}, 0.7,
			`{"score":0.9,"matched_aspects":[{"aspect":"tone","score":0.9}],"aspects":{"a1":{"score":0.9}},"explanation":"x"}`, []string{"tone"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := similarWith(t, tc.response, NewSimilarOptions().WithAspects(tc.aspects).WithSimilarityThreshold(tc.threshold))
			if err != nil {
				t.Fatalf("Similar: %v", err)
			}
			if got := aspectNames(result.MatchedAspects); strings.Join(got, "|") != strings.Join(tc.matched, "|") {
				t.Errorf("MatchedAspects = %v, want %v", got, tc.matched)
			}
			if got := aspectNames(result.DifferingAspects); strings.Join(got, "|") != strings.Join(tc.differing, "|") {
				t.Errorf("DifferingAspects = %v, want %v", got, tc.differing)
			}
		})
	}
}

func aspectNames(aspects []AspectMatch) []string {
	var names []string
	for _, aspect := range aspects {
		names = append(names, aspect.Aspect)
	}
	return names
}

// An answer that does not score every named aspect exactly once is refused, and
// so are options that could not be answered.
func TestSimilarRefusesAnIncompleteAspectAnswer(t *testing.T) {
	cases := []struct {
		name     string
		aspects  []string
		response string
		want     types.ErrorKind
	}{
		{"a key is missing", []string{"meaning", "tone"}, `{"score":0.9,"aspects":{"a1":{"score":0.9}},"explanation":"x"}`, types.KindInvariantViolation},
		{"aspects is null", []string{"tone"}, `{"score":0.9,"aspects":null,"explanation":"x"}`, types.KindInvariantViolation},
		{"aspects is absent", []string{"tone"}, `{"score":0.9,"explanation":"x"}`, types.KindInvariantViolation},
		{"aspects is an empty object", []string{"tone"}, `{"score":0.9,"aspects":{},"explanation":"x"}`, types.KindInvariantViolation},
		{"aspects is a string", []string{"tone"}, `{"score":0.9,"aspects":"tone is the same","explanation":"x"}`, types.KindSchemaViolation},
		{"an entry with no score", []string{"tone"}, `{"score":0.9,"aspects":{"a1":{"reason":"same"}},"explanation":"x"}`, types.KindSchemaViolation},
		{"an entry with a null score", []string{"tone"}, `{"score":0.9,"aspects":{"a1":{"score":null}},"explanation":"x"}`, types.KindSchemaViolation},
		{"an entry off the scale", []string{"tone"}, `{"score":0.9,"aspects":{"a1":{"score":1.5}},"explanation":"x"}`, types.KindInvariantViolation},
		{"a negative entry", []string{"tone"}, `{"score":0.9,"aspects":{"a1":{"score":-0.1}},"explanation":"x"}`, types.KindInvariantViolation},
		{"the same aspect with two scores", []string{"tone"},
			`{"score":0.9,"matched_aspects":[{"aspect":"tone","score":0.9}],"differing_aspects":[{"aspect":"tone","score":0.2}],"explanation":"x"}`, types.KindInvariantViolation},
		{"a rephrased name in a list", []string{"user intent"}, `{"score":0.9,"aspects":[{"aspect":"intent of the user","score":0.9}],"explanation":"x"}`, types.KindInvariantViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err, _ := similarWith(t, tc.response, NewSimilarOptions().WithAspects(tc.aspects))
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if got := types.KindOf(err); got != tc.want {
				t.Errorf("kind = %v, want %v (%v)", got, tc.want, err)
			}
			if result.IsSimilar || result.Score != 0 || len(result.MatchedAspects)+len(result.DifferingAspects) != 0 {
				t.Errorf("a failed call returned a usable result: %+v", result)
			}
		})
	}
}

func TestSimilarOptionsRefuseAspectsThatCannotBeAnswered(t *testing.T) {
	cases := []struct {
		name    string
		aspects []string
		wantErr bool
	}{
		{"none", nil, false},
		{"one", []string{"tone"}, false},
		{"several", []string{"tone", "meaning", "length"}, false},
		{"a blank name", []string{"tone", " "}, true},
		{"an empty name", []string{""}, true},
		{"the same name twice", []string{"tone", "tone"}, true},
		{"the same name in another case", []string{"tone", "Tone"}, true},
		{"the same name with padding", []string{"tone", " tone "}, true},
		{"names that only look alike", []string{"tone", "tones"}, false},
		{"a name shaped like an answer key", []string{"a1"}, true},
		{"an answer key in capitals", []string{"tone", "A2"}, true},
		{"an answer key with two digits", []string{"a10"}, true},
		{"a word that starts with a", []string{"area", "a", "a1b", "audience 2"}, false},
		{"a name with punctuation", []string{"cost/benefit", "who, what, when"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := NewSimilarOptions().WithAspects(tc.aspects).Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate = %v, want an error: %v", err, tc.wantErr)
			}
		})
	}

	// Refused before anything is sent.
	called := false
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		called = true
		return `{"score":0.9,"explanation":"x"}`, nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	if _, err := Similar("a", "b", NewSimilarOptions().WithAspects([]string{"tone", "tone"})); err == nil || called {
		t.Fatalf("err = %v, provider called = %v", err, called)
	}
}

// The aspects are this library's instruction, so they go in the request as
// that, one to a line and quoted. They travelled as steering, which is the
// caller's text and is marked to the model as untrusted: the library's own key
// mapping was presented as something to be wary of, and joined to the caller's
// words with a separator. A name with ";" in it read as two aspects.
func TestSimilarListsAspectsInTheRequestNotInTheSteering(t *testing.T) {
	const answer = `{"score":0.8,"aspects":{"a1":{"score":0.9},"a2":{"score":0.9},"a3":{"score":0.9}},"explanation":"x"}`
	aspects := []string{"tone; a2 = length", `the "voice" used`, "who, what = when"}

	opts := NewSimilarOptions().WithAspects(aspects)
	opts.OpOptions.Steering = "Compare as a copy editor would"
	_, err, sent := similarWith(t, answer, opts)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}

	if sent.opts.Steering != "Compare as a copy editor would" {
		t.Errorf("the caller's steering was changed: %q", sent.opts.Steering)
	}
	for _, line := range []string{
		`a1: "tone; a2 = length"` + "\n",
		`a2: "the \"voice\" used"` + "\n",
		`a3: "who, what = when"` + "\n",
	} {
		if !strings.Contains(sent.user, line) {
			t.Errorf("the request has no line %q:\n%s", line, sent.user)
		}
	}
	if strings.Count(sent.user, "\na2: ")+strings.Count(sent.user, "\na3: ") != 2 || !strings.HasPrefix(sent.user, "Aspects to score") {
		t.Errorf("the aspect lines are not one per key:\n%s", sent.user)
	}
	// The items still close the message: everything this library writes ends
	// with the data.
	if !strings.HasSuffix(sent.user, `"Fog grounded the flight."`) {
		t.Errorf("the request does not end with the second item:\n%s", sent.user)
	}

	// With no aspects the request is the comparison alone.
	_, err, sent = similarWith(t, `{"score":0.8,"explanation":"x"}`, NewSimilarOptions())
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if !strings.HasPrefix(sent.user, "Compare these items") || strings.Contains(sent.user, "Aspects") {
		t.Errorf("a request with no aspects mentions them:\n%s", sent.user)
	}
}

// The scale in the prompt is about meaning. An aspect such as tone is not, and
// the prompt says an aspect is judged in its own terms.
func TestSimilarPromptScoresAnAspectInItsOwnTerms(t *testing.T) {
	for _, phrase := range []string{
		"The overall score judges meaning, not wording",
		"An aspect is judged in its own terms",
		"whatever the overall score is",
	} {
		if !strings.Contains(similarSystemPrompt, phrase) {
			t.Errorf("the prompt does not say %q", phrase)
		}
	}
}

// The explanation is required by the schema and read by callers. An answer
// without one was accepted from a provider that does not enforce the schema.
func TestSimilarRefusesAnAnswerWithNoExplanation(t *testing.T) {
	for _, response := range []string{
		`{"score":0.8}`,
		`{"score":0.8,"explanation":""}`,
		`{"score":0.8,"explanation":"   "}`,
		`{"score":0.8,"explanation":null}`,
	} {
		result, err, _ := similarWith(t, response, NewSimilarOptions())
		if types.KindOf(err) != types.KindSchemaViolation {
			t.Errorf("%s: err = %v, want a schema violation", response, err)
		}
		if result.IsSimilar || result.Score != 0 {
			t.Errorf("%s: a failed call returned %+v", response, result)
		}
	}
}

// Every comparison with NaN is false, so a NaN threshold passed the range test
// and then made nothing similar.
func TestSimilarRefusesAThresholdThatIsNotANumber(t *testing.T) {
	for _, threshold := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01, 1.01} {
		if err := NewSimilarOptions().WithSimilarityThreshold(threshold).Validate(); err == nil {
			t.Errorf("threshold %v was accepted", threshold)
		}
	}
	for _, threshold := range []float64{0, 0.5, 1} {
		if err := NewSimilarOptions().WithSimilarityThreshold(threshold).Validate(); err != nil {
			t.Errorf("threshold %v was refused: %v", threshold, err)
		}
	}
}
