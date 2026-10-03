package ops

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/config"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// flatRecorder is a provider that records the request it was sent and answers
// with an object no operation can use, which is enough: these tests read what
// reached the provider and not what the operation made of the answer.
type flatRecorder struct {
	mu   sync.Mutex
	reqs []llm.CompletionRequest
}

func (p *flatRecorder) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	return llm.CompletionResponse{Content: `{}`}, nil
}

func (p *flatRecorder) Name() string                               { return "local" }
func (p *flatRecorder) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *flatRecorder) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// only returns the request of the one call the operation made.
func (p *flatRecorder) only(t *testing.T) llm.CompletionRequest {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) != 1 {
		t.Fatalf("the provider was called %d times, want 1", len(p.reqs))
	}
	return p.reqs[0]
}

// flatKnobs are the two settings the eleven flat options types gained.
type flatKnobs struct {
	tokens int
	search bool
}

// flatCase is one of the eleven operations whose options spell the common
// fields out. The closures hide the options type, so one table walks them all.
type flatCase struct {
	name string
	// tier is the tier the operation asks for when its options name none, which
	// fixes the output ceiling a request carries when MaxOutputTokens is zero.
	tier types.Speed
	// single runs the operation with one options value holding the knobs.
	single func(ctx context.Context, k flatKnobs) error
	// merged runs it with the value the type's own merge makes of the knobs, in
	// order, on top of the type's constructor.
	merged func(ctx context.Context, ks ...flatKnobs) error
	// mergedKnobs is what the merge of the knobs holds, with no operation run.
	mergedKnobs func(ks ...flatKnobs) flatKnobs
}

// newFlatCase builds a flatCase from the parts that differ between the types.
// literal builds the options with the settings the operation needs to accept
// them, and the knobs; merge is the type's merge function; read gives the knobs
// back out of an options value.
func newFlatCase[O any](name string, tier types.Speed, defaults func() O,
	literal func(ctx context.Context, k flatKnobs) O, merge func(defaults, user O) O,
	run func(O) error, read func(O) flatKnobs) flatCase {
	mergeAll := func(ctx context.Context, ks ...flatKnobs) O {
		out := defaults()
		for _, k := range ks {
			out = merge(out, literal(ctx, k))
		}
		return out
	}
	return flatCase{
		name: name,
		tier: tier,
		single: func(ctx context.Context, k flatKnobs) error {
			return run(literal(ctx, k))
		},
		merged: func(ctx context.Context, ks ...flatKnobs) error {
			return run(mergeAll(ctx, ks...))
		},
		mergedKnobs: func(ks ...flatKnobs) flatKnobs {
			return read(mergeAll(context.Background(), ks...))
		},
	}
}

type flatRecord struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

var flatRecords = []flatRecord{{Name: "a", Value: 1}, {Name: "b", Value: 2}}

func flatCases() []flatCase {
	return []flatCase{
		newFlatCase("Arbitrate", types.Fast, NewArbitrateOptions,
			func(ctx context.Context, k flatKnobs) ArbitrateOptions {
				return ArbitrateOptions{Rules: []string{"cheaper is better"}, Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeArbitrateOptions,
			func(o ArbitrateOptions) error { _, err := Arbitrate(flatRecords, o); return err },
			func(o ArbitrateOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Audit", types.Smart, NewAuditOptions,
			func(ctx context.Context, k flatKnobs) AuditOptions {
				return AuditOptions{Policies: []string{"no secrets"}, Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeAuditOptions,
			func(o AuditOptions) error { _, err := Audit(flatRecords[0], o); return err },
			func(o AuditOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Assemble", types.Smart, NewComposeOptions,
			func(ctx context.Context, k flatKnobs) ComposeOptions {
				return ComposeOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeComposeOptions,
			func(o ComposeOptions) error {
				_, err := Assemble[flatRecord]([]any{flatRecords[0], flatRecords[1]}, o)
				return err
			},
			func(o ComposeOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Conform", types.Fast, NewConformOptions,
			func(ctx context.Context, k flatKnobs) ConformOptions {
				return ConformOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeConformOptions,
			func(o ConformOptions) error { _, err := Conform(flatRecords[0], "ISO8601", o); return err },
			func(o ConformOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Derive", types.Fast, NewDeriveOptions,
			func(ctx context.Context, k flatKnobs) DeriveOptions {
				return DeriveOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeDeriveOptions,
			func(o DeriveOptions) error { _, err := Derive[flatRecord, flatRecord](flatRecords[0], o); return err },
			func(o DeriveOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Interpolate", types.Fast, NewInterpolateOptions,
			func(ctx context.Context, k flatKnobs) InterpolateOptions {
				return InterpolateOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeInterpolateOptions,
			func(o InterpolateOptions) error { _, err := Interpolate(flatRecords, o); return err },
			func(o InterpolateOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Settle", types.Fast, NewNegotiateOptions,
			func(ctx context.Context, k flatKnobs) NegotiateOptions {
				return NegotiateOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeNegotiateOptions,
			func(o NegotiateOptions) error {
				_, err := Settle[flatRecord](map[string]any{"price": "low"}, o)
				return err
			},
			func(o NegotiateOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("SettleAdversarial", types.Fast, NewAdversarialOptions,
			func(ctx context.Context, k flatKnobs) AdversarialOptions {
				return AdversarialOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeAdversarialOptions,
			func(o AdversarialOptions) error {
				_, err := SettleAdversarial(AdversarialContext[flatRecord]{
					Ours:        AdversarialPosition[flatRecord]{Position: flatRecords[0]},
					Theirs:      AdversarialPosition[flatRecord]{Position: flatRecords[1]},
					OurLeverage: "balanced",
				}, o)
				return err
			},
			func(o AdversarialOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Pivot", types.Smart, NewPivotOptions,
			func(ctx context.Context, k flatKnobs) PivotOptions {
				return PivotOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergePivotOptions,
			func(o PivotOptions) error { _, err := Pivot[flatRecord, flatRecord](flatRecords[0], o); return err },
			func(o PivotOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Project", types.Fast, NewProjectOptions,
			func(ctx context.Context, k flatKnobs) ProjectOptions {
				return ProjectOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeProjectOptions,
			func(o ProjectOptions) error { _, err := Project[flatRecord, flatRecord](flatRecords[0], o); return err },
			func(o ProjectOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
		newFlatCase("Resolve", types.Fast, NewResolveOptions,
			func(ctx context.Context, k flatKnobs) ResolveOptions {
				return ResolveOptions{Context: ctx, MaxOutputTokens: k.tokens, WebSearch: k.search}
			},
			mergeResolveOptions,
			func(o ResolveOptions) error { _, err := Resolve(flatRecords, o); return err },
			func(o ResolveOptions) flatKnobs { return flatKnobs{o.MaxOutputTokens, o.WebSearch} }),
	}
}

// A caller's output ceiling and web-search flag reach the request of each of
// the eleven operations that build their options by hand. Neither used to: the
// types had no field for them, so there was nothing to set, and a value a
// caller put on a type that has them elsewhere was dropped on the way.
func TestFlatOptionsCarryTheOutputCeilingAndWebSearch(t *testing.T) {
	cases := flatCases()
	if len(cases) != 11 {
		t.Fatalf("%d operations in the table, want the eleven flat options types", len(cases))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Run("one value", func(t *testing.T) {
				for _, k := range []flatKnobs{{777, false}, {0, true}, {3000, true}} {
					provider := &flatRecorder{}
					ctx := WithProvider(context.Background(), provider)
					_ = c.single(ctx, k)
					req := provider.only(t)
					wantTokens := k.tokens
					if wantTokens == 0 {
						wantTokens = config.GetMaxTokens(c.tier)
					}
					if req.MaxTokens != wantTokens {
						t.Errorf("%+v: MaxTokens = %d, want %d", k, req.MaxTokens, wantTokens)
					}
					if req.WebSearch != k.search {
						t.Errorf("%+v: WebSearch = %v, want %v", k, req.WebSearch, k.search)
					}
				}
			})
			t.Run("two merged", func(t *testing.T) {
				for _, k := range [][]flatKnobs{
					{{500, false}, {0, true}},
					{{0, true}, {900, false}},
					{{500, false}, {900, false}},
					{{500, true}, {0, false}},
				} {
					provider := &flatRecorder{}
					ctx := WithProvider(context.Background(), provider)
					_ = c.merged(ctx, k...)
					req := provider.only(t)
					want := c.mergedKnobs(k...)
					if req.MaxTokens != want.tokens || req.WebSearch != want.search {
						t.Errorf("%+v: the request carries (%d, %v), the merge holds %+v", k, req.MaxTokens, req.WebSearch, want)
					}
					if want.tokens == 0 || (!want.search && (k[0].search || k[1].search)) {
						t.Errorf("%+v: the merge lost a setting: %+v", k, want)
					}
				}
			})
			t.Run("neither set", func(t *testing.T) {
				provider := &flatRecorder{}
				ctx := WithProvider(context.Background(), provider)
				_ = c.single(ctx, flatKnobs{})
				req := provider.only(t)
				if req.MaxTokens != config.GetMaxTokens(c.tier) {
					t.Errorf("MaxTokens = %d, want the %v tier's %d", req.MaxTokens, c.tier, config.GetMaxTokens(c.tier))
				}
				if req.WebSearch {
					t.Error("WebSearch is set on a request whose options did not ask for it")
				}
			})
		})
	}
}

// The merge of each type: the ceiling is the last non-zero one and web search,
// once on, stays on. Twelve combinations of three values, for each of the
// eleven merges and the one Pivot and Project share.
func TestFlatOptionMergesCarryTheTwoSettings(t *testing.T) {
	steps := []struct {
		name string
		in   []flatKnobs
		want flatKnobs
	}{
		{"nothing", []flatKnobs{{}, {}, {}}, flatKnobs{}},
		{"one ceiling", []flatKnobs{{100, false}}, flatKnobs{100, false}},
		{"the last ceiling wins", []flatKnobs{{100, false}, {200, false}, {300, false}}, flatKnobs{300, false}},
		{"a zero ceiling keeps the earlier", []flatKnobs{{100, false}, {0, false}}, flatKnobs{100, false}},
		{"a zero ceiling in the middle", []flatKnobs{{100, false}, {0, false}, {0, false}}, flatKnobs{100, false}},
		{"a later ceiling over a zero one", []flatKnobs{{0, false}, {250, false}}, flatKnobs{250, false}},
		{"web search on", []flatKnobs{{0, true}}, flatKnobs{0, true}},
		{"web search stays on", []flatKnobs{{0, true}, {0, false}}, flatKnobs{0, true}},
		{"web search on later", []flatKnobs{{0, false}, {0, true}}, flatKnobs{0, true}},
		{"both", []flatKnobs{{64, true}, {128, false}}, flatKnobs{128, true}},
		{"both in different values", []flatKnobs{{64, false}, {0, true}}, flatKnobs{64, true}},
		{"a negative ceiling is not a ceiling", []flatKnobs{{100, false}, {-5, false}}, flatKnobs{100, false}},
	}
	for _, c := range flatCases() {
		for _, step := range steps {
			if got := c.mergedKnobs(step.in...); got != step.want {
				t.Errorf("%s, %s: merge holds %+v, want %+v", c.name, step.name, got, step.want)
			}
		}
	}
}

// Pivot and Project take the ceiling through the lock a client may put on a
// context: one above it is refused before a request is made, as it is for the
// options types that have always been checked against the lock, and one at it
// is let through.
func TestPivotAndProjectRefuseACeilingAboveTheLock(t *testing.T) {
	provider := &flatRecorder{}
	ctx := WithLockedLimits(WithProvider(context.Background(), provider), LockedLimits{MaxOutputTokens: 100})
	const refused = "locked policy: max output tokens 500 exceeds the client-scope ceiling of 100"

	_, pivotErr := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{Context: ctx, MaxOutputTokens: 500})
	_, projectErr := Project[flatRecord, flatRecord](flatRecords[0], ProjectOptions{Context: ctx, MaxOutputTokens: 500})
	for name, err := range map[string]error{"Pivot": pivotErr, "Project": projectErr} {
		if err == nil || err.Error() != refused {
			t.Errorf("%s: error = %v, want %q", name, err, refused)
			continue
		}
		if got := types.KindOf(err); got != types.KindConfiguration {
			t.Errorf("%s: kind = %v, want %v", name, got, types.KindConfiguration)
		}
	}
	provider.mu.Lock()
	calls := len(provider.reqs)
	provider.mu.Unlock()
	if calls != 0 {
		t.Errorf("%d requests were made for a refused ceiling", calls)
	}

	_, _ = Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{Context: ctx, MaxOutputTokens: 100})
	_, _ = Project[flatRecord, flatRecord](flatRecords[0], ProjectOptions{Context: ctx, MaxOutputTokens: 100})
	provider.mu.Lock()
	calls = len(provider.reqs)
	provider.mu.Unlock()
	if calls != 2 {
		t.Errorf("%d requests were made for a ceiling at the lock, want 2", calls)
	}
}

// Every one of the eleven flat options types is read against the lock a client
// may put on a context: a ceiling above it, or a mode weaker than its floor, is
// refused before a request is made, with a configuration error that says which
// lock it broke; a ceiling at it is let through. Pivot and Project did this
// already, and the other nine did not look at the lock at all.
func TestEveryFlatOptionsTypeIsReadAgainstTheLock(t *testing.T) {
	const aboveCeiling = "locked policy: max output tokens 500 exceeds the client-scope ceiling of 100"
	const belowFloor = "locked policy: mode transform is weaker than the client-scope floor of strict"
	for _, c := range flatCases() {
		t.Run(c.name, func(t *testing.T) {
			t.Run("a ceiling above the lock", func(t *testing.T) {
				provider := &flatRecorder{}
				ctx := WithLockedLimits(WithProvider(context.Background(), provider), LockedLimits{MaxOutputTokens: 100})
				err := c.single(ctx, flatKnobs{tokens: 500})
				if err == nil || err.Error() != aboveCeiling {
					t.Fatalf("error = %v, want %q", err, aboveCeiling)
				}
				if types.KindOf(err) != types.KindConfiguration || !errors.Is(err, types.ErrConfiguration) {
					t.Errorf("the refusal is not a configuration error: kind %v", types.KindOf(err))
				}
				if len(provider.reqs) != 0 {
					t.Errorf("%d requests were made for a refused ceiling", len(provider.reqs))
				}
			})
			t.Run("a ceiling above the lock, through a merge", func(t *testing.T) {
				provider := &flatRecorder{}
				ctx := WithLockedLimits(WithProvider(context.Background(), provider), LockedLimits{MaxOutputTokens: 100})
				err := c.merged(ctx, flatKnobs{tokens: 50}, flatKnobs{tokens: 500})
				if err == nil || err.Error() != aboveCeiling {
					t.Fatalf("error = %v, want %q", err, aboveCeiling)
				}
				if len(provider.reqs) != 0 {
					t.Errorf("%d requests were made for a refused ceiling", len(provider.reqs))
				}
			})
			t.Run("a ceiling at the lock", func(t *testing.T) {
				provider := &flatRecorder{}
				ctx := WithLockedLimits(WithProvider(context.Background(), provider), LockedLimits{MaxOutputTokens: 100})
				_ = c.single(ctx, flatKnobs{tokens: 100})
				if req := provider.only(t); req.MaxTokens != 100 {
					t.Errorf("MaxTokens = %d, want 100", req.MaxTokens)
				}
			})
			t.Run("no ceiling asked for under a lock", func(t *testing.T) {
				provider := &flatRecorder{}
				ctx := WithLockedLimits(WithProvider(context.Background(), provider), LockedLimits{MaxOutputTokens: 100})
				_ = c.single(ctx, flatKnobs{})
				provider.only(t)
			})
			t.Run("a mode weaker than the floor", func(t *testing.T) {
				provider := &flatRecorder{}
				ctx := WithLockedLimits(WithProvider(context.Background(), provider), LockedLimits{MinMode: types.Strict})
				err := c.single(ctx, flatKnobs{})
				if err == nil || err.Error() != belowFloor {
					t.Fatalf("error = %v, want %q", err, belowFloor)
				}
				if types.KindOf(err) != types.KindConfiguration || !errors.Is(err, types.ErrConfiguration) {
					t.Errorf("the refusal is not a configuration error: kind %v", types.KindOf(err))
				}
				if len(provider.reqs) != 0 {
					t.Errorf("%d requests were made for a refused mode", len(provider.reqs))
				}
			})
			t.Run("no lock", func(t *testing.T) {
				provider := &flatRecorder{}
				ctx := WithProvider(context.Background(), provider)
				_ = c.single(ctx, flatKnobs{tokens: 100000})
				provider.only(t)
			})
		})
	}
}

// checkLockedLimitsOf names what it refused as a configuration error, once, at
// the source, and keeps the words callers read.
func TestCheckLockedLimitsOfClassifiesWhatItRefuses(t *testing.T) {
	lock := func(l LockedLimits) context.Context { return WithLockedLimits(context.Background(), l) }
	cases := []struct {
		name string
		opt  types.OpOptions
		want string
	}{
		{"no context", types.OpOptions{MaxOutputTokens: 9}, ""},
		{"a context with no lock", types.OpOptions{Context: context.Background(), MaxOutputTokens: 9}, ""},
		{"an empty lock", types.OpOptions{Context: lock(LockedLimits{}), MaxOutputTokens: 9, Mode: types.Creative}, ""},
		{"a ceiling above the lock", types.OpOptions{Context: lock(LockedLimits{MaxOutputTokens: 100}), MaxOutputTokens: 101},
			"locked policy: max output tokens 101 exceeds the client-scope ceiling of 100"},
		{"a ceiling at the lock", types.OpOptions{Context: lock(LockedLimits{MaxOutputTokens: 100}), MaxOutputTokens: 100}, ""},
		{"no ceiling under a lock", types.OpOptions{Context: lock(LockedLimits{MaxOutputTokens: 100})}, ""},
		{"a mode weaker than the floor", types.OpOptions{Context: lock(LockedLimits{MinMode: types.Strict}), Mode: types.Creative},
			"locked policy: mode creative is weaker than the client-scope floor of strict"},
		{"a mode at the floor", types.OpOptions{Context: lock(LockedLimits{MinMode: types.TransformMode}), Mode: types.TransformMode}, ""},
		{"a mode stricter than the floor", types.OpOptions{Context: lock(LockedLimits{MinMode: types.Creative}), Mode: types.Strict}, ""},
		{"no mode asked for", types.OpOptions{Context: lock(LockedLimits{MinMode: types.Strict})}, ""},
		{"both broken, the ceiling is named", types.OpOptions{Context: lock(LockedLimits{MaxOutputTokens: 10, MinMode: types.Strict}), MaxOutputTokens: 11, Mode: types.Creative},
			"locked policy: max output tokens 11 exceeds the client-scope ceiling of 10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkLockedLimitsOf(tc.opt)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if got := types.KindOf(err); got != types.KindConfiguration {
				t.Errorf("kind = %v, want configuration", got)
			}
			if !errors.Is(err, types.ErrConfiguration) {
				t.Error("errors.Is(err, ErrConfiguration) is false")
			}
			if errors.Is(err, types.ErrInvalidRequest) {
				t.Error("the refusal also matches invalid request")
			}
		})
	}
}

// The merges of Assemble, Conform and Derive carry the caller's request and
// correlation identifiers, as every other flat merge does: they were left out,
// so an identifier a caller set never reached the call.
func TestComposeConformAndDeriveMergesCarryTheIdentifiers(t *testing.T) {
	type ids struct{ request, correlation string }
	cases := []struct {
		name           string
		defaults, user ids
		want           ids
	}{
		{"neither side sets one", ids{}, ids{}, ids{}},
		{"the user sets both", ids{}, ids{"r1", "c1"}, ids{"r1", "c1"}},
		{"the user sets the request only", ids{}, ids{"r1", ""}, ids{"r1", ""}},
		{"the user sets the correlation only", ids{}, ids{"", "c1"}, ids{"", "c1"}},
		{"the defaults hold when the user is silent", ids{"r0", "c0"}, ids{}, ids{"r0", "c0"}},
		{"the user overrides both", ids{"r0", "c0"}, ids{"r1", "c1"}, ids{"r1", "c1"}},
		{"the user overrides the request only", ids{"r0", "c0"}, ids{"r1", ""}, ids{"r1", "c0"}},
		{"the user overrides the correlation only", ids{"r0", "c0"}, ids{"", "c1"}, ids{"r0", "c1"}},
		{"the defaults carry a request alone", ids{"r0", ""}, ids{"", "c1"}, ids{"r0", "c1"}},
		{"an identifier is kept as it is written", ids{}, ids{" r 1 ", "c 1"}, ids{" r 1 ", "c 1"}},
	}
	merges := map[string]func(defaults, user ids) ids{
		"compose": func(d, u ids) ids {
			got := mergeComposeOptions(ComposeOptions{RequestID: d.request, CorrelationID: d.correlation}, ComposeOptions{RequestID: u.request, CorrelationID: u.correlation})
			return ids{got.RequestID, got.CorrelationID}
		},
		"conform": func(d, u ids) ids {
			got := mergeConformOptions(ConformOptions{RequestID: d.request, CorrelationID: d.correlation}, ConformOptions{RequestID: u.request, CorrelationID: u.correlation})
			return ids{got.RequestID, got.CorrelationID}
		},
		"derive": func(d, u ids) ids {
			got := mergeDeriveOptions(DeriveOptions{RequestID: d.request, CorrelationID: d.correlation}, DeriveOptions{RequestID: u.request, CorrelationID: u.correlation})
			return ids{got.RequestID, got.CorrelationID}
		},
	}
	for name, merge := range merges {
		for _, tc := range cases {
			t.Run(name+": "+tc.name, func(t *testing.T) {
				if got := merge(tc.defaults, tc.user); got != tc.want {
					t.Errorf("merge holds %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}
