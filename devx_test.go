package schemaflux_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// The four papercuts in DX-001..DX-005, all reported from integrating this
// library into a real application rather than found by reading it.
//
// None of them is a wrong answer. Each is the library making a caller work out
// something it already knew: which model will be used, where a context goes,
// why a field it never asked about failed a call, and what a fake provider can
// be asked about afterwards. That is why they went unnoticed here -- every one
// of them is invisible from inside, where the mechanism is already known.
//
// `schemafluxtest.Install` calls t.Setenv, so nothing in this file may call
// t.Parallel.

type devxDoc struct {
	Title string `json:"title"`
}

// --- DX-001: .Model() on the fluent builders ---------------------------------

// The pin has to reach the wire from EVERY builder shape.
//
// The option types come in three (see requestBase's doc comment): embedding
// CommonOptions, embedding types.OpOptions, and spelling the common fields out
// flat. The third shape had no Model field at all before this change, so a
// setter would have had nothing to write to -- and a table with one entry per
// shape is the only way to see that, because each shape compiles identically.
func TestModelPinReachesTheProviderFromEveryBuilderShape(t *testing.T) {
	const pinned = "gpt-5-mini-2026-01-01"

	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"Extracting", func(t *testing.T) {
			_, _ = schemaflux.Extracting[devxDoc]("x").Model(pinned).Run(context.Background())
		}},
		{"Generating", func(t *testing.T) {
			_, _ = schemaflux.Generating[string]("x").Model(pinned).Run(context.Background())
		}},
		{"Summarizing", func(t *testing.T) {
			_, _ = schemaflux.Summarizing("some text to summarise").Model(pinned).Run()
		}},
		{"Rewriting", func(t *testing.T) {
			_, _ = schemaflux.Rewriting("some text").Model(pinned).Run()
		}},
		{"Translating", func(t *testing.T) {
			_, _ = schemaflux.Translating("some text").To("fr").Model(pinned).Run()
		}},
		{"Choosing", func(t *testing.T) {
			_, _ = schemaflux.Choosing([]string{"a", "b"}).Model(pinned).Run(context.Background())
		}},
		{"Classifying", func(t *testing.T) {
			_, _ = schemaflux.Classifying[string, string]("x").
				Categories("a", "b").Model(pinned).Run()
		}},
		{"Scoring", func(t *testing.T) {
			_, _ = schemaflux.Scoring("x").Model(pinned).Run()
		}},
		{"Inferring", func(t *testing.T) {
			_, _ = schemaflux.Inferring("x").Model(pinned).Run()
		}},
		{"CheckingSimilarity", func(t *testing.T) {
			_, _ = schemaflux.CheckingSimilarity("a", "b").Model(pinned).Run()
		}},
		{"Comparing", func(t *testing.T) {
			_, _ = schemaflux.Comparing("a", "b").Model(pinned).Run()
		}},
		{"Explaining", func(t *testing.T) {
			_, _ = schemaflux.Explaining("x").Model(pinned).Run()
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := schemafluxtest.New().Shaped()
			schemafluxtest.Install(t, p)

			tc.run(t)

			if p.CallCount() == 0 {
				t.Fatal("the operation never reached the provider, so this proves nothing")
			}
			if got := p.LastRequest().Model; got != pinned {
				t.Errorf("model = %q, want the pinned %q", got, pinned)
			}
		})
	}
}

// A pin beats the speed tier, whichever order they are set in. The tier is a
// preference that floats as models are released; a pin is the caller saying
// they need this exact one, which is the whole reason to have both.
func TestModelPinBeatsTheSpeedTier(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func()
	}{
		{"pin after tier", func() {
			_, _ = schemaflux.Extracting[devxDoc]("x").Smart().Model("pinned-model").Run(context.Background())
		}},
		{"pin before tier", func() {
			_, _ = schemaflux.Extracting[devxDoc]("x").Model("pinned-model").Smart().Run(context.Background())
		}},
		{"pin with Fast", func() {
			_, _ = schemaflux.Extracting[devxDoc]("x").Fast().Model("pinned-model").Run(context.Background())
		}},
		{"pin with Quick", func() {
			_, _ = schemaflux.Extracting[devxDoc]("x").Quick().Model("pinned-model").Run(context.Background())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := schemafluxtest.New().Shaped()
			schemafluxtest.Install(t, p)
			tc.run()
			if got := p.LastRequest().Model; got != "pinned-model" {
				t.Errorf("model = %q, want the pin to win over the tier", got)
			}
		})
	}
}

// The last pin wins, and an empty string clears it back to the tier.
//
// Clearing matters because a caller threading an optional model through their
// own config will pass "" when the reader has not chosen one, and the useful
// reading of that is "no opinion" rather than "pin the empty model".
func TestModelPinIsReplacedAndCleared(t *testing.T) {
	t.Run("last pin wins", func(t *testing.T) {
		p := schemafluxtest.New().Shaped()
		schemafluxtest.Install(t, p)
		_, _ = schemaflux.Extracting[devxDoc]("x").Model("first").Model("second").Run(context.Background())
		if got := p.LastRequest().Model; got != "second" {
			t.Errorf("model = %q, want the later pin", got)
		}
	})

	t.Run("empty clears the pin", func(t *testing.T) {
		p := schemafluxtest.New().Shaped()
		schemafluxtest.Install(t, p)
		_, _ = schemaflux.Extracting[devxDoc]("x").Model("first").Model("").Run(context.Background())
		got := p.LastRequest().Model
		if got == "first" {
			t.Error("an empty pin left the previous one in place")
		}
		if got == "" {
			t.Error("clearing the pin left no model at all; the tier mapping should have supplied one")
		}
	})
}

// --- DX-002: Run(ctx) everywhere ---------------------------------------------

// A context passed to Run has to reach the provider, on the builders that took
// no context at all before this change.
//
// Cancellation is the assertion because it is observable without inspecting
// anything: the fake provider reports a cancelled context as an error, so a
// context that did not arrive shows up as a call that succeeded.
func TestRunAcceptsAContextOnTheBuildersThatTookNone(t *testing.T) {
	cases := []struct {
		name string
		run  func(ctx context.Context) error
	}{
		{"Summarizing", func(ctx context.Context) error {
			_, err := schemaflux.Summarizing("text").Run(ctx)
			return err
		}},
		{"Rewriting", func(ctx context.Context) error {
			_, err := schemaflux.Rewriting("text").Run(ctx)
			return err
		}},
		{"Translating", func(ctx context.Context) error {
			_, err := schemaflux.Translating("text").To("fr").Run(ctx)
			return err
		}},
		{"Expanding", func(ctx context.Context) error {
			_, err := schemaflux.Expanding("text").Run(ctx)
			return err
		}},
		{"Classifying", func(ctx context.Context) error {
			_, err := schemaflux.Classifying[string, string]("x").Categories("a", "b").Run(ctx)
			return err
		}},
		{"Scoring", func(ctx context.Context) error {
			_, err := schemaflux.Scoring("x").Run(ctx)
			return err
		}},
		{"Comparing", func(ctx context.Context) error {
			_, err := schemaflux.Comparing("a", "b").Run(ctx)
			return err
		}},
		{"Inferring", func(ctx context.Context) error {
			_, err := schemaflux.Inferring("x").Run(ctx)
			return err
		}},
		{"Explaining", func(ctx context.Context) error {
			_, err := schemaflux.Explaining("x").Run(ctx)
			return err
		}},
		{"CheckingSimilarity", func(ctx context.Context) error {
			_, err := schemaflux.CheckingSimilarity("a", "b").Run(ctx)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := schemafluxtest.New().Shaped()
			schemafluxtest.Install(t, p)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if err := tc.run(ctx); !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled; the context did not reach the provider", err)
			}
		})
	}
}

// Run() with no argument still compiles and still works. The change is additive
// or it is a breaking change dressed up as a papercut fix.
func TestRunWithNoContextStillWorks(t *testing.T) {
	p := schemafluxtest.New().Shaped().Reply("a summary")
	schemafluxtest.Install(t, p)

	got, err := schemaflux.Summarizing("some text").Run()
	if err != nil {
		t.Fatalf("Run() with no context: %v", err)
	}
	if got == "" {
		t.Error("no summary came back")
	}
}

// Run(ctx) and .Context(ctx).Run() are the same request. If they were not, the
// fix would have introduced a second way to be wrong instead of removing one.
func TestRunContextMatchesTheContextBuilder(t *testing.T) {
	// Well under the default invocation timeout. A deadline LONGER than that
	// default never survives -- applyTimeout layers the default on top and the
	// earlier of the two wins, which is context.WithTimeout's own rule -- so
	// each call would derive its own deadline microseconds apart and the
	// comparison would be measuring the clock rather than the plumbing.
	deadline := time.Now().Add(2 * time.Second)

	viaRun := schemafluxtest.New().Shaped()
	restore := schemafluxtest.Install(t, viaRun)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	_, _ = schemaflux.Summarizing("text").Run(ctx)
	restore()

	viaBuilder := schemafluxtest.New().Shaped()
	schemafluxtest.Install(t, viaBuilder)
	_, _ = schemaflux.Summarizing("text").Context(ctx).Run()

	runDeadline, okRun := viaRun.ContextN(0).Deadline()
	builderDeadline, okBuilder := viaBuilder.ContextN(0).Deadline()
	if !okRun || !okBuilder {
		t.Fatalf("a deadline did not survive: Run(ctx) had one = %v, .Context(ctx) had one = %v",
			okRun, okBuilder)
	}
	if !runDeadline.Equal(builderDeadline) {
		t.Errorf("Run(ctx) deadline %s, .Context(ctx).Run() deadline %s; they must be the same request",
			runDeadline, builderDeadline)
	}
}

// --- DX-003 / DX-004: the two traps name their remedy -------------------------

type devxRequiredFields struct {
	Present string `json:"present"`
	Absent  string `json:"absent"`
}

// A required field that came back empty has to say what to do about it.
//
// The failure and its fix are in different places: the error surfaces at a call
// site after a repair loop, and the fix is a struct tag on a type declared
// somewhere else. Without the remedy in the message there is nothing to search
// for, and every plausible reading -- bad model, bad prompt, wrong operation --
// is wrong.
func TestRequiredButEmptyErrorNamesItsRemedy(t *testing.T) {
	p := schemafluxtest.New().Reply(`{"present":"here","absent":""}`)
	schemafluxtest.Install(t, p)

	_, err := schemaflux.Extracting[devxRequiredFields]("x").Strict().Run(context.Background())
	if err == nil {
		t.Fatal("an empty required field was accepted")
	}
	message := err.Error()

	if !strings.Contains(message, "absent") {
		t.Errorf("the error does not name the field: %v", err)
	}
	for _, want := range []string{
		`schemaflux:"optional"`, // the explicit tag
		"pointer",               // the type-level spelling
		"omitempty",             // the inherited inference
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the error does not mention %q, so the remedy is unsearchable: %v", want, err)
		}
	}
	// It must not read as a recommendation. A required field arriving empty is
	// often the failure this library exists to refuse, and the fix is then a
	// better prompt rather than a weaker type.
	if !strings.Contains(message, "If an empty value is a valid answer") {
		t.Errorf("the remedy is stated unconditionally; it must be conditional: %v", err)
	}
	// Strict is the other lever, and the one a caller is most likely to want:
	// the check runs only in Strict mode, which they may have reached for
	// wanting exact decoding rather than mandatory non-empty fields.
	if !strings.Contains(message, "Strict()") {
		t.Errorf("the error does not say Strict is what requires the field: %v", err)
	}
}

// Each of the three spellings actually works, so the remedy the error names is
// not merely advice.
func TestEveryRemedyTheErrorNamesActuallyWorks(t *testing.T) {
	t.Run("pointer", func(t *testing.T) {
		type target struct {
			Present string  `json:"present"`
			Absent  *string `json:"absent"`
		}
		p := schemafluxtest.New().Reply(`{"present":"here","absent":""}`)
		schemafluxtest.Install(t, p)
		if _, err := schemaflux.Extracting[target]("x").Strict().Run(context.Background()); err != nil {
			t.Errorf("a pointer field did not make the empty value acceptable: %v", err)
		}
	})

	t.Run("schemaflux optional tag", func(t *testing.T) {
		type target struct {
			Present string `json:"present"`
			Absent  string `json:"absent" schemaflux:"optional"`
		}
		p := schemafluxtest.New().Reply(`{"present":"here","absent":""}`)
		schemafluxtest.Install(t, p)
		if _, err := schemaflux.Extracting[target]("x").Strict().Run(context.Background()); err != nil {
			t.Errorf(`a schemaflux:"optional" tag did not make the empty value acceptable: %v`, err)
		}
	})

	t.Run("omitempty", func(t *testing.T) {
		type target struct {
			Present string `json:"present"`
			Absent  string `json:"absent,omitempty"`
		}
		p := schemafluxtest.New().Reply(`{"present":"here","absent":""}`)
		schemafluxtest.Install(t, p)
		if _, err := schemaflux.Extracting[target]("x").Strict().Run(context.Background()); err != nil {
			t.Errorf("an omitempty tag did not make the empty value acceptable: %v", err)
		}
	})
}

// An unknown field rejected under Strict has to say that Strict is what
// rejected it. The mode is opt-in and nothing else in the library behaves this
// way, but the message reads as a decoding fact rather than as a consequence of
// a mode the caller chose.
func TestUnknownFieldErrorNamesStrictAsTheCause(t *testing.T) {
	p := schemafluxtest.New().Reply(`{"title":"ok","confidence":0.91}`)
	schemafluxtest.Install(t, p)

	_, err := schemaflux.Extracting[devxDoc]("x").Strict().Run(context.Background())
	if err == nil {
		t.Fatal("Strict accepted an unknown field")
	}
	message := err.Error()
	if !strings.Contains(message, "confidence") {
		t.Errorf("the error does not name the field: %v", err)
	}
	if !strings.Contains(message, "Strict()") {
		t.Errorf("the error does not name Strict as the cause: %v", err)
	}
}

// The counterpart, and the reason the message above says "drop Strict": without
// it the same answer is fine. A caller reading the error has to be able to act
// on it, and this is what acting on it produces.
func TestTheSameAnswerIsAcceptedWithoutStrict(t *testing.T) {
	p := schemafluxtest.New().Reply(`{"title":"ok","confidence":0.91}`)
	schemafluxtest.Install(t, p)

	got, err := schemaflux.Extracting[devxDoc]("x").Run(context.Background())
	if err != nil {
		t.Fatalf("an extra field was rejected without Strict: %v", err)
	}
	if got.Title != "ok" {
		t.Errorf("title = %q, want the answer to survive the extra field", got.Title)
	}
}

// --- DX-005: schemafluxtest records contexts and answers from the request -----

func TestProviderRecordsTheContextOfEveryCall(t *testing.T) {
	p := schemafluxtest.New().Shaped()
	schemafluxtest.Install(t, p)

	first := time.Now().Add(11 * time.Second)
	second := time.Now().Add(29 * time.Second)

	ctx1, cancel1 := context.WithDeadline(context.Background(), first)
	defer cancel1()
	ctx2, cancel2 := context.WithDeadline(context.Background(), second)
	defer cancel2()

	_, _ = schemaflux.Summarizing("one").Run(ctx1)
	_, _ = schemaflux.Summarizing("two").Run(ctx2)

	if n := len(p.Contexts()); n != 2 {
		t.Fatalf("recorded %d contexts, want 2", n)
	}
	got1, ok1 := p.ContextN(0).Deadline()
	got2, ok2 := p.ContextN(1).Deadline()
	if !ok1 || !ok2 {
		t.Fatal("a recorded context lost its deadline")
	}
	if !got1.Equal(first) || !got2.Equal(second) {
		t.Errorf("contexts recorded out of order or wrong: got %s and %s, want %s and %s",
			got1, got2, first, second)
	}
}

// Out of range is a background context, not a panic and not nil: a test
// asserting on a call that never happened should report the missing deadline it
// was actually looking for.
func TestContextNOutOfRangeIsBackground(t *testing.T) {
	p := schemafluxtest.New().Shaped()
	for _, n := range []int{-1, 0, 1, 99} {
		if _, ok := p.ContextN(n).Deadline(); ok {
			t.Errorf("ContextN(%d) on an unused provider carried a deadline", n)
		}
	}
}

func TestReplyFuncAnswersFromTheRequest(t *testing.T) {
	p := schemafluxtest.New().ReplyFunc(func(call int, req schemaflux.CompletionRequest) (string, error) {
		// The whole point: the answer depends on what was asked. A caller that
		// batches cannot script this ahead of time without reimplementing its
		// own batching in the fixture.
		if strings.Contains(req.UserPrompt, "alpha") {
			return "answered alpha", nil
		}
		return "answered something else", nil
	})
	schemafluxtest.Install(t, p)

	got, err := schemaflux.Summarizing("a passage about alpha particles").Run(context.Background())
	if err != nil {
		t.Fatalf("Summarizing: %v", err)
	}
	if !strings.Contains(got, "alpha") {
		t.Errorf("got %q, want the reply that was computed from the request", got)
	}
}

func TestReplyFuncSeesTheCallIndex(t *testing.T) {
	p := schemafluxtest.New().ReplyFunc(func(call int, _ schemaflux.CompletionRequest) (string, error) {
		return "call " + string(rune('0'+call)), nil
	})
	schemafluxtest.Install(t, p)

	for want := 0; want < 3; want++ {
		got, err := schemaflux.Summarizing("text").Run(context.Background())
		if err != nil {
			t.Fatalf("call %d: %v", want, err)
		}
		if expected := "call " + string(rune('0'+want)); got != expected {
			t.Errorf("call %d answered %q, want %q", want, got, expected)
		}
	}
}

func TestReplyFuncErrorFailsTheCall(t *testing.T) {
	p := schemafluxtest.New().ReplyFunc(func(int, schemaflux.CompletionRequest) (string, error) {
		return "", errors.New("the fixture refused")
	})
	schemafluxtest.Install(t, p)

	_, err := schemaflux.Summarizing("text").Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "the fixture refused") {
		t.Fatalf("err = %v, want the fixture's error surfaced", err)
	}
}

// ReplyFunc wins over a scripted list, so a test can start from Reply and
// override without unpicking the fixture it inherited.
func TestReplyFuncTakesPrecedenceOverReply(t *testing.T) {
	p := schemafluxtest.New().
		Reply("the scripted answer").
		ReplyFunc(func(int, schemaflux.CompletionRequest) (string, error) {
			return "the computed answer", nil
		})
	schemafluxtest.Install(t, p)

	got, err := schemaflux.Summarizing("text").Run(context.Background())
	if err != nil {
		t.Fatalf("Summarizing: %v", err)
	}
	if got != "the computed answer" {
		t.Errorf("got %q, want ReplyFunc to win over Reply", got)
	}
}

// Scripted errors still apply first, so "fail twice, then answer from the
// request" stays expressible.
func TestScriptedFailuresStillApplyBeforeReplyFunc(t *testing.T) {
	p := schemafluxtest.New().
		FailThen(1, errors.New("first attempt fails"), "unused").
		ReplyFunc(func(int, schemaflux.CompletionRequest) (string, error) {
			return "the computed answer", nil
		})
	schemafluxtest.Install(t, p)

	if _, err := schemaflux.Summarizing("text").Run(context.Background()); err == nil {
		t.Fatal("the scripted failure did not fire")
	}
}

func TestResetClearsRecordedContexts(t *testing.T) {
	p := schemafluxtest.New().Shaped()
	schemafluxtest.Install(t, p)

	_, _ = schemaflux.Summarizing("text").Run(context.Background())
	if len(p.Contexts()) != 1 {
		t.Fatalf("recorded %d contexts before Reset, want 1", len(p.Contexts()))
	}
	p.Reset()
	if n := len(p.Contexts()); n != 0 {
		t.Errorf("Reset left %d contexts behind", n)
	}
}
