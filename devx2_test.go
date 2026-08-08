package schemaflux_test

import (
	"context"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// DX-007 and DX-008: two defaults that were decided for a caller and never
// mentioned to them.
//
// Both are the same failure in different clothes. Nothing errored, nothing
// looked wrong, and the behaviour a caller got was not the behaviour they
// asked for -- which is the failure this library is being rebuilt against,
// arriving through configuration rather than through a model.

type devx2Doc struct {
	Title string `json:"title"`
}

type devx2Pair struct {
	Present string `json:"present"`
	Absent  string `json:"absent"`
}

// --- DX-007: Strict's two rules, separated -----------------------------------

// The four combinations, each asserting exactly which rule ran.
//
// A table rather than four tests, because the point is the GRID: every cell has
// to be reachable, and before this change only two of them were -- both rules
// or neither.
func TestStrictsTwoRulesAreIndependentlyReachable(t *testing.T) {
	const extraField = `{"title":"ok","confidence":0.91}`
	const emptyRequired = `{"present":"here","absent":""}`

	t.Run("neither rule: both answers accepted", func(t *testing.T) {
		schemafluxtest.Install(t, schemafluxtest.New().Reply(extraField))
		if _, err := schemaflux.Extracting[devx2Doc]("x").Run(context.Background()); err != nil {
			t.Errorf("an extra field was rejected with neither rule asked for: %v", err)
		}

		schemafluxtest.Install(t, schemafluxtest.New().Reply(emptyRequired))
		if _, err := schemaflux.Extracting[devx2Pair]("x").Run(context.Background()); err != nil {
			t.Errorf("an empty required field was rejected with neither rule asked for: %v", err)
		}
	})

	t.Run("ExactFields alone", func(t *testing.T) {
		schemafluxtest.Install(t, schemafluxtest.New().Reply(extraField))
		_, err := schemaflux.Extracting[devx2Doc]("x").ExactFields().Run(context.Background())
		if err == nil {
			t.Error("ExactFields accepted a property the schema does not name")
		}

		// And the OTHER rule stayed off. This is the assertion that would have
		// caught the bundle: before DX-007 asking for exact decoding silently
		// turned on mandatory non-empty fields as well.
		schemafluxtest.Install(t, schemafluxtest.New().Reply(emptyRequired))
		if _, err := schemaflux.Extracting[devx2Pair]("x").ExactFields().Run(context.Background()); err != nil {
			t.Errorf("ExactFields also required non-empty fields, which is the bundle it exists to undo: %v", err)
		}
	})

	t.Run("CompleteFields alone", func(t *testing.T) {
		schemafluxtest.Install(t, schemafluxtest.New().Reply(emptyRequired))
		_, err := schemaflux.Extracting[devx2Pair]("x").CompleteFields().Run(context.Background())
		if err == nil {
			t.Error("CompleteFields accepted an empty required field")
		}

		// The counterpart: tolerating an unrecognised property is the whole
		// reason a caller reaches for this half instead of Strict. On a batched
		// operation the bundle discards every good answer over one key nobody
		// would have read.
		schemafluxtest.Install(t, schemafluxtest.New().Reply(extraField))
		if _, err := schemaflux.Extracting[devx2Doc]("x").CompleteFields().Run(context.Background()); err != nil {
			t.Errorf("CompleteFields also rejected an unknown field, which is the bundle it exists to undo: %v", err)
		}
	})

	t.Run("Strict still means both", func(t *testing.T) {
		schemafluxtest.Install(t, schemafluxtest.New().Reply(extraField))
		if _, err := schemaflux.Extracting[devx2Doc]("x").Strict().Run(context.Background()); err == nil {
			t.Error("Strict stopped rejecting unknown fields")
		}

		schemafluxtest.Install(t, schemafluxtest.New().Reply(emptyRequired))
		if _, err := schemaflux.Extracting[devx2Pair]("x").Strict().Run(context.Background()); err == nil {
			t.Error("Strict stopped requiring non-empty fields")
		}
	})
}

// Each half keeps the error message that names its remedy (DX-003, DX-004).
// Separating the rules must not separate a caller from the way out of them.
func TestEachHalfKeepsItsRemedy(t *testing.T) {
	t.Run("ExactFields names Strict and the drop", func(t *testing.T) {
		schemafluxtest.Install(t, schemafluxtest.New().Reply(`{"title":"ok","confidence":0.91}`))
		_, err := schemaflux.Extracting[devx2Doc]("x").ExactFields().Run(context.Background())
		if err == nil {
			t.Fatal("accepted an unknown field")
		}
		if !strings.Contains(err.Error(), "confidence") {
			t.Errorf("the error does not name the field: %v", err)
		}
	})

	t.Run("CompleteFields names the optional spellings", func(t *testing.T) {
		schemafluxtest.Install(t, schemafluxtest.New().Reply(`{"present":"here","absent":""}`))
		_, err := schemaflux.Extracting[devx2Pair]("x").CompleteFields().Run(context.Background())
		if err == nil {
			t.Fatal("accepted an empty required field")
		}
		for _, want := range []string{`schemaflux:"optional"`, "pointer", "omitempty"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not mention %q: %v", want, err)
			}
		}
	})
}

// --- DX-008: the caller's deadline is the budget -----------------------------

// A deadline the caller set reaches the provider intact, on every operation.
//
// This was neither honoured nor uniformly ignored, which is the worst of the
// three possibilities. Extract and Generate route through the generic op runner
// and never had their context touched, so a 90-second budget arrived as 90
// seconds; Summarize and Choose went through operationContext, which layered
// the 30-second default on top, and context.WithTimeout takes the EARLIER of
// the two. Same library, same call shape, two different answers to "how long do
// I get", and nothing anywhere saying which applied.
//
// The numbers below are the real ones from the report: ArticleFlux runs its
// interest pass on a deliberate 90-second budget, raised from 20 after the
// entity pass timed out on every call, and its digest path was being cut to 30
// without a word.
func TestACallersDeadlineReachesTheProviderIntact(t *testing.T) {
	const asked = 90 * time.Second

	cases := []struct {
		name string
		run  func(ctx context.Context)
	}{
		{"Extracting", func(ctx context.Context) {
			_, _ = schemaflux.Extracting[devx2Doc]("x").Run(ctx)
		}},
		{"Generating", func(ctx context.Context) {
			_, _ = schemaflux.Generating[string]("x").Run(ctx)
		}},
		{"Summarizing", func(ctx context.Context) {
			_, _ = schemaflux.Summarizing("some text").Run(ctx)
		}},
		{"Choosing", func(ctx context.Context) {
			_, _ = schemaflux.Choosing([]string{"a", "b"}).Run(ctx)
		}},
		{"Rewriting", func(ctx context.Context) {
			_, _ = schemaflux.Rewriting("some text").Run(ctx)
		}},
		{"Translating", func(ctx context.Context) {
			_, _ = schemaflux.Translating("some text").To("fr").Run(ctx)
		}},
		{"Classifying", func(ctx context.Context) {
			_, _ = schemaflux.Classifying[string, string]("x").Categories("a", "b").Run(ctx)
		}},
		{"Scoring", func(ctx context.Context) {
			_, _ = schemaflux.Scoring("x").Run(ctx)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := schemafluxtest.New().Shaped()
			schemafluxtest.Install(t, p)

			ctx, cancel := context.WithTimeout(context.Background(), asked)
			defer cancel()
			tc.run(ctx)

			if p.CallCount() == 0 {
				t.Fatal("the operation never reached the provider, so this proves nothing")
			}
			deadline, ok := p.ContextN(0).Deadline()
			if !ok {
				t.Fatal("no deadline reached the provider at all")
			}
			// Generous: the assertion is that the caller's budget survived
			// rather than being replaced by the library's 30-second default,
			// not that no time passed.
			if left := time.Until(deadline); left < asked-10*time.Second {
				t.Errorf("the provider saw %s of a %s budget; the library's default replaced "+
					"the caller's deadline instead of deferring to it", left.Round(time.Second), asked)
			}
		})
	}
}

// The default still applies when the caller expressed no opinion. It is a floor
// for callers who said nothing, not a ceiling on callers who said something --
// removing it entirely would leave an unbounded call hanging on a provider that
// never answers.
func TestTheDefaultTimeoutStillAppliesWithoutACallerDeadline(t *testing.T) {
	p := schemafluxtest.New().Shaped()
	schemafluxtest.Install(t, p)

	_, _ = schemaflux.Summarizing("some text").Run(context.Background())

	if p.CallCount() == 0 {
		t.Fatal("the operation never reached the provider")
	}
	if _, ok := p.ContextN(0).Deadline(); !ok {
		t.Error("a call with no caller deadline reached the provider unbounded; the default " +
			"timeout is what stops a call hanging forever on a provider that never answers")
	}
}

// A caller's SHORTER deadline was always honoured and still is. The fix moved
// only the case where the caller asked for more than the default.
func TestAShorterCallerDeadlineIsStillHonoured(t *testing.T) {
	p := schemafluxtest.New().Shaped()
	schemafluxtest.Install(t, p)

	short := 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), short)
	defer cancel()
	_, _ = schemaflux.Summarizing("some text").Run(ctx)

	deadline, ok := p.ContextN(0).Deadline()
	if !ok {
		t.Fatal("no deadline reached the provider")
	}
	if left := time.Until(deadline); left > short {
		t.Errorf("the provider saw %s of a %s budget; a caller asking for LESS than the default "+
			"must still get less", left.Round(time.Second), short)
	}
}
