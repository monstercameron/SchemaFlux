package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A refusal is an error an operation returns before it asks a model anything:
// the caller's arguments or options cannot be used. Each carries a kind, and
// the text and the chain of an error that had none before it was given one are
// what they were: a caller may already match on the text, and errors.As on the
// error underneath still finds it.
type refusal struct {
	name string
	call func() error
	// text is the error's message. For an error that had no kind it was read from
	// the operation before the kind was added.
	text string
	kind types.ErrorKind
	// sentinel is what errors.Is has to match: the sentinel of the kind.
	sentinel error
	// underneath, when set, is a check on the chain that the error keeps.
	underneath func(error) bool
}

var (
	// refuseChan is an argument encoding/json cannot write.
	refuseChan = func() chan int { return make(chan int) }

	isUnsupportedType = func(err error) bool {
		var unsupported *json.UnsupportedTypeError
		return errors.As(err, &unsupported)
	}
)

// checkRefusals runs every case with no provider reachable and checks the text,
// the kind, the sentinel and the chain of what comes back.
func checkRefusals(t *testing.T, cases []refusal) {
	t.Helper()
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		t.Error("a model was asked, and the call was to be refused before that")
		return "", errors.New("no model")
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("no error")
			}
			if got := err.Error(); got != tc.text {
				t.Errorf("text = %q, want %q", got, tc.text)
			}
			if got := types.KindOf(err); got != tc.kind {
				t.Errorf("kind = %v, want %v", got, tc.kind)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(err, %v) is false", tc.sentinel)
			}
			if tc.underneath != nil && !tc.underneath(err) {
				t.Error("the error underneath is no longer in the chain")
			}
		})
	}
}

const marshalChan = ": json: unsupported type: chan int"

func TestArbitrateRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	rules := []string{"cheaper is better"}
	two := []string{"a", "b"}
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	checkRefusals(t, []refusal{
		{"no options", func() error { _, err := Arbitrate([]string{}, ArbitrateOptions{Rules: rules}); return err },
			"arbitrate: configuration: there are no options to arbitrate between", cfg, types.ErrConfiguration, nil},
		{"two option values", func() error {
			_, err := Arbitrate(two, ArbitrateOptions{Rules: rules}, ArbitrateOptions{Rules: rules})
			return err
		}, "arbitrate: configuration: 2 option values were given, and only one is read", cfg, types.ErrConfiguration, nil},
		{"a blank rule", func() error { _, err := Arbitrate(two, ArbitrateOptions{Rules: []string{" "}}); return err },
			"arbitrate: configuration: rule 1 is blank", cfg, types.ErrConfiguration, nil},
		{"more weights than rules", func() error {
			_, err := Arbitrate(two, ArbitrateOptions{Rules: rules, Weights: []float64{1, 1}})
			return err
		}, "arbitrate: configuration: 2 weights were given for 1 rules", cfg, types.ErrConfiguration, nil},
		{"a negative weight", func() error {
			_, err := Arbitrate(two, ArbitrateOptions{Rules: rules, Weights: []float64{-1}})
			return err
		}, "arbitrate: configuration: the weight of rule 1 has to be a number that is not negative", cfg, types.ErrConfiguration, nil},
		{"a tiebreaker nobody knows", func() error {
			_, err := Arbitrate(two, ArbitrateOptions{Rules: rules, Tiebreaker: "never"})
			return err
		}, "arbitrate: configuration: the tiebreaker has to be one of first, random or most-confident", cfg, types.ErrConfiguration, nil},
		{"no rules", func() error { _, err := Arbitrate(two, ArbitrateOptions{}); return err },
			"arbitrate: configuration: at least one rule is required", cfg, types.ErrConfiguration, nil},
		{"an option that cannot be written as JSON", func() error {
			_, err := Arbitrate([]chan int{refuseChan(), refuseChan()}, ArbitrateOptions{Rules: rules})
			return err
		}, "arbitrate: invalid request: the request could not be written as JSON", inv, types.ErrInvalidRequest, nil},
	})
}

func TestAuditRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	policies := []string{"no secrets"}
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	checkRefusals(t, []refusal{
		{"two option values", func() error {
			_, err := Audit(flatRecords[0], AuditOptions{Policies: policies}, AuditOptions{Policies: policies})
			return err
		}, "audit: configuration: 2 option values were given, and only one is read", cfg, types.ErrConfiguration, nil},
		{"a threshold that is not a number", func() error {
			_, err := Audit(flatRecords[0], AuditOptions{Threshold: math.NaN()})
			return err
		}, "audit: configuration: Threshold has to be a severity from 0 to 1", cfg, types.ErrConfiguration, nil},
		{"a threshold above one", func() error { _, err := Audit(flatRecords[0], AuditOptions{Threshold: 1.5}); return err },
			"audit: configuration: Threshold has to be a severity from 0 to 1", cfg, types.ErrConfiguration, nil},
		{"a negative threshold", func() error { _, err := Audit(flatRecords[0], AuditOptions{Threshold: -0.1}); return err },
			"audit: configuration: Threshold has to be a severity from 0 to 1", cfg, types.ErrConfiguration, nil},
		{"deep and shallow together", func() error {
			_, err := Audit(flatRecords[0], AuditOptions{Deep: true, Shallow: true})
			return err
		}, "audit: configuration: Deep and Shallow are both set, and they ask for opposite things", cfg, types.ErrConfiguration, nil},
		{"data that cannot be written as JSON", func() error {
			_, err := Audit(refuseChan(), AuditOptions{Policies: policies})
			return err
		}, "audit: invalid request: the request could not be written as JSON", inv, types.ErrInvalidRequest, nil},
	})
}

func TestComposeRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	checkRefusals(t, []refusal{
		{"a merge strategy nobody knows", func() error {
			_, err := Assemble[flatRecord]([]any{flatRecords[0]}, ComposeOptions{MergeStrategy: "bogus"})
			return err
		}, `compose: configuration: the merge strategy must be "first", "last", "combine" or "smart"`, cfg, types.ErrConfiguration, nil},
		{"a part that cannot be written as JSON", func() error {
			_, err := Assemble[flatRecord]([]any{flatRecords[0], refuseChan()})
			return err
		}, "failed to marshal part 1" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"the first part cannot be written either", func() error {
			_, err := Assemble[flatRecord]([]any{refuseChan()}, ComposeOptions{MergeStrategy: "last"})
			return err
		}, "failed to marshal part 0" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

func TestConformRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	checkRefusals(t, []refusal{
		{"an empty standard", func() error { _, err := Conform(flatRecords[0], ""); return err },
			"standard cannot be empty", cfg, types.ErrConfiguration, nil},
		{"an empty standard with options", func() error { _, err := Conform(flatRecords[0], "", NewConformOptions()); return err },
			"standard cannot be empty", cfg, types.ErrConfiguration, nil},
		{"an input that cannot be written as JSON", func() error { _, err := Conform(refuseChan(), "ISO8601"); return err },
			"failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"an input that cannot be written, strictly", func() error {
			_, err := Conform(refuseChan(), "USPS", ConformOptions{Strict: true})
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

func TestDeriveRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	floor := "derive: configuration: MinConfidence has to be a number from 0 to 1"
	checkRefusals(t, []refusal{
		{"a floor that is not a number", func() error {
			_, err := Derive[flatRecord, flatRecord](flatRecords[0], DeriveOptions{MinConfidence: math.NaN()})
			return err
		}, floor, cfg, types.ErrConfiguration, nil},
		{"a floor above one", func() error {
			_, err := Derive[flatRecord, flatRecord](flatRecords[0], DeriveOptions{MinConfidence: 1.5})
			return err
		}, floor, cfg, types.ErrConfiguration, nil},
		{"a negative floor", func() error {
			_, err := Derive[flatRecord, flatRecord](flatRecords[0], DeriveOptions{MinConfidence: -0.5})
			return err
		}, floor, cfg, types.ErrConfiguration, nil},
		{"an input that cannot be written as JSON", func() error {
			_, err := Derive[chan int, flatRecord](refuseChan())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"an input that cannot be written, with options", func() error {
			_, err := Derive[chan int, flatRecord](refuseChan(), NewDeriveOptions())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

func TestInterpolateRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	checkRefusals(t, []refusal{
		{"a negative window", func() error {
			_, err := Interpolate(flatRecords, InterpolateOptions{ContextWindow: -1})
			return err
		}, "interpolate: configuration: ContextWindow cannot be negative", cfg, types.ErrConfiguration, nil},
		{"no items", func() error { _, err := Interpolate([]flatRecord{}); return err },
			"interpolate: invalid request: no items to interpolate", inv, types.ErrInvalidRequest, nil},
		{"a method nobody knows", func() error {
			_, err := Interpolate(flatRecords, InterpolateOptions{Method: "cubic"})
			return err
		}, "interpolate: invalid request: the method is not one of auto, linear, trend, semantic or pattern", inv, types.ErrInvalidRequest, nil},
		{"a gap beyond the items", func() error {
			_, err := Interpolate(flatRecords, InterpolateOptions{GapIndices: []int{5}})
			return err
		}, "interpolate: invalid request: gap index 5 is outside the sequence of 2 items", inv, types.ErrInvalidRequest, nil},
		{"a negative gap", func() error {
			_, err := Interpolate(flatRecords, InterpolateOptions{GapIndices: []int{-1}})
			return err
		}, "interpolate: invalid request: gap index -1 is outside the sequence of 2 items", inv, types.ErrInvalidRequest, nil},
		{"items that cannot be written as JSON", func() error {
			_, err := Interpolate([]chan int{refuseChan(), refuseChan()})
			return err
		}, "interpolate: invalid request: the items could not be written as JSON", inv, types.ErrInvalidRequest, nil},
	})
}

func TestNegotiateRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	two := "negotiate: configuration: 2 option values were given, and only one is read"
	checkRefusals(t, []refusal{
		{"Settle with two option values", func() error {
			_, err := Settle[flatRecord](map[string]any{"a": 1}, NegotiateOptions{}, NegotiateOptions{})
			return err
		}, two, cfg, types.ErrConfiguration, nil},
		{"Settle with a minimum that is not a number", func() error {
			_, err := Settle[flatRecord](map[string]any{"a": 1}, NegotiateOptions{MinSatisfaction: math.NaN()})
			return err
		}, "negotiate: configuration: MinSatisfaction has to be a number from 0 to 1", cfg, types.ErrConfiguration, nil},
		{"Settle with a negative number of alternatives", func() error {
			_, err := Settle[flatRecord](map[string]any{"a": 1}, NegotiateOptions{MaxAlternatives: -1})
			return err
		}, "negotiate: configuration: MaxAlternatives cannot be negative", cfg, types.ErrConfiguration, nil},
		{"Settle with constraints that cannot be written as JSON", func() error {
			_, err := Settle[flatRecord](refuseChan())
			return err
		}, "negotiate: invalid request: the constraints could not be written as JSON", inv, types.ErrInvalidRequest, nil},
		{"SettleAdversarial with two option values", func() error {
			_, err := SettleAdversarial(AdversarialContext[flatRecord]{}, AdversarialOptions{}, AdversarialOptions{})
			return err
		}, two, cfg, types.ErrConfiguration, nil},
		{"SettleAdversarial with positions that cannot be written as JSON", func() error {
			_, err := SettleAdversarial(AdversarialContext[chan int]{Ours: AdversarialPosition[chan int]{Position: refuseChan()}})
			return err
		}, "negotiate: invalid request: the negotiation could not be written as JSON", inv, types.ErrInvalidRequest, nil},
	})
}

func TestPivotRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	locked := WithLockedLimits(context.Background(), LockedLimits{MinMode: types.Strict})
	checkRefusals(t, []refusal{
		{"an aggregate nobody knows", func() error {
			_, err := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{Aggregate: "median"})
			return err
		}, `pivot: configuration: Aggregate "median" is not one of first, last, sum, avg, list, concat`, cfg, types.ErrConfiguration, nil},
		{"a blank PivotOn entry", func() error {
			_, err := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{PivotOn: []string{" "}})
			return err
		}, "pivot: configuration: PivotOn entry 1 is blank", cfg, types.ErrConfiguration, nil},
		{"a blank GroupBy entry", func() error {
			_, err := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{GroupBy: []string{"name", ""}})
			return err
		}, "pivot: configuration: GroupBy entry 2 is blank", cfg, types.ErrConfiguration, nil},
		// Pivot shares the check of field names with Project, and the operation a
		// message names is the one that was called: this said "project:" before.
		{"a PivotOn that is not a field of the input", func() error {
			_, err := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{PivotOn: []string{"nope"}})
			return err
		}, `pivot: configuration: PivotOn names "nope", which is not a field of the input`, cfg, types.ErrConfiguration, nil},
		{"a GroupBy that is not a field of the input", func() error {
			_, err := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{GroupBy: []string{"name", "nope"}})
			return err
		}, `pivot: configuration: GroupBy names "nope", which is not a field of the input`, cfg, types.ErrConfiguration, nil},
		{"a dotted GroupBy path whose first segment is not a field", func() error {
			_, err := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{GroupBy: []string{"nope.deeper"}})
			return err
		}, `pivot: configuration: GroupBy names "nope.deeper", which is not a field of the input`, cfg, types.ErrConfiguration, nil},
		{"an input that cannot be written as JSON", func() error {
			_, err := Pivot[chan int, flatRecord](refuseChan())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"a mode weaker than the locked floor", func() error {
			_, err := Pivot[flatRecord, flatRecord](flatRecords[0], PivotOptions{Context: locked})
			return err
		}, "locked policy: mode transform is weaker than the client-scope floor of strict", cfg, types.ErrConfiguration, nil},
	})
}

func TestProjectRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	locked := WithLockedLimits(context.Background(), LockedLimits{MinMode: types.Strict})
	checkRefusals(t, []refusal{
		{"a mapping with a blank target", func() error {
			_, err := Project[flatRecord, flatRecord](flatRecords[0], ProjectOptions{Mappings: map[string]string{"name": " "}})
			return err
		}, "project: configuration: a mapping has a blank source or target", cfg, types.ErrConfiguration, nil},
		{"a mapping with a blank source", func() error {
			_, err := Project[flatRecord, flatRecord](flatRecords[0], ProjectOptions{Mappings: map[string]string{"": "name"}})
			return err
		}, "project: configuration: a mapping has a blank source or target", cfg, types.ErrConfiguration, nil},
		{"a mapping from a field the input lacks", func() error {
			_, err := Project[flatRecord, flatRecord](flatRecords[0], ProjectOptions{Mappings: map[string]string{"nope": "name"}})
			return err
		}, `project: configuration: Mappings names "nope", which is not a field of the input`, cfg, types.ErrConfiguration, nil},
		{"an input that cannot be written as JSON", func() error {
			_, err := Project[chan int, flatRecord](refuseChan())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"an input that cannot be written, with fields excluded", func() error {
			_, err := Project[chan int, flatRecord](refuseChan(), ProjectOptions{Exclude: []string{"name"}})
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"a mode weaker than the locked floor", func() error {
			_, err := Project[flatRecord, flatRecord](flatRecords[0], ProjectOptions{Context: locked})
			return err
		}, "locked policy: mode transform is weaker than the client-scope floor of strict", cfg, types.ErrConfiguration, nil},
	})
}

// copyObject reads a key and finds a value where a name has to be. No document
// json.Marshal writes does that, which is why Project never meets the error; the
// copier is driven directly.
func TestExcludingCopierRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	copier := &excludingCopier{excluded: map[string]struct{}{"a": {}}, removed: excludedValues{}}
	checkRefusals(t, []refusal{
		{"a key that is not a name", func() error {
			decoder := json.NewDecoder(bytes.NewReader([]byte(`1`)))
			return copier.copyObject(decoder, &bytes.Buffer{})
		}, "unexpected object key in the input document", types.KindConfiguration, types.ErrConfiguration, nil},
	})
}

func TestResolveRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	checkRefusals(t, []refusal{
		{"a threshold that is not a number", func() error {
			_, err := Resolve(flatRecords, ResolveOptions{ConflictThreshold: math.NaN()})
			return err
		}, "resolve: configuration: ConflictThreshold has to be a number from 0 to 1", cfg, types.ErrConfiguration, nil},
		{"a threshold that is not a number, for one source", func() error {
			_, err := Resolve(flatRecords[:1], ResolveOptions{ConflictThreshold: 2})
			return err
		}, "resolve: configuration: ConflictThreshold has to be a number from 0 to 1", cfg, types.ErrConfiguration, nil},
		{"no sources", func() error { _, err := Resolve([]flatRecord{}); return err },
			"resolve: invalid request: no sources to resolve", inv, types.ErrInvalidRequest, nil},
		{"a strategy nobody knows", func() error {
			_, err := Resolve(flatRecords, ResolveOptions{Strategy: "coin-flip"})
			return err
		}, `resolve: invalid request: strategy "coin-flip" is not one of newest, most-complete, majority, authoritative, merge`, inv, types.ErrInvalidRequest, nil},
		{"an authoritative source beyond the sources", func() error {
			_, err := Resolve(flatRecords, ResolveOptions{AuthoritativeSource: 7})
			return err
		}, "resolve: invalid request: AuthoritativeSource 7 is outside the 2 source(s) supplied", inv, types.ErrInvalidRequest, nil},
		{"a field priority beyond the sources", func() error {
			_, err := Resolve(flatRecords, ResolveOptions{FieldPriorities: map[string]int{"name": 9}})
			return err
		}, `resolve: invalid request: the priority for field "name" names source 9, outside the 2 source(s) supplied`, inv, types.ErrInvalidRequest, nil},
		{"a source that cannot be written as JSON", func() error {
			_, err := Resolve([]chan int{refuseChan(), refuseChan()})
			return err
		}, "failed to marshal source 0" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

func TestSynthesizeRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	options := NewSynthesizeOptions()
	with := func(change func(*SynthesizeOptions)) SynthesizeOptions {
		o := NewSynthesizeOptions()
		change(&o)
		return o
	}
	checkRefusals(t, []refusal{
		{"no sources", func() error { _, err := Synthesize[flatRecord](nil, options); return err },
			"no sources provided for synthesis", cfg, types.ErrConfiguration, nil},
		{"an empty list of sources", func() error { _, err := Synthesize[flatRecord]([]any{}, options); return err },
			"no sources provided for synthesis", cfg, types.ErrConfiguration, nil},
		{"a strategy nobody knows", func() error {
			_, err := Synthesize[flatRecord]([]any{"a"}, with(func(o *SynthesizeOptions) { o.Strategy = "bogus" }))
			return err
		}, "invalid options: invalid strategy: bogus", cfg, types.ErrConfiguration, nil},
		{"a conflict resolution nobody knows", func() error {
			_, err := Synthesize[flatRecord]([]any{"a"}, with(func(o *SynthesizeOptions) { o.ConflictResolution = "bogus" }))
			return err
		}, "invalid options: invalid conflict resolution: bogus", cfg, types.ErrConfiguration, nil},
		{"source priority with no priorities", func() error {
			_, err := Synthesize[flatRecord]([]any{"a"}, with(func(o *SynthesizeOptions) { o.ConflictResolution = "source-priority" }))
			return err
		}, "synthesize: configuration: the source-priority resolution needs SourcePriorities", cfg, types.ErrConfiguration, nil},
		{"a priority beyond the sources", func() error {
			_, err := Synthesize[flatRecord]([]any{"a"}, with(func(o *SynthesizeOptions) {
				o.ConflictResolution, o.SourcePriorities = "source-priority", []int{3}
			}))
			return err
		}, "synthesize: configuration: source priority 0 names source 3, but 1 source(s) were supplied", cfg, types.ErrConfiguration, nil},
		{"a source that cannot be written as JSON", func() error {
			_, err := Synthesize[flatRecord]([]any{"a", refuseChan()}, options)
			return err
		}, "failed to marshal source 1" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

func TestPredictRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	with := func(change func(*PredictOptions)) PredictOptions {
		o := NewPredictOptions()
		change(&o)
		return o
	}
	predict := func(o PredictOptions) func() error {
		return func() error { _, err := Predict[flatRecord](flatRecords, o); return err }
	}
	checkRefusals(t, []refusal{
		{"no horizon", predict(with(func(o *PredictOptions) { o.Horizon = "" })),
			"invalid options: prediction horizon is required", cfg, types.ErrConfiguration, nil},
		{"a confidence level above one", predict(with(func(o *PredictOptions) { o.ConfidenceLevel = 2 })),
			"invalid options: confidence level must be between 0 and 1, got 2.000000", cfg, types.ErrConfiguration, nil},
		{"an interval with no confidence level", predict(with(func(o *PredictOptions) { o.ConfidenceLevel = 0 })),
			"invalid options: an interval needs a confidence level above 0", cfg, types.ErrConfiguration, nil},
		{"a method nobody knows", predict(with(func(o *PredictOptions) { o.Method = "tea-leaves" })),
			"invalid options: invalid method: tea-leaves", cfg, types.ErrConfiguration, nil},
		{"no scenarios", predict(with(func(o *PredictOptions) { o.NumScenarios = 0 })),
			"invalid options: num scenarios must be at least 1, got 0", cfg, types.ErrConfiguration, nil},
		{"history that cannot be written as JSON", func() error {
			_, err := Predict[flatRecord](refuseChan(), NewPredictOptions())
			return err
		}, "failed to marshal historical data" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

func TestEnrichRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	deep := func(depth string) EnrichOptions {
		o := NewEnrichOptions()
		o.Depth = depth
		return o
	}
	checkRefusals(t, []refusal{
		{"Enrich with a depth nobody knows", func() error {
			_, err := Enrich[flatRecord, flatRecord](flatRecords[0], deep("bottomless"))
			return err
		}, "invalid options: invalid depth: bottomless", cfg, types.ErrConfiguration, nil},
		{"Enrich with an input that cannot be written as JSON", func() error {
			_, err := Enrich[chan int, flatRecord](refuseChan(), NewEnrichOptions())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"Enrich with a context that cannot be written as JSON", func() error {
			_, err := Enrich[flatRecord, flatRecord](flatRecords[0], NewEnrichOptions().WithContext(map[string]any{"a": refuseChan()}))
			return err
		}, "enrich: configuration: the context cannot be written as JSON", cfg, types.ErrConfiguration, nil},
		{"EnrichInPlace with a depth nobody knows", func() error {
			_, err := EnrichInPlace(flatRecords[0], deep("bottomless"))
			return err
		}, "invalid options: invalid depth: bottomless", cfg, types.ErrConfiguration, nil},
		{"EnrichInPlace asked for confidence", func() error {
			_, err := EnrichInPlace(flatRecords[0], NewEnrichOptions().WithIncludeConfidence(true))
			return err
		}, "enrich: configuration: EnrichInPlace returns the enriched value itself and has nowhere to put confidence scores; use Enrich", cfg, types.ErrConfiguration, nil},
		{"EnrichInPlace with an input that cannot be written as JSON", func() error {
			_, err := EnrichInPlace(refuseChan(), NewEnrichOptions())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

func TestDecomposeRefusalsHaveAKindAndKeepTheirText(t *testing.T) {
	cfg, inv := types.KindConfiguration, types.KindInvalidRequest
	with := func(change func(*DecomposeOptions)) DecomposeOptions {
		o := NewDecomposeOptions()
		change(&o)
		return o
	}
	strategy := with(func(o *DecomposeOptions) { o.Strategy = "shred" })
	depth := with(func(o *DecomposeOptions) { o.MaxDepth = 0 })
	parts := with(func(o *DecomposeOptions) { o.TargetParts = -2 })
	checkRefusals(t, []refusal{
		{"Decompose with a strategy nobody knows", func() error {
			_, err := Decompose(flatRecords[0], strategy)
			return err
		}, "invalid options: invalid strategy: shred", cfg, types.ErrConfiguration, nil},
		{"Decompose with no depth", func() error { _, err := Decompose(flatRecords[0], depth); return err },
			"invalid options: max depth must be at least 1, got 0", cfg, types.ErrConfiguration, nil},
		{"Decompose with a negative number of parts", func() error { _, err := Decompose(flatRecords[0], parts); return err },
			"invalid options: target parts cannot be negative, got -2", cfg, types.ErrConfiguration, nil},
		{"Decompose with an input that cannot be written as JSON", func() error {
			_, err := Decompose(refuseChan(), NewDecomposeOptions())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
		{"DecomposeToSlice with a strategy nobody knows", func() error {
			_, err := DecomposeToSlice[flatRecord, flatRecord](flatRecords[0], strategy)
			return err
		}, "invalid options: invalid strategy: shred", cfg, types.ErrConfiguration, nil},
		{"DecomposeToSlice with no depth", func() error {
			_, err := DecomposeToSlice[flatRecord, flatRecord](flatRecords[0], depth)
			return err
		}, "invalid options: max depth must be at least 1, got 0", cfg, types.ErrConfiguration, nil},
		{"DecomposeToSlice with an input that cannot be written as JSON", func() error {
			_, err := DecomposeToSlice[chan int, flatRecord](refuseChan(), NewDecomposeOptions())
			return err
		}, "failed to marshal input" + marshalChan, inv, types.ErrInvalidRequest, isUnsupportedType},
	})
}

// A projection that carries the value of a field the caller excluded is the
// model's answer breaking what was asked of it: an invariant, not a fault in how
// the answer was written. It is the one error of Project made after the model
// answered that had no kind.
func TestProjectLeakOfAnExcludedValueIsAnInvariantViolation(t *testing.T) {
	type record struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	type projected struct {
		Note string `json:"note"`
	}
	answer := `{"projected":{"note":"the token is supersecretvalue"},"mappings":[],"lost":[],"inferred":[],"confidence":0.9}`
	defer stubLLM(answer)()

	_, err := Project[record, projected](record{ID: "u1", Token: "supersecretvalue"}, ProjectOptions{Exclude: []string{"token"}})
	if err == nil {
		t.Fatal("a projection carrying an excluded value was accepted")
	}
	if want := `project: the projection contains the value of excluded field "token"`; err.Error() != want {
		t.Errorf("text = %q, want %q", err.Error(), want)
	}
	if got := types.KindOf(err); got != types.KindInvariantViolation {
		t.Errorf("kind = %v, want %v", got, types.KindInvariantViolation)
	}
	if !errors.Is(err, types.ErrInvariantViolation) {
		t.Error("errors.Is(err, ErrInvariantViolation) is false")
	}
}
