package ops

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// LAT-006. The request for a decision is built from what the questions decide
// once, and from the subject on every call. The bytes it comes to are the bytes
// it came to before, which is what the tests below hold it to.

// dec12ReferencePrompt is the message as it was written before LAT-006: every
// question and option through the encoder, on every call. It is kept here as
// the oracle the faster writer is compared against.
func dec12ReferencePrompt(subject string, questions []assessQuestion, steering string, confidence bool) (string, error) {
	quoted := func(value any) (string, error) {
		raw, err := promptJSON(value)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}

	var message strings.Builder
	message.WriteString("Questions:\n")
	for i, q := range questions {
		text, err := quoted(q.ask)
		if err != nil {
			return "", err
		}
		message.WriteString(assessKey(i))
		switch q.kind {
		case questionChoice:
			message.WriteString(" (one of the options below): " + text + "\n")
			for n, label := range q.labels {
				option, err := quoted(label)
				if err != nil {
					return "", err
				}
				message.WriteString("  " + strconv.Itoa(n+1) + ": " + option + "\n")
			}
		case questionRating:
			message.WriteString(" (a rating from " + strconv.Itoa(q.low) + " to " + strconv.Itoa(q.high) + "): " + text + "\n")
		default:
			message.WriteString(" (yes or no): " + text + "\n")
		}
	}
	if strings.TrimSpace(steering) != "" {
		text, err := quoted(steering)
		if err != nil {
			return "", err
		}
		message.WriteString("\nGuidance: " + text + "\n")
	}
	if confidence {
		message.WriteString("\nAlso give \"confidence\": how sure you are of these answers taken together, a number from 0 to 1.\n")
	}

	message.WriteString("\nSubject:\n" + subject)
	return message.String(), nil
}

// dec12AwkwardTexts are strings the writer has to get exactly as the encoder
// does: nothing to escape, a quote, a backslash, controls, markup, characters
// outside ASCII, bytes that are not UTF-8, and the two line separators JSON
// always escapes.
var dec12AwkwardTexts = []string{
	"",
	"Is this message spam?",
	"a",
	`say "yes"`,
	`C:\temp\file`,
	"two\nlines",
	"tab\there",
	"carriage\rreturn",
	"bell\a and backspace\b and form feed\f",
	"nul\x00byte",
	"R&D <b>bold</b> > less",
	"delete \x7f character",
	"accents: é à ü ñ",
	"Japanese: お客様",
	"emoji: 😀",
	"line separator \u2028 and paragraph separator \u2029",
	"invalid \xff\xfe bytes",
	"truncated \xe3\x81",
	"a very long question " + strings.Repeat("that goes on and on ", 40),
	" leading and trailing space ",
}

// The writer's output is the encoder's output for every string, byte for byte.
func TestDec12QuoteWritesWhatTheEncoderWrites(t *testing.T) {
	for _, text := range dec12AwkwardTexts {
		t.Run(strconv.Quote(text[:min(len(text), 24)]), func(t *testing.T) {
			var out strings.Builder
			if err := dec12Quote(&out, text); err != nil {
				t.Fatalf("dec12Quote: %v", err)
			}
			want, err := promptJSON(text)
			if err != nil {
				t.Fatalf("promptJSON: %v", err)
			}
			if out.String() != string(want) {
				t.Errorf("dec12Quote = %q, the encoder writes %q", out.String(), want)
			}
		})
	}

	// Every single character the encoder treats in a way of its own, and the
	// bytes that are not characters.
	t.Run("every character up to U+2100", func(t *testing.T) {
		for r := rune(0); r <= 0x2100; r++ {
			text := "x" + string(r) + "y"
			var out strings.Builder
			if err := dec12Quote(&out, text); err != nil {
				t.Fatalf("U+%04X: %v", r, err)
			}
			want, _ := promptJSON(text)
			if out.String() != string(want) {
				t.Fatalf("U+%04X: dec12Quote = %q, the encoder writes %q", r, out.String(), want)
			}
		}
	})
	t.Run("every byte alone", func(t *testing.T) {
		for b := 0; b < 256; b++ {
			text := "x" + string([]byte{byte(b)}) + "y"
			var out strings.Builder
			if err := dec12Quote(&out, text); err != nil {
				t.Fatalf("byte %#x: %v", b, err)
			}
			want, _ := promptJSON(text)
			if out.String() != string(want) {
				t.Fatalf("byte %#x (valid UTF-8: %v): dec12Quote = %q, the encoder writes %q", b, utf8.ValidString(text), out.String(), want)
			}
		}
	})
}

type dec12Plain struct {
	Name  string `json:"name"`
	Notes string `json:"notes"`
	Count int    `json:"count"`
}

// A subject is written as promptJSON writes it: markup characters as they are,
// and no newline at the end.
func TestDec12MarshalWritesWhatPromptJSONWrites(t *testing.T) {
	values := []any{
		nil,
		"text",
		"R&D <team> & co",
		42,
		3.5,
		true,
		[]string{"a", "b"},
		map[string]any{"b": 1, "a": "x<y"},
		dec12Plain{Name: "A & B", Notes: "<note>", Count: 3},
		&dec12Plain{Name: "pointer"},
		dec12Subject,
		[]dec12Plain{{Name: "one"}, {Name: "two"}},
		struct{}{},
	}
	for i, value := range values {
		got, err := dec12Marshal(value)
		if err != nil {
			t.Fatalf("value %d: %v", i, err)
		}
		want, err := promptJSON(value)
		if err != nil {
			t.Fatalf("value %d: promptJSON: %v", i, err)
		}
		if got != string(want) {
			t.Errorf("value %d: dec12Marshal = %q, promptJSON writes %q", i, got, want)
		}
	}

	// A value JSON cannot write is an error, from the pool as from the
	// encoder, and the next value is not affected by it.
	if _, err := dec12Marshal(make(chan int)); err == nil {
		t.Error("a channel was written")
	}
	if got, err := dec12Marshal("after"); err != nil || got != `"after"` {
		t.Errorf("the value after a failure = %q, %v", got, err)
	}

	// Written from many goroutines at once, each value comes back as its own.
	var wait sync.WaitGroup
	for n := 0; n < 16; n++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := 0; i < 200; i++ {
				text := "worker " + strconv.Itoa(n) + " round " + strconv.Itoa(i) + " & more"
				got, err := dec12Marshal(text)
				if want, _ := promptJSON(text); err != nil || got != string(want) {
					t.Errorf("concurrent write = %q, %v; want %q", got, err, want)
					return
				}
			}
		}()
	}
	wait.Wait()
}

// The message is what it was before: the questions as the encoder quotes them,
// then what else was asked for, then the subject.
func TestDec12MessageIsTheMessageItWasBefore(t *testing.T) {
	questionSets := map[string][]assessQuestion{
		"one yes or no":     {{kind: questionYesNo, ask: "Is this spam?"}},
		"one choice":        {{kind: questionChoice, ask: "Which team?", labels: []string{"billing", "technical", "shipping"}}},
		"one rating":        {{kind: questionRating, ask: "How urgent?", low: 1, high: 5}},
		"a rating below 0":  {{kind: questionRating, ask: "How warm?", low: -2, high: 2}},
		"awkward text":      {{kind: questionYesNo, ask: `Does it say "refund" or R&D <b>?`}, {kind: questionChoice, ask: "Which?\nOne.", labels: []string{`"a"`, `b\c`, "é", "<x>"}}},
		"non-ASCII options": {{kind: questionChoice, ask: "言語は?", labels: []string{"日本語", "English"}}},
		"thirteen mixed": {
			{kind: questionYesNo, ask: "q"}, {kind: questionChoice, ask: "c", labels: []string{"x", "y"}},
			{kind: questionRating, ask: "r", low: 0, high: 9}, {kind: questionYesNo, ask: "q"},
			{kind: questionYesNo, ask: "q"}, {kind: questionYesNo, ask: "q"}, {kind: questionYesNo, ask: "q"},
			{kind: questionYesNo, ask: "q"}, {kind: questionYesNo, ask: "q"}, {kind: questionYesNo, ask: "q"},
			{kind: questionYesNo, ask: "q"}, {kind: questionYesNo, ask: "q"}, {kind: questionYesNo, ask: "q"},
		},
		"the desk's own": planFor(reflect.TypeOf(dec12Look{})).questions,
	}
	subjects := []string{`"hello"`, `{"id":"M-1"}`, `"R&D <b>"`}
	steerings := []string{"", "   ", "Be brief.", `Answer "no" when unsure & <quick>`, "two\nlines"}

	for name, questions := range questionSets {
		for _, confidence := range []bool{false, true} {
			for _, subject := range subjects {
				for _, steering := range steerings {
					want, err := dec12ReferencePrompt(subject, questions, steering, confidence)
					if err != nil {
						t.Fatalf("%s: reference: %v", name, err)
					}
					block, err := dec12RenderQuestions(questions)
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					got, err := assessUserPrompt(block, subject, steering, confidence)
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					if got != want {
						t.Fatalf("%s (confidence %v, steering %q): the message changed.\n got: %q\nwant: %q", name, confidence, steering, got, want)
					}
				}
			}
		}
	}
}

// A short run of digits is read as the decoder reads it, and anything the fast
// reader does not take is left to the decoder.
func TestDec12PlainIntegerReadsWhatTheDecoderReads(t *testing.T) {
	cases := []struct {
		raw   string
		plain bool
	}{
		{"0", true}, {"1", true}, {"7", true}, {"42", true}, {"100", true}, {"123456789", true},
		{"-1", true}, {"-12", true}, {" 5 ", true}, {"\n3\n", true},
		{"-0", false}, {"00", false}, {"01", false}, {"-01", false}, {"007", false},
		{"2.0", false}, {"1.5", false}, {"1e1", false}, {"1E1", false}, {"+1", false},
		{"", false}, {"-", false}, {"1234567890", false}, {`"2"`, false}, {"true", false},
		{"null", false}, {"0x10", false}, {"1_0", false}, {"१", false}, {"--1", false}, {"1 2", false},
	}
	for _, tc := range cases {
		t.Run(strconv.Quote(tc.raw), func(t *testing.T) {
			got, plain := dec12PlainInteger([]byte(tc.raw))
			if plain != tc.plain {
				t.Fatalf("dec12PlainInteger(%q) reports %v, want %v", tc.raw, plain, tc.plain)
			}
			if !plain {
				return
			}
			var decoded float64
			if err := json.Unmarshal([]byte(tc.raw), &decoded); err != nil || decoded != math.Trunc(decoded) || int(decoded) != got {
				t.Errorf("dec12PlainInteger(%q) = %d, the decoder reads %v (%v)", tc.raw, got, decoded, err)
			}
		})
	}
}

// What is declared depends on the kinds of the questions and on nothing else,
// so questions of the same kinds are given the same schema, and it is the
// schema that was declared before.
func TestDec12QuestionsOfTheSameKindsShareOneDeclaredAnswer(t *testing.T) {
	first := []assessQuestion{
		{kind: questionYesNo, ask: "Is it about billing?"},
		{kind: questionChoice, ask: "Which team?", labels: []string{"billing", "technical", "shipping"}},
		{kind: questionRating, ask: "How urgent?", low: 1, high: 5},
	}
	sameKinds := []assessQuestion{
		{kind: questionYesNo, ask: "Is the customer angry?"},
		{kind: questionChoice, ask: "Which colour?", labels: []string{"red", "green", "blue"}},
		{kind: questionRating, ask: "How warm?", low: 1, high: 5},
	}
	differing := map[string][]assessQuestion{
		"one more option":    {first[0], {kind: questionChoice, ask: "x", labels: []string{"a", "b", "c", "d"}}, first[2]},
		"one fewer option":   {first[0], {kind: questionChoice, ask: "x", labels: []string{"a", "b"}}, first[2]},
		"a scale that moved": {first[0], first[1], {kind: questionRating, ask: "x", low: 0, high: 4}},
		"a wider scale":      {first[0], first[1], {kind: questionRating, ask: "x", low: 1, high: 6}},
		"a different order":  {first[1], first[0], first[2]},
		"one question more":  append(append([]assessQuestion{}, first...), assessQuestion{kind: questionYesNo, ask: "x"}),
		"one question fewer": first[:2],
		"a rating for a yes": {first[0], first[1], {kind: questionYesNo, ask: "x"}},
	}

	same := func(a, b map[string]any) bool { return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer() }
	one := dec12Schemas.declare(first, false)
	if !same(one, dec12Schemas.declare(first, false)) {
		t.Error("the same questions were given two schemas")
	}
	if !same(one, dec12Schemas.declare(sameKinds, false)) {
		t.Error("questions of the same kinds were given different schemas")
	}
	if !reflect.DeepEqual(one, assessSchema(first, false)) || !reflect.DeepEqual(one, assessSchema(sameKinds, false)) {
		t.Error("the shared schema is not the one assessSchema declares")
	}
	withConfidence := dec12Schemas.declare(first, true)
	if same(one, withConfidence) || !reflect.DeepEqual(withConfidence, assessSchema(first, true)) {
		t.Error("the schema that asks for the model's confidence is not its own")
	}
	for name, questions := range differing {
		got := dec12Schemas.declare(questions, false)
		if same(one, got) {
			t.Errorf("%s: shares a schema with questions of other kinds", name)
		}
		if !reflect.DeepEqual(got, assessSchema(questions, false)) {
			t.Errorf("%s: the schema is not the one assessSchema declares", name)
		}
	}
}

// Nothing the caller wrote is in the key, and the cache stops growing at its
// limit: a program that asks a question of a new shape every time gets a
// correct schema every time and does not keep them.
func TestDec12TheSchemaCacheDoesNotGrowWithoutBound(t *testing.T) {
	var cache dec12SchemaCache
	for low := 0; low < 3*dec12SchemaLimit; low++ {
		questions := []assessQuestion{{kind: questionRating, ask: "How " + strconv.Itoa(low) + "?", low: low, high: low + 4}}
		got := cache.declare(questions, false)
		if !reflect.DeepEqual(got, assessSchema(questions, false)) {
			t.Fatalf("scale %d: the schema is wrong", low)
		}
	}
	if size := cache.size(); size != dec12SchemaLimit {
		t.Errorf("the cache holds %d schemas after %d shapes, want its limit of %d", size, 3*dec12SchemaLimit, dec12SchemaLimit)
	}

	// Past the limit nothing is added, and what was added is still served.
	first := []assessQuestion{{kind: questionRating, ask: "again", low: 0, high: 4}}
	if !reflect.DeepEqual(cache.declare(first, false), assessSchema(first, false)) {
		t.Error("a schema kept before the limit is no longer right")
	}

	// The key says nothing of the text.
	one := dec12SchemaKey(nil, []assessQuestion{{kind: questionChoice, ask: "secret question", labels: []string{"secret a", "secret b"}}}, false)
	if strings.Contains(string(one), "secret") {
		t.Errorf("the key holds the caller's text: %q", one)
	}
}

// Many goroutines asking at once all get a schema that declares the answer, and
// the cache is added to from more than one of them.
func TestDec12TheSchemaCacheIsSafeForConcurrentUse(t *testing.T) {
	var cache dec12SchemaCache
	var wait sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := 0; i < 300; i++ {
				questions := []assessQuestion{{kind: questionRating, ask: "r", low: i % 40, high: i%40 + 3}, {kind: questionYesNo, ask: "y"}}
				confidence := (worker+i)%2 == 0
				if got := cache.declare(questions, confidence); !reflect.DeepEqual(got, assessSchema(questions, confidence)) {
					t.Errorf("worker %d: the schema for scale %d is wrong", worker, i%40)
					return
				}
			}
		}()
	}
	wait.Wait()
	if size := cache.size(); size > dec12SchemaLimit || size != 80 {
		t.Errorf("the cache holds %d schemas for 40 scales with and without confidence, want 80", size)
	}
}

// The request a decision sends is built from the questions and the subject.
func TestDec12BuildRequestMakesTheRequestThatIsSent(t *testing.T) {
	yesNo := func(text string) dec12Ask {
		return dec12Ask{questions: []assessQuestion{{kind: questionYesNo, ask: text}}}
	}

	t.Run("a question that cannot be asked is refused before anything is written", func(t *testing.T) {
		for name, ask := range map[string]dec12Ask{
			"none":          {},
			"a blank one":   yesNo("   "),
			"one option":    {questions: []assessQuestion{{kind: questionChoice, ask: "x", labels: []string{"only"}}}},
			"a repeat":      {questions: []assessQuestion{{kind: questionChoice, ask: "x", labels: []string{"a", " a "}}}},
			"no room":       {questions: []assessQuestion{{kind: questionRating, ask: "x", low: 3, high: 3}}},
			"a vast scale":  {questions: []assessQuestion{{kind: questionRating, ask: "x", low: 0, high: 1000}}},
			"a blank label": {questions: []assessQuestion{{kind: questionChoice, ask: "x", labels: []string{"a", " "}}}},
		} {
			if _, err := dec12BuildRequest(context.Background(), "subject", ask, false, nil); types.KindOf(err) != types.KindConfiguration {
				t.Errorf("%s: err = %v, want a configuration error", name, err)
			}
		}
	})

	t.Run("a subject that says nothing is refused", func(t *testing.T) {
		_, err := dec12BuildRequest(context.Background(), struct{ hidden int }{1}, yesNo("Is it?"), false, nil)
		if types.KindOf(err) != types.KindInvalidRequest {
			t.Errorf("err = %v, want an invalid request", err)
		}
	})

	t.Run("the request declares the answer and keeps the steering out of the instructions", func(t *testing.T) {
		got, err := dec12BuildRequest(context.Background(), "hello", yesNo("Is it a greeting?"), true, []types.OpOptions{{Steering: "Be brief."}})
		if err != nil {
			t.Fatal(err)
		}
		if got.opt.Steering != "" {
			t.Errorf("the steering is left on the options: %q", got.opt.Steering)
		}
		if !strings.Contains(got.user, `Guidance: "Be brief."`) {
			t.Errorf("the steering is not in the message:\n%s", got.user)
		}
		if got.opt.ResponseFormat != "json" || got.opt.SchemaName != "assessment" || got.opt.JSONSchema == nil {
			t.Errorf("the answer is not declared: %q, %q, %v", got.opt.ResponseFormat, got.opt.SchemaName, got.opt.JSONSchema != nil)
		}
		if _, asked := got.opt.JSONSchema["properties"].(map[string]any)["confidence"]; !asked {
			t.Error("the confidence was asked for and is not declared")
		}
	})

	t.Run("a decision defaults to the fast tier and a named tier is kept", func(t *testing.T) {
		fast, _ := dec12BuildRequest(context.Background(), "x", yesNo("Is it?"), false, nil)
		smart, _ := dec12BuildRequest(context.Background(), "x", yesNo("Is it?"), false, []types.OpOptions{{Intelligence: types.Smart}})
		if fast.opt.Intelligence != types.Fast || smart.opt.Intelligence != types.Smart {
			t.Errorf("tiers: %v and %v", fast.opt.Intelligence, smart.opt.Intelligence)
		}
	})

	t.Run("reasoning is preferred off unless something the caller wrote mentions a number", func(t *testing.T) {
		cases := []struct {
			name    string
			subject any
			ask     dec12Ask
			opts    []types.OpOptions
			want    types.Effort
		}{
			{"nothing with a number", "the printer is broken", yesNo("Is it a complaint?"), nil, types.EffortNone},
			{"a number in the subject", "I was charged 40 dollars", yesNo("Is it a complaint?"), nil, types.EffortDefault},
			{"a number in the question", "the printer is broken", yesNo("Is it more than twice as bad as last time?"), nil, types.EffortDefault},
			{"a number in an option", "the printer is broken", dec12Ask{questions: []assessQuestion{{kind: questionChoice, ask: "Which?", labels: []string{"level 1", "level 2"}}}}, nil, types.EffortDefault},
			{"a number in the steering", "the printer is broken", yesNo("Is it a complaint?"), []types.OpOptions{{Steering: "Answer within 5 words."}}, types.EffortDefault},
			{"a number only in the message's own wording", "the printer is broken", dec12Ask{questions: []assessQuestion{{kind: questionRating, ask: "How bad is it?", low: 1, high: 5}}}, nil, types.EffortNone},
		}
		for _, tc := range cases {
			got, err := dec12BuildRequest(context.Background(), tc.subject, tc.ask, false, tc.opts)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got.effort != tc.want {
				t.Errorf("%s: the effort is %q, want %q", tc.name, got.effort, tc.want)
			}
		}
	})

	t.Run("a set of questions worked out ahead gives the request that one worked out now does", func(t *testing.T) {
		questions := planFor(reflect.TypeOf(dec12Look{})).questions
		ahead := planFor(reflect.TypeOf(dec12Look{})).ask
		now := dec12Ask{questions: questions}
		for _, confidence := range []bool{false, true} {
			a, errA := dec12BuildRequest(context.Background(), dec12Subject, ahead, confidence, nil)
			b, errB := dec12BuildRequest(context.Background(), dec12Subject, now, confidence, nil)
			if errA != nil || errB != nil {
				t.Fatalf("errors: %v, %v", errA, errB)
			}
			if a.user != b.user || a.effort != b.effort || !reflect.DeepEqual(a.opt.JSONSchema, b.opt.JSONSchema) {
				t.Errorf("confidence %v: the requests differ", confidence)
			}
		}
	})

	t.Run("two calls on the same questions declare the answer with one schema", func(t *testing.T) {
		a, _ := dec12BuildRequest(context.Background(), "one", yesNo("Is it the first?"), false, nil)
		b, _ := dec12BuildRequest(context.Background(), "two", yesNo("Is it the second?"), false, nil)
		if reflect.ValueOf(a.opt.JSONSchema).Pointer() != reflect.ValueOf(b.opt.JSONSchema).Pointer() {
			t.Error("a schema was built for each call")
		}
	})

	t.Run("a type's questions are worked out once", func(t *testing.T) {
		plan := planFor(reflect.TypeOf(dec12Look{}))
		if plan.err != nil || !plan.ask.ready || plan.ask.block == "" || plan.ask.schema == nil || plan.ask.schemaConfidence == nil {
			t.Fatalf("the plan holds %+v, %v", plan.ask.ready, plan.err)
		}
		if planFor(reflect.TypeOf(dec12Look{})) != plan {
			t.Error("a second look at the type read it again")
		}
		if plan.ask.numbers != dec12QuestionsMentionANumber(plan.questions) {
			t.Error("the plan's reading of the numbers is not the questions'")
		}
	})
}
