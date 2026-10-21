package ops

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/v2/internal/logger"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// coreCall is one request the scripted model of coreScript received.
type coreCall struct {
	system   string
	user     string
	opts     types.OpOptions
	deadline bool
}

// coreScripted records what an operation sent to the model. It is the model
// the tests of Extract, Transform, Generate and their neighbours in this
// package script their answers through.
type coreScripted struct {
	mu    sync.Mutex
	calls []coreCall
}

func (s *coreScripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *coreScripted) last() coreCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return coreCall{}
	}
	return s.calls[len(s.calls)-1]
}

// coreScript installs a model that answers with whatever respond returns and
// writes down each request. The previous model is put back when the test ends.
func coreScript(t *testing.T, respond func(call coreCall) (string, error)) *coreScripted {
	t.Helper()
	scripted := &coreScripted{}
	previousCaller, previousProvider := currentHooks()
	setLLMCaller(func(ctx context.Context, system, user string, opts types.OpOptions) (string, error) {
		_, bounded := ctx.Deadline()
		call := coreCall{system: system, user: user, opts: opts, deadline: bounded}
		scripted.mu.Lock()
		scripted.calls = append(scripted.calls, call)
		scripted.mu.Unlock()
		return respond(call)
	})
	t.Cleanup(func() {
		setLLMCaller(previousCaller)
		SetDefaultProvider(previousProvider)
	})
	return scripted
}

// coreAnswer is coreScript for a model that always gives the same answer.
func coreAnswer(t *testing.T, body string) *coreScripted {
	t.Helper()
	return coreScript(t, func(coreCall) (string, error) { return body, nil })
}

type coreTarget struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestGenerateRefusesATargetNoAnswerCouldSatisfy(t *testing.T) {
	model := coreAnswer(t, `{"name":"x"}`)

	t.Run("an empty interface", func(t *testing.T) {
		result, err := Generate[any]("make one", NewGenerateOptions())
		if err == nil {
			t.Fatalf("Generate[any] returned %v and no error", result)
		}
		if result != nil {
			t.Errorf("result = %v beside an error", result)
		}
		if types.KindOf(err) != types.KindConfiguration {
			t.Errorf("kind = %v, want configuration", types.KindOf(err))
		}
	})
	t.Run("an interface with methods", func(t *testing.T) {
		result, err := Generate[error]("make one", NewGenerateOptions())
		if err == nil || result != nil {
			t.Fatalf("Generate[error] = %v, %v", result, err)
		}
	})
	if model.count() != 0 {
		t.Errorf("the model was called %d times for a target that cannot be satisfied", model.count())
	}
}

func TestTransformDoesNotPanicOnAnInterfaceInput(t *testing.T) {
	model := coreAnswer(t, `{"name":"Ada","age":3}`)

	t.Run("a nil interface value", func(t *testing.T) {
		result, err := Transform[any, coreTarget](nil, NewTransformOptions())
		if err != nil {
			t.Fatalf("Transform[any, coreTarget](nil): %v", err)
		}
		if result.Name != "Ada" {
			t.Errorf("result = %+v", result)
		}
	})
	t.Run("an interface holding a struct", func(t *testing.T) {
		result, err := Transform[any, coreTarget](coreTarget{Name: "x"}, NewTransformOptions())
		if err != nil || result.Name != "Ada" {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
	t.Run("an interface target", func(t *testing.T) {
		result, err := Transform[coreTarget, any](coreTarget{Name: "x"}, NewTransformOptions())
		if err == nil || result != nil {
			t.Fatalf("Transform to any = %v, %v", result, err)
		}
		if types.KindOf(err) != types.KindConfiguration {
			t.Errorf("kind = %v, want configuration", types.KindOf(err))
		}
	})
	if model.count() != 2 {
		t.Errorf("the model was called %d times, want 2", model.count())
	}
}

func TestGenerateRefusesAPromptOverTheBound(t *testing.T) {
	model := coreAnswer(t, `{"name":"x"}`)

	cases := []struct {
		name   string
		prompt string
		wantOK bool
	}{
		{"exactly the bound in ASCII", strings.Repeat("a", 10000), true},
		{"one character over in ASCII", strings.Repeat("a", 10001), false},
		{"4000 Japanese characters are 12000 bytes and fit", strings.Repeat("日", 4000), true},
		{"exactly the bound in Japanese", strings.Repeat("日", 10000), true},
		{"one Japanese character over", strings.Repeat("日", 10001), false},
		{"far over", strings.Repeat("word ", 20000), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := model.count()
			result, err := Generate[coreTarget](c.prompt, NewGenerateOptions())
			if c.wantOK {
				if err != nil {
					t.Fatalf("Generate: %v", err)
				}
				if model.count() != before+1 {
					t.Fatalf("model calls = %d, want one more than %d", model.count(), before)
				}
				if !utf8.ValidString(model.last().user) {
					t.Error("the prompt reached the model with a character cut in half")
				}
				if model.last().user != strings.TrimSpace(c.prompt) {
					t.Error("a prompt inside the bound was changed on its way to the model")
				}
				return
			}
			if err == nil {
				t.Fatal("a prompt over the bound was accepted")
			}
			if result != (coreTarget{}) {
				t.Errorf("result = %+v beside an error", result)
			}
			if types.KindOf(err) != types.KindContextTooLarge {
				t.Errorf("kind = %v, want context too large", types.KindOf(err))
			}
			if !strings.Contains(err.Error(), "10000") {
				t.Errorf("the error does not name the bound: %v", err)
			}
			if model.count() != before {
				t.Error("the model was called for a prompt that was refused")
			}
		})
	}
}

func TestGenerateKeepsTheClausesAppendedAfterTheCallersText(t *testing.T) {
	model := coreAnswer(t, `{"name":"x"}`)

	prompt := strings.Repeat("a", 9900)
	_, err := Generate[coreTarget](prompt, NewGenerateOptions().WithTemplate("TEMPLATE-MARKER"))
	if err == nil {
		// 9900 characters plus the template clause is 9900 + 2 + 31 characters:
		// inside the bound, so the clause has to arrive whole.
		if !strings.Contains(model.last().user, "TEMPLATE-MARKER") {
			t.Fatal("the template clause was cut from the prompt")
		}
		return
	}
	t.Fatalf("a prompt inside the bound was refused: %v", err)
}

func TestGenerateDoesNotWriteThePromptOrTheAnswerToTheLog(t *testing.T) {
	logs := captureLogs(t)
	coreAnswer(t, `{"nonsense": true}`)

	_, err := Generate[coreTarget]("PROMPT-SECRET-MARKER please", NewGenerateOptions())
	if err == nil {
		t.Fatal("an answer with none of the target's fields was accepted")
	}
	if logs() == "" {
		t.Fatal("the operation logged nothing, so this witnesses nothing")
	}
	if text := logs(); strings.Contains(text, "PROMPT-SECRET-MARKER") || strings.Contains(text, "nonsense") {
		t.Errorf("the log carries the caller's text or the model's:\n%s", text)
	}
}

// Extract logged the first hundred bytes of the caller's input at debug, which
// is a copy of an invoice or a ticket in the log. It logs the length.
func TestExtractDoesNotLogTheCallersInput(t *testing.T) {
	inputs := []struct {
		name  string
		input string
	}{
		{"short", "jane.doe@example.com unpaid invoice 4417"},
		{"long, past a hundred bytes", strings.Repeat("padding ", 20) + "TAIL-SECRET-MARKER"},
		{"the start of a long one", "HEAD-SECRET-MARKER " + strings.Repeat("padding ", 40)},
		{"with line breaks", "name: Ada\nSSN: 078-05-1120\nnotes: HEAD-SECRET-MARKER"},
		{"unicode", "Zażółć gęślą jaźń, HEAD-SECRET-MARKER"},
		{"a single word", "HEAD-SECRET-MARKER"},
	}
	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			coreAnswer(t, `{"name":"Ada","age":3}`)

			if _, err := Extract[coreTarget](tc.input, NewExtractOptions()); err != nil {
				t.Fatalf("Extract: %v", err)
			}
			text := logs()
			if !strings.Contains(text, "Extract input normalized") {
				t.Fatalf("the operation logged no input line, so this witnesses nothing:\n%s", text)
			}
			for _, secret := range []string{"jane.doe", "4417", "SECRET-MARKER", "078-05-1120", "padding", "Zażółć"} {
				if strings.Contains(text, secret) {
					t.Errorf("the log carries the caller's input (%q):\n%s", secret, text)
				}
			}
		})
	}
}

func TestGenerateRefusesAnExampleOrSeedThatCannotBeWritten(t *testing.T) {
	model := coreAnswer(t, `{"name":"x"}`)

	t.Run("seed data", func(t *testing.T) {
		result, err := Generate[coreTarget]("make one", NewGenerateOptions().WithSeedData(make(chan int)))
		if err == nil || result != (coreTarget{}) {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
	t.Run("examples", func(t *testing.T) {
		result, err := Generate[coreTarget]("make one", NewGenerateOptions().WithExamples(func() {}))
		if err == nil || result != (coreTarget{}) {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
	if model.count() != 0 {
		t.Errorf("the model was called %d times with an option that was dropped", model.count())
	}
}

func TestExtractRefusesAnExampleThatCannotBeWritten(t *testing.T) {
	model := coreAnswer(t, `{"name":"x"}`)

	result, err := Extract[coreTarget]("Ada, 3", NewExtractOptions().WithExamples(make(chan int)))
	if err == nil || result != (coreTarget{}) {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if model.count() != 0 {
		t.Errorf("the model was called %d times", model.count())
	}
}

func TestExtractSendsTheStrictSchemaRequestOnItsOwn(t *testing.T) {
	model := coreAnswer(t, `{"name":"Ada","age":3}`)

	_, err := Extract[coreTarget]("Ada, 3", NewExtractOptions().WithAllowPartial(false).WithStrictSchema(true))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(model.last().opts.Steering, "strict schema") {
		t.Errorf("StrictSchema never reached the model; steering = %q", model.last().opts.Steering)
	}
}

func TestTransformMappingRulesAreRenderedInKeyOrder(t *testing.T) {
	model := coreAnswer(t, `{"name":"Ada","age":3}`)

	rules := map[string]string{"zeta": "z", "alpha": "a", "mid": "m", "beta": "b", "omega": "o"}
	var first string
	for i := 0; i < 30; i++ {
		_, err := Transform[coreTarget, coreTarget](coreTarget{Name: "x"}, NewTransformOptions().WithMappingRules(rules))
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		steering := model.last().opts.Steering
		if i == 0 {
			first = steering
			want := "alpha <- a; beta <- b; mid <- m; omega <- o; zeta <- z"
			if !strings.Contains(steering, want) {
				t.Fatalf("steering = %q, want it to contain %q", steering, want)
			}
		} else if steering != first {
			t.Fatalf("the prompt changed between identical calls:\n%q\n%q", first, steering)
		}
	}
}

func TestTransformParseFailureCarriesNoConfidenceAndNoValue(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not JSON", `I cannot do that`},
		{"a field of the wrong type after one that decoded", `{"name":"Ada","age":"three"}`},
		{"an object with none of the fields", `{"other":1}`},
		{"a truncated object", `{"name":"Ada"`},
		{"an array", `["a"]`},
		{"a number", `7`},
		{"null in an object field of the wrong type", `{"name":"Ada","age":[1]}`},
		{"an empty answer", ``},
		{"a string", `"Ada"`},
		{"an empty object", `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			result, err := Transform[coreTarget, coreTarget](coreTarget{Name: "x"}, NewTransformOptions())
			if err == nil {
				t.Fatalf("an unusable answer was accepted: %+v", result)
			}
			if result != (coreTarget{}) {
				t.Errorf("result = %+v beside an error", result)
			}
			var transformErr types.TransformError
			if !errors.As(err, &transformErr) {
				t.Fatalf("error %T is not a TransformError", err)
			}
			if transformErr.ModelConfidence != 0 {
				t.Errorf("ModelConfidence = %v: no model reported one", transformErr.ModelConfidence)
			}
		})
	}
}

// A decode that fails after some of the fields were read leaves those fields in
// whatever it decoded into, so the value has to be decoded into a local that is
// handed back only when the whole answer was accepted.
func TestGenerateParseFailureReturnsNoValue(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"not JSON", `I cannot do that`},
		{"a field of the wrong type after one that decoded", `{"name":"Ada","age":"three"}`},
		{"a field of the wrong type before one that decoded", `{"age":"three","name":"Ada"}`},
		{"an object with none of the fields", `{"other":1}`},
		{"a truncated object", `{"name":"Ada"`},
		{"an array", `["a"]`},
		{"an empty answer", ``},
		{"an empty object", `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			coreAnswer(t, c.body)
			result, err := Generate[coreTarget]("make one", NewGenerateOptions())
			if err == nil {
				t.Fatalf("an unusable answer was accepted: %+v", result)
			}
			if result != (coreTarget{}) {
				t.Errorf("result = %+v beside an error", result)
			}
		})
	}
}

func TestTransformRefusesAChangedPreservedFieldAndReturnsNothing(t *testing.T) {
	coreAnswer(t, `{"name":"Changed","age":3}`)

	result, err := Transform[coreTarget, coreTarget](coreTarget{Name: "Ada", Age: 3},
		NewTransformOptions().WithPreserveFields([]string{"name"}))
	if err == nil {
		t.Fatal("a changed preserved field was accepted")
	}
	if result != (coreTarget{}) {
		t.Errorf("result = %+v beside an error", result)
	}
	if types.KindOf(err) != types.KindInvariantViolation {
		t.Errorf("kind = %v, want invariant violation", types.KindOf(err))
	}
	if strings.Contains(err.Error(), "Changed") || strings.Contains(err.Error(), "Ada") {
		t.Errorf("the error carries a value: %v", err)
	}
}

func TestTransformAndGenerateGiveTheCallAnEndByDefault(t *testing.T) {
	model := coreAnswer(t, `{"name":"Ada","age":3}`)

	if _, err := Transform[coreTarget, coreTarget](coreTarget{Name: "x"}, NewTransformOptions()); err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !model.last().deadline {
		t.Error("Transform ran with no deadline when the caller set none")
	}
	if _, err := Generate[coreTarget]("make one", NewGenerateOptions()); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !model.last().deadline {
		t.Error("Generate ran with no deadline when the caller set none")
	}

	// A deadline the caller set is the budget and is not replaced.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	opts := NewGenerateOptions()
	opts.CommonOptions.Context = ctx
	if _, err := Generate[coreTarget]("make one", opts); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !model.last().deadline {
		t.Error("the caller's deadline was dropped")
	}
}

// A call that goes well writes nothing at or above info: that an operation
// started and that it completed are debug lines, and their arguments are built
// only when debug is on. They were info lines, which cost a call its boxed
// arguments at the default level for a line that level does not write.
func TestAnOperationsStartAndEndLinesAreDebugLines(t *testing.T) {
	flat, nested := reflect.TypeFor[benchFlat](), reflect.TypeFor[benchNested]()
	cases := []struct {
		name  string
		run   func() error
		lines []string
	}{
		{"Extract", func() error {
			_, err := Extract[benchFlat]("order A-2207, Team plan, $49", NewExtractOptions())
			return err
		}, []string{"Extract operation started", "Extract operation completed"}},
		{"Transform", func() error {
			_, err := Transform[benchFlat, benchNested](benchFlat{OrderID: "A-2207"}, NewTransformOptions())
			return err
		}, []string{"Transform operation started", "Transform operation completed"}},
		{"Generate of a struct", func() error {
			_, err := Generate[benchNested]("an order for a team plan", NewGenerateOptions())
			return err
		}, []string{"Generate operation started", "Generate operation completed"}},
		{"Generate of a string", func() error {
			_, err := Generate[string]("a greeting", NewGenerateOptions())
			return err
		}, []string{"Generate operation started", "Generate operation completed (string)"}},
	}
	answers := map[string]string{
		"Extract":              benchAnswerFor(t, flat),
		"Transform":            benchAnswerFor(t, nested),
		"Generate of a struct": benchAnswerFor(t, nested),
		"Generate of a string": "hello there",
	}
	for _, tc := range cases {
		for _, level := range []logger.LogLevel{logger.InfoLevel, logger.WarnLevel, logger.ErrorLevel} {
			t.Run(fmt.Sprintf("%s at %v", tc.name, level), func(t *testing.T) {
				swapLLMResponse(t, answers[tc.name])
				text, entries := loggerAt(t, level)
				if err := tc.run(); err != nil {
					t.Fatal(err)
				}
				if text() != "" || len(entries()) != 0 {
					t.Errorf("a call that went well wrote %q and kept %d entries at level %v", text(), len(entries()), level)
				}
			})
		}
		t.Run(tc.name+" at debug", func(t *testing.T) {
			swapLLMResponse(t, answers[tc.name])
			_, entries := loggerAt(t, logger.DebugLevel)
			if err := tc.run(); err != nil {
				t.Fatal(err)
			}
			level := map[string]string{}
			for _, entry := range entries() {
				level[entry.Message] = entry.Level
				if entry.Level == "INFO" {
					t.Errorf("the line %q was written at INFO", entry.Message)
				}
			}
			for _, line := range tc.lines {
				if level[line] != "DEBUG" {
					t.Errorf("%q was written at %q, want DEBUG", line, level[line])
				}
			}
		})
	}
}

// Transform and Generate read their answer as the caller's type: one that shares
// a member with it is accepted, and a field it leaves out is the zero value. An
// answer that is not about the type, is null or is of the wrong kind is refused
// under the operation's own error, beside a zero result, with a classified cause
// that holds nothing the model wrote.
func TestTransformAndGenerateReadTheAnswerAsTheCallersType(t *testing.T) {
	const marker = "ZZMARKER"
	accepted := []struct {
		name, body string
		want       coreTarget
	}{
		{"every member", `{"name":"Dana","age":3}`, coreTarget{"Dana", 3}},
		{"a member left out", `{"name":"Dana"}`, coreTarget{Name: "Dana"}},
		{"a member that is null", `{"name":"Dana","age":null}`, coreTarget{Name: "Dana"}},
		{"an unknown member beside a known one", `{"name":"Dana","extra":1}`, coreTarget{Name: "Dana"}},
		{"a code fence", "```json\n{\"age\":7}\n```", coreTarget{Age: 7}},
	}
	refused := []struct {
		name, body string
		kind       types.ErrorKind
	}{
		{"an empty object", `{}`, types.KindSchemaViolation},
		{"an object that shares no member", `{"` + marker + `":"` + marker + `"}`, types.KindSchemaViolation},
		{"a member of the wrong kind", `{"name":["` + marker + `"]}`, types.KindSchemaViolation},
		{"null", `null`, types.KindMalformedOutput},
		{"an array", `["` + marker + `"]`, types.KindMalformedOutput},
		{"prose", marker + ` is not json`, types.KindMalformedOutput},
	}
	for _, tc := range accepted {
		t.Run("accepted/"+tc.name, func(t *testing.T) {
			coreAnswer(t, tc.body)
			if got, err := Transform[string, coreTarget]("in", NewTransformOptions()); err != nil || got != tc.want {
				t.Errorf("Transform = %+v, %v, want %+v", got, err, tc.want)
			}
			if got, err := Generate[coreTarget]("make one", NewGenerateOptions()); err != nil || got != tc.want {
				t.Errorf("Generate = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			coreAnswer(t, tc.body)
			got, err := Transform[string, coreTarget]("in", NewTransformOptions())
			var transformErr types.TransformError
			if !errors.As(err, &transformErr) || got != (coreTarget{}) {
				t.Fatalf("Transform = %+v, %v, want a TransformError beside a zero result", got, err)
			}
			wantKind(t, err, tc.kind, marker)
			if !strings.Contains(transformErr.Reason, "failed to parse response") {
				t.Errorf("the reason does not say what failed: %q", transformErr.Reason)
			}

			generated, err := Generate[coreTarget]("make one", NewGenerateOptions())
			var generateErr types.GenerateError
			if !errors.As(err, &generateErr) || generated != (coreTarget{}) {
				t.Fatalf("Generate = %+v, %v, want a GenerateError beside a zero result", generated, err)
			}
			wantKind(t, err, tc.kind, marker)
		})
	}
}
