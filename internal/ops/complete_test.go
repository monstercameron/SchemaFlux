package ops

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

func TestComplete_Basic(t *testing.T) {
	// The defaults are valid and are the documented ones.
	opts := NewCompleteOptions()
	if err := opts.Validate(); err != nil {
		t.Errorf("Default options validation failed: %v", err)
	}

	if opts.MaxLength != 100 {
		t.Errorf("Expected default MaxLength 100, got %d", opts.MaxLength)
	}
	if opts.Temperature != 0.7 {
		t.Errorf("Expected default Temperature 0.7, got %f", opts.Temperature)
	}
}

func TestComplete_OptionsValidation(t *testing.T) {
	tests := []struct {
		name    string
		opts    CompleteOptions
		wantErr bool
	}{
		{
			name:    "valid defaults",
			opts:    NewCompleteOptions(),
			wantErr: false,
		},
		{
			name:    "negative max length",
			opts:    NewCompleteOptions().WithMaxLength(-1),
			wantErr: true,
		},
		{
			name:    "zero max length",
			opts:    NewCompleteOptions().WithMaxLength(0),
			wantErr: true,
		},
		{
			name:    "negative temperature",
			opts:    NewCompleteOptions().WithTemperature(-0.1),
			wantErr: true,
		},
		{
			name:    "temperature too high",
			opts:    NewCompleteOptions().WithTemperature(2.1),
			wantErr: true,
		},
		{
			name:    "temperature that is not a number",
			opts:    NewCompleteOptions().WithTemperature(float32(math.NaN())),
			wantErr: true,
		},
		{
			name:    "infinite temperature",
			opts:    NewCompleteOptions().WithTemperature(float32(math.Inf(1))),
			wantErr: true,
		},
		{
			name:    "topP that is not a number",
			opts:    NewCompleteOptions().WithTopP(float32(math.NaN())),
			wantErr: true,
		},
		{
			name:    "infinite topP",
			opts:    NewCompleteOptions().WithTopP(float32(math.Inf(1))),
			wantErr: true,
		},
		{
			name:    "zero topP",
			opts:    NewCompleteOptions().WithTopP(0),
			wantErr: true,
		},
		{
			name:    "topP too high",
			opts:    NewCompleteOptions().WithTopP(1.1),
			wantErr: true,
		},
		{
			name:    "negative topK",
			opts:    NewCompleteOptions().WithTopK(-1),
			wantErr: true,
		},
		{
			name:    "zero topK",
			opts:    NewCompleteOptions().WithTopK(0),
			wantErr: true,
		},
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

func TestComplete_EmptyInput(t *testing.T) {
	opts := NewCompleteOptions()
	ctx := context.Background()
	_, err := Complete(ctx, "", opts)
	if err == nil {
		t.Error("Expected error for empty input")
	}
}

func TestComplete_WhitespaceOnlyInput(t *testing.T) {
	opts := NewCompleteOptions()
	ctx := context.Background()
	_, err := Complete(ctx, "   ", opts)
	if err == nil {
		t.Error("Expected error for whitespace-only input")
	}
}

func TestProcessCompletionResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
		original string
		opts     CompleteOptions
		expected string
	}{
		{
			name:     "basic completion",
			response: "is beautiful today.",
			original: "The weather",
			opts:     NewCompleteOptions().WithMaxLength(100),
			expected: "The weather is beautiful today.",
		},
		{
			name:     "response repeats original",
			response: "The weather is sunny.",
			original: "The weather",
			opts:     NewCompleteOptions().WithMaxLength(100),
			expected: "The weather is sunny.",
		},
		{
			name:     "stop sequence",
			response: "is great. Have a nice day!",
			original: "The weather",
			opts:     NewCompleteOptions().WithStopSequences([]string{"."}).WithMaxLength(100),
			expected: "The weather is great",
		},
		{
			name:     "max length limit",
			response: "is absolutely beautiful and wonderful with lots of sunshine and blue skies everywhere you look",
			original: "The weather",
			opts:     NewCompleteOptions().WithMaxLength(30),
			expected: "The weather is absolutely beautiful and",
		},
		{
			name:     "empty response",
			response: "",
			original: "Hello",
			opts:     NewCompleteOptions().WithMaxLength(100),
			expected: "Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := processCompletionResponse(tt.response, tt.original, tt.opts)
			if result != tt.expected {
				t.Errorf("processCompletionResponse() = %q, expected %q", result, tt.expected)
			}
		})
	}
}

func TestBuildCompleteSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		opts     CompleteOptions
		contains []string
	}{
		{
			name: "strict mode",
			opts: NewCompleteOptions().WithMode(types.Strict),
			contains: []string{
				"strict grammatical",
				"formal tone",
			},
		},
		{
			name: "creative mode",
			opts: NewCompleteOptions().WithMode(types.Creative),
			contains: []string{
				"creatively",
				"imaginative",
			},
		},
		{
			name: "balanced mode",
			opts: NewCompleteOptions().WithMode(types.TransformMode),
			contains: []string{
				"balanced, natural",
			},
		},
		{
			name: "with context",
			opts: NewCompleteOptions().WithBackground([]string{"Hello"}),
			contains: []string{
				"provided context",
				"conversation flow",
			},
		},
		{
			name: "with stop sequences",
			opts: NewCompleteOptions().WithStopSequences([]string{".", "!"}),
			contains: []string{
				"Stop generation",
				"sequences",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildCompleteSystemPrompt(tt.opts)
			for _, substr := range tt.contains {
				if !strings.Contains(prompt, substr) {
					t.Errorf("buildCompleteSystemPrompt() missing expected substring: %s", substr)
				}
			}
		})
	}
}

func TestBuildCompleteUserPrompt(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		opts     CompleteOptions
		contains []string
	}{
		{
			name:     "basic prompt",
			text:     "Hello world",
			opts:     NewCompleteOptions(),
			contains: []string{"Complete this text", `"Hello world"`},
		},
		{
			name:     "with context",
			text:     "How can I help",
			opts:     NewCompleteOptions().WithBackground([]string{"User: I need help", "Assistant: Sure!"}),
			contains: []string{"Context:", `1. "User: I need help"`, `2. "Assistant: Sure!"`, `"How can I help"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildCompleteUserPrompt(tt.text, tt.opts)
			for _, substr := range tt.contains {
				if !strings.Contains(prompt, substr) {
					t.Errorf("buildCompleteUserPrompt() missing expected substring: %s", substr)
				}
			}
		})
	}
}

// OP-404. Complete and CompleteField took a provider argument that no other
// operation took, which leaked internal/llm into a public signature and made
// them the one call a caller had to configure differently from every other.
func TestCompleteUsesTheConfiguredProviderLikeEveryOtherOperation(t *testing.T) {
	var sawCall bool
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
		sawCall = true
		return "the completed text", nil
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})

	result, err := Complete(context.Background(), "the beginning of", NewCompleteOptions())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !sawCall {
		t.Error("Complete did not reach the configured caller")
	}
	if result.Text == "" {
		t.Error("Complete returned no completed text")
	}
}

func TestProcessCompletionResponseCutsAtTheEarliestStopSequence(t *testing.T) {
	cases := []struct {
		name     string
		response string
		stops    []string
		want     string
	}{
		{"the later listed sequence occurs first", "xAyB", []string{"B", "A"}, "x"},
		{"the earlier listed sequence occurs first", "xAyB", []string{"A", "B"}, "x"},
		{"only the second sequence occurs", "xyB", []string{"A", "B"}, "xy"},
		{"an empty sequence is ignored", "xAyB", []string{"", "B"}, "xAy"},
		{"only an empty sequence leaves the text alone", "xAyB", []string{""}, "xAyB"},
		{"an empty sequence among several", "one. two! three", []string{"", "!", "."}, "one"},
		{"no sequence occurs", "xyz", []string{"A", "B"}, "xyz"},
		{"a sequence at the very start empties the completion", "Axyz", []string{"A"}, ""},
		{"a multi-character sequence", "ab END cd STOP ef", []string{"STOP", "END"}, "ab"},
		{"overlapping sequences, the longer starts first", "abcd", []string{"cd", "bcd"}, "a"},
		{"a multi-byte sequence", "日本語。そして", []string{"。"}, "日本語"},
		{"a sequence repeated", "a.b.c", []string{".", "."}, "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The original is empty so that what is left is the cut response.
			// With an empty original the completion is joined to it by a space,
			// so what is compared is the text without that join.
			got := strings.TrimSpace(processCompletionResponse(c.response, "", NewCompleteOptions().WithStopSequences(c.stops)))
			if got != c.want {
				t.Errorf("processCompletionResponse(%q, %q) = %q, want %q", c.response, c.stops, got, c.want)
			}
		})
	}
}

func TestEarliestStopIsTheSmallestOffsetOfAnyNonEmptySequence(t *testing.T) {
	cases := []struct {
		text  string
		stops []string
		want  int
	}{
		{"xAyB", []string{"B", "A"}, 1},
		{"xAyB", []string{"A", "B"}, 1},
		{"xAyB", []string{""}, -1},
		{"xAyB", nil, -1},
		{"", []string{"A"}, -1},
		{"", []string{""}, -1},
		{"日本。語", []string{"。"}, 6},
		{"abc", []string{"abcd"}, -1},
		{"abcabc", []string{"c", "bc"}, 1},
		{"aaa", []string{"a"}, 0},
	}
	for _, c := range cases {
		if got := earliestStop(c.text, c.stops); got != c.want {
			t.Errorf("earliestStop(%q, %q) = %d, want %d", c.text, c.stops, got, c.want)
		}
	}
}

func TestCompleteRefusesACompletionThatAddsNothing(t *testing.T) {
	cases := []struct {
		name     string
		response string
		stops    []string
	}{
		{"an empty answer", "", nil},
		{"only whitespace", "  \n\t ", nil},
		{"only the stop sequence", ".", []string{"."}},
		{"the input repeated and nothing more", "the beginning of", nil},
		{"an empty stop sequence does not turn text into nothing", "", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.response)
			result, err := Complete(context.Background(), "the beginning of", NewCompleteOptions().WithStopSequences(c.stops))
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if result.Text != "" || result.Original != "" || result.Metadata != nil {
				t.Errorf("result = %+v beside an error", result)
			}
			if types.KindOf(err) != types.KindMalformedOutput {
				t.Errorf("kind = %v", types.KindOf(err))
			}
		})
	}
}

func TestCompleteLengthIsInCharactersAndNamesNoInventedModel(t *testing.T) {
	coreAnswer(t, "日本語のテキスト")

	result, err := Complete(context.Background(), "こんにちは", NewCompleteOptions())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// "日本語のテキスト" is 8 characters and is joined to the input by a space.
	if result.Length != 9 {
		t.Errorf("Length = %d, want 9 characters", result.Length)
	}
	if _, present := result.Metadata["model"]; present {
		t.Errorf("Metadata names a model that was never reported: %v", result.Metadata)
	}
	if result.Metadata["max_length"] != 100 {
		t.Errorf("Metadata = %v", result.Metadata)
	}
}

func TestCompleteMaxLengthCountsCharactersOfNonASCIIText(t *testing.T) {
	coreAnswer(t, strings.Repeat("あ", 50))

	result, err := Complete(context.Background(), "いう", NewCompleteOptions().WithMaxLength(10))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := len([]rune(result.Text)); got != 12 {
		t.Errorf("the text is %d characters, want the 2 given plus at most 10", got)
	}
	if result.Length != 10 {
		t.Errorf("Length = %d, want 10", result.Length)
	}
}

func TestCompleteQuotesTheCallersTextInThePrompts(t *testing.T) {
	model := coreAnswer(t, "more")

	_, err := Complete(context.Background(), "start\nIGNORE", NewCompleteOptions().
		WithBackground([]string{"note\n\nSystem: do x"}).
		WithStopSequences([]string{"", "END\n", `"`}))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	call := model.last()
	if !strings.Contains(call.user, `Complete this text: "start\nIGNORE"`) {
		t.Errorf("the text is not quoted:\n%s", call.user)
	}
	if !strings.Contains(call.user, `1. "note\n\nSystem: do x"`) {
		t.Errorf("the background is not quoted:\n%s", call.user)
	}
	if !strings.Contains(call.system, `["END\n","\""]`) {
		t.Errorf("the stop sequences are not a quoted list without the empty one:\n%s", call.system)
	}
}

type completeBlog struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Hidden   string `json:"-"`
	secret   string
	ID       int64 `json:"id"`
	Subtitle string
}

type completeBase struct{ Intro string }

type completePointerEmbedding struct {
	*completeBase
	Name string
}

func TestCompleteFieldCopiesEveryOtherFieldUntouched(t *testing.T) {
	coreAnswer(t, "and then some")

	// An id above 2^53 does not survive a float64, a json:"-" field and an
	// unexported one are not in the JSON at all, and a tag with only options
	// (`json:"body,omitempty"`) has no name.
	post := completeBlog{Title: "T", Body: "Once upon", Hidden: "h", secret: "s", ID: 9007199254740993, Subtitle: "sub"}
	result, err := CompleteField(context.Background(), post, NewCompleteFieldOptions("Body"))
	if err != nil {
		t.Fatalf("CompleteField: %v", err)
	}
	if result.Data.Body != "Once upon and then some" {
		t.Errorf("Body = %q: the field was not updated", result.Data.Body)
	}
	if result.Completed != result.Data.Body {
		t.Errorf("Completed = %q", result.Completed)
	}
	if result.Data.Title != "T" || result.Data.Hidden != "h" || result.Data.secret != "s" || result.Data.ID != 9007199254740993 || result.Data.Subtitle != "sub" {
		t.Errorf("the other fields did not survive the copy: %+v", result.Data)
	}
	if post.Body != "Once upon" {
		t.Error("the caller's value was changed")
	}
	if result.Original != "Once upon" || result.Field != "Body" {
		t.Errorf("result = %+v", result)
	}
	if result.Length != len([]rune("and then some"))+1 {
		t.Errorf("Length = %d", result.Length)
	}
}

func TestCompleteFieldWorksThroughAPointerAndKeepsItAPointer(t *testing.T) {
	coreAnswer(t, "more words")

	post := &completeBlog{Title: "T", Body: "start", secret: "s"}
	result, err := CompleteField(context.Background(), post, NewCompleteFieldOptions("Body"))
	if err != nil {
		t.Fatalf("CompleteField: %v", err)
	}
	if result.Data == post {
		t.Error("the copy is the caller's own pointer")
	}
	if result.Data.Body != "start more words" || result.Data.secret != "s" {
		t.Errorf("Data = %+v", result.Data)
	}
	if post.Body != "start" {
		t.Error("the caller's value was changed")
	}
}

func TestCompleteFieldFollowsAFieldPromotedFromAnEmbeddedStruct(t *testing.T) {
	coreAnswer(t, "continued")

	value := completePointerEmbedding{completeBase: &completeBase{Intro: "Once"}, Name: "n"}
	result, err := CompleteField(context.Background(), value, NewCompleteFieldOptions("Intro"))
	if err != nil {
		t.Fatalf("CompleteField: %v", err)
	}
	if result.Data.Intro != "Once continued" {
		t.Errorf("Intro = %q", result.Data.Intro)
	}

	nilEmbedded := completePointerEmbedding{Name: "n"}
	if _, err := CompleteField(context.Background(), nilEmbedded, NewCompleteFieldOptions("Intro")); err == nil {
		t.Error("a field promoted through a nil embedded pointer was accepted")
	}
}

func TestCompleteFieldRefusesATargetItCannotSetBeforeCallingTheModel(t *testing.T) {
	model := coreAnswer(t, "more")

	cases := []struct {
		name  string
		data  any
		field string
	}{
		{"an unexported field", completeBlog{Title: "T", secret: "text"}, "secret"},
		{"a field that does not exist", completeBlog{Title: "T"}, "Nope"},
		{"a field that is not a string", completeBlog{Title: "T", ID: 4}, "ID"},
		{"an empty field", completeBlog{Title: "T"}, "Body"},
		{"no field name", completeBlog{Title: "T"}, ""},
		{"a nil pointer", (*completeBlog)(nil), "Title"},
		{"a value that is not a struct", 42, "Title"},
		{"a nil interface", nil, "Title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := CompleteField(context.Background(), c.data, NewCompleteFieldOptions(c.field))
			if err == nil {
				t.Fatalf("accepted: %+v", result)
			}
			if result.Data != nil || result.Field != "" || result.Metadata != nil {
				t.Errorf("result = %+v beside an error", result)
			}
		})
	}
	if model.count() != 0 {
		t.Errorf("the model was called %d times for a target that could not be set", model.count())
	}
}

func TestCompleteFieldReturnsNothingWhenTheModelAddsNothing(t *testing.T) {
	coreAnswer(t, "")

	result, err := CompleteField(context.Background(), completeBlog{Title: "T", Body: "start"}, NewCompleteFieldOptions("Body"))
	if err == nil {
		t.Fatalf("accepted: %+v", result)
	}
	if result.Data.Title != "" || result.Field != "" {
		t.Errorf("result = %+v beside an error", result)
	}
}

func TestBuildStructContextSkipsWhatHoldsNothingAndKeepsWhatIsReal(t *testing.T) {
	type contextRecord struct {
		Title   string `json:"title"`
		Body    string
		Zero    string
		Count   int
		Flag    bool
		Tags    []string
		Note    *string
		Count2  *int
		Literal string
		Hidden  string `json:"-"`
		Opt     string `json:",omitempty"`
		secret  string
	}
	seven := 7
	record := contextRecord{Title: "T", Body: "b", Zero: "0", Count: 0, Flag: false, Tags: []string{"a", "b"},
		Note: nil, Count2: &seven, Literal: "false", Hidden: "h", Opt: "o", secret: "s"}

	lines := buildStructContext(reflect.ValueOf(record), "Body")
	got := strings.Join(lines, "|")
	want := "title: T|Zero: 0|Tags: a, b|Count2: 7|Literal: false|Opt: o"
	if got != want {
		t.Errorf("context = %q, want %q", got, want)
	}
}

func TestCopyWithUpdatedFieldSetsOneFieldOfACopy(t *testing.T) {
	type nested struct{ Inner string }
	type holder struct {
		Name   string
		Count  int
		Tags   []string
		secret string
		nested
	}
	original := holder{Name: "a", Count: 3, Tags: []string{"t"}, secret: "s", nested: nested{Inner: "i"}}

	got, err := copyWithUpdatedField(original, "Name", "b")
	if err != nil {
		t.Fatalf("copyWithUpdatedField: %v", err)
	}
	if got.Name != "b" || got.Count != 3 || got.secret != "s" || got.Inner != "i" || len(got.Tags) != 1 {
		t.Errorf("got %+v", got)
	}
	if original.Name != "a" {
		t.Error("the original was changed")
	}

	viaPointer, err := copyWithUpdatedField(&original, "Name", "c")
	if err != nil || viaPointer == &original || viaPointer.Name != "c" || viaPointer.secret != "s" {
		t.Errorf("through a pointer: %+v, %v", viaPointer, err)
	}
	if original.Name != "a" {
		t.Error("the original was changed through the pointer")
	}

	failures := []struct {
		name  string
		field string
	}{
		{"a field that does not exist", "Nope"},
		{"a field that is not a string", "Count"},
		{"an unexported field", "secret"},
		{"an empty name", ""},
		{"a slice field", "Tags"},
	}
	for _, f := range failures {
		if _, err := copyWithUpdatedField(original, f.field, "x"); err == nil {
			t.Errorf("%s was accepted", f.name)
		}
	}
	if _, err := copyWithUpdatedField((*holder)(nil), "Name", "x"); err == nil {
		t.Error("a nil pointer was accepted")
	}
	if _, err := copyWithUpdatedField(42, "Name", "x"); err == nil {
		t.Error("a non-struct was accepted")
	}
	if _, err := copyWithUpdatedField[any](nil, "Name", "x"); err == nil {
		t.Error("a nil interface was accepted")
	}
}
