package bind

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The types a stand-in root converts: this package cannot name the real root's,
// and a conversion is read through the type the caller names.
type (
	standUsage    struct{ in, out int }
	standCost     struct{ total float64 }
	standTiming   struct{ network time.Duration }
	standRequest  struct{ model string }
	standResponse struct{ content string }
	standChunk    struct{ delta string }
	standLimits   struct{ questions int }
)

func conversionHooks() Hooks {
	h := standIn()
	h.UsageFromInternal = func(u types.TokenUsage) standUsage { return standUsage{u.PromptTokens, u.CompletionTokens} }
	h.UsageToInternal = func(u standUsage) types.TokenUsage {
		return types.TokenUsage{PromptTokens: u.in, CompletionTokens: u.out, TotalTokens: u.in + u.out}
	}
	h.CostFromInternal = func(c types.CostInfo) standCost { return standCost{c.TotalCost} }
	h.CostToInternal = func(c standCost) types.CostInfo { return types.CostInfo{TotalCost: c.total} }
	h.TimingFromInternal = func(t types.Timing) standTiming { return standTiming{t.Network} }
	h.TimingToInternal = func(t standTiming) types.Timing { return types.Timing{Network: t.network} }
	h.RequestFromInternal = func(r llm.CompletionRequest) (standRequest, error) {
		if len(r.Tools) > 0 {
			return standRequest{}, errors.New("tools")
		}
		return standRequest{r.Model}, nil
	}
	h.RequestToInternal = func(r standRequest) llm.CompletionRequest { return llm.CompletionRequest{Model: r.model} }
	h.ResponseFromInternal = func(r llm.CompletionResponse) (standResponse, error) { return standResponse{r.Content}, nil }
	h.ResponseToInternal = func(r standResponse) llm.CompletionResponse { return llm.CompletionResponse{Content: r.content} }
	h.ChunkFromInternal = func(c llm.StreamChunk) (standChunk, error) { return standChunk{c.Delta}, nil }
	h.ChunkToInternal = func(c standChunk) llm.StreamChunk { return llm.StreamChunk{Delta: c.delta} }
	h.LimitsFromInternal = func(l llm.DecisionLimits) standLimits { return standLimits{l.MaxQuestions} }
	h.LimitsToInternal = func(l standLimits) llm.DecisionLimits { return llm.DecisionLimits{MaxQuestions: l.questions} }
	h.KindOf = func(kind types.ErrorKind, narrow types.Narrowing) uint8 {
		if narrow == types.NarrowRefusal {
			return 15
		}
		return uint8(kind)
	}
	h.InternalKind = func(kind uint8) (types.ErrorKind, types.Narrowing) { return types.ErrorKind(kind), types.NarrowNone }
	h.KindOfError = func(err error) (uint8, bool) { return uint8(types.KindOf(err)), types.KindOf(err) != types.KindUnknown }
	h.FromProvider = func(err error) error { return &converted{op: "from provider", err: err} }
	h.ToProvider = func(err error, provider, model string) error { return &converted{op: provider + "/" + model, err: err} }
	h.Variables = func(name string) ([]string, []string, bool) {
		if name == "acme" {
			return []string{"ACME_KEY"}, []string{"ACME_URL"}, true
		}
		return nil, nil, false
	}
	return h
}

func TestTheConversionsReturnTheZeroValueBeforeTheRootRegisters(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	if got := UsageFromInternal[standUsage](types.TokenUsage{PromptTokens: 3}); got != (standUsage{}) {
		t.Errorf("UsageFromInternal = %+v", got)
	}
	if got := UsageToInternal(standUsage{1, 2}); got != (types.TokenUsage{}) {
		t.Errorf("UsageToInternal = %+v", got)
	}
	if got := CostFromInternal[standCost](types.CostInfo{TotalCost: 1}); got != (standCost{}) {
		t.Errorf("CostFromInternal = %+v", got)
	}
	if got := CostToInternal(standCost{1}); got.TotalCost != 0 {
		t.Errorf("CostToInternal = %+v", got)
	}
	if got := TimingFromInternal[standTiming](types.Timing{Network: 1}); got != (standTiming{}) {
		t.Errorf("TimingFromInternal = %+v", got)
	}
	if got := TimingToInternal(standTiming{1}); got.Network != 0 {
		t.Errorf("TimingToInternal = %+v", got)
	}
	if got := RequestToInternal(standRequest{"m"}); got.Model != "" {
		t.Errorf("RequestToInternal = %+v", got)
	}
	if got := ResponseToInternal(standResponse{"c"}); got.Content != "" {
		t.Errorf("ResponseToInternal = %+v", got)
	}
	if got := ChunkToInternal(standChunk{"d"}); got.Delta != "" {
		t.Errorf("ChunkToInternal = %+v", got)
	}
	if got := LimitsFromInternal[standLimits](llm.DecisionLimits{MaxQuestions: 4}); got != (standLimits{}) {
		t.Errorf("LimitsFromInternal = %+v", got)
	}
	if got := LimitsToInternal(standLimits{4}); got.MaxQuestions != 0 {
		t.Errorf("LimitsToInternal = %+v", got)
	}
	if _, err := RequestFromInternal[standRequest](llm.CompletionRequest{}); types.KindOf(err) != types.KindConfiguration {
		t.Errorf("RequestFromInternal err = %v, want a configuration error", err)
	}
	if _, err := ResponseFromInternal[standResponse](llm.CompletionResponse{}); types.KindOf(err) != types.KindConfiguration {
		t.Errorf("ResponseFromInternal err = %v, want a configuration error", err)
	}
	if _, err := ChunkFromInternal[standChunk](llm.StreamChunk{}); types.KindOf(err) != types.KindConfiguration {
		t.Errorf("ChunkFromInternal err = %v, want a configuration error", err)
	}
	if KindOf(types.KindTimeout, types.NarrowNone) != 0 {
		t.Error("KindOf answered before the root registered")
	}
	if kind, mark := InternalKind(3); kind != types.KindProviderUnavailable || mark != types.NarrowNone {
		t.Errorf("InternalKind = %v %v", kind, mark)
	}
	if kind, known := KindOfError(errors.New("x")); kind != 0 || known {
		t.Errorf("KindOfError = %v %v", kind, known)
	}
	plain := errors.New("x")
	if FromProvider(plain) != plain || ToProvider(plain, "p", "m") != plain {
		t.Error("an error was changed before the root registered")
	}
	if FromProvider(nil) != nil || ToProvider(nil, "p", "m") != nil {
		t.Error("nil did not stay nil")
	}
	if _, _, ok := ProviderVariables("acme"); ok {
		t.Error("ProviderVariables answered before the root registered")
	}
}

func TestTheConversionsCallTheRegisteredFunctionsWithNoAllocation(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	Register(conversionHooks())

	if got := UsageFromInternal[standUsage](types.TokenUsage{PromptTokens: 3, CompletionTokens: 4}); got != (standUsage{3, 4}) {
		t.Errorf("UsageFromInternal = %+v", got)
	}
	if got := UsageToInternal(standUsage{3, 4}); got.TotalTokens != 7 {
		t.Errorf("UsageToInternal = %+v", got)
	}
	if got := CostFromInternal[standCost](types.CostInfo{TotalCost: 2.5}); got != (standCost{2.5}) {
		t.Errorf("CostFromInternal = %+v", got)
	}
	if got := CostToInternal(standCost{2.5}); got.TotalCost != 2.5 {
		t.Errorf("CostToInternal = %+v", got)
	}
	if got := TimingFromInternal[standTiming](types.Timing{Network: time.Second}); got.network != time.Second {
		t.Errorf("TimingFromInternal = %+v", got)
	}
	if got := TimingToInternal(standTiming{time.Minute}); got.Network != time.Minute {
		t.Errorf("TimingToInternal = %+v", got)
	}
	if got, err := RequestFromInternal[standRequest](llm.CompletionRequest{Model: "m"}); err != nil || got.model != "m" {
		t.Errorf("RequestFromInternal = %+v, %v", got, err)
	}
	if _, err := RequestFromInternal[standRequest](llm.CompletionRequest{Tools: []llm.Tool{{Name: "f"}}}); err == nil {
		t.Error("a request with tools was converted")
	}
	if got := RequestToInternal(standRequest{"m"}); got.Model != "m" {
		t.Errorf("RequestToInternal = %+v", got)
	}
	if got, err := ResponseFromInternal[standResponse](llm.CompletionResponse{Content: "c"}); err != nil || got.content != "c" {
		t.Errorf("ResponseFromInternal = %+v, %v", got, err)
	}
	if got := ResponseToInternal(standResponse{"c"}); got.Content != "c" {
		t.Errorf("ResponseToInternal = %+v", got)
	}
	if got, err := ChunkFromInternal[standChunk](llm.StreamChunk{Delta: "d"}); err != nil || got.delta != "d" {
		t.Errorf("ChunkFromInternal = %+v, %v", got, err)
	}
	if got := ChunkToInternal(standChunk{"d"}); got.Delta != "d" {
		t.Errorf("ChunkToInternal = %+v", got)
	}
	if got := LimitsFromInternal[standLimits](llm.DecisionLimits{MaxQuestions: 4}); got.questions != 4 {
		t.Errorf("LimitsFromInternal = %+v", got)
	}
	if got := LimitsToInternal(standLimits{4}); got.MaxQuestions != 4 {
		t.Errorf("LimitsToInternal = %+v", got)
	}
	if got := KindOf(types.KindTimeout, types.NarrowRefusal); got != 15 {
		t.Errorf("KindOf = %d, want the mark to decide", got)
	}
	if kind, _ := InternalKind(9); kind != 9 {
		t.Errorf("InternalKind = %v", kind)
	}
	if kind, known := KindOfError(types.Newf(types.KindTimeout, "x", "y")); !known || kind != uint8(types.KindTimeout) {
		t.Errorf("KindOfError = %v %v", kind, known)
	}
	if got := ToProvider(errors.New("x"), "p", "m"); !strings.Contains(got.Error(), "p/m") {
		t.Errorf("ToProvider = %v", got)
	}
	if keys, urls, ok := ProviderVariables("acme"); !ok || keys[0] != "ACME_KEY" || urls[0] != "ACME_URL" {
		t.Errorf("ProviderVariables = %v %v %v", keys, urls, ok)
	}

	usage := types.TokenUsage{PromptTokens: 1}
	if allocs := testing.AllocsPerRun(200, func() { _ = UsageFromInternal[standUsage](usage) }); allocs != 0 {
		t.Errorf("a usage conversion allocates %v times", allocs)
	}
	req := llm.CompletionRequest{Model: "m"}
	if allocs := testing.AllocsPerRun(200, func() { _, _ = RequestFromInternal[standRequest](req) }); allocs != 0 {
		t.Errorf("a request conversion allocates %v times", allocs)
	}
}

func TestAConversionRegisteredUnderAnotherTypeIsAMistakeThatPanics(t *testing.T) {
	unregister()
	t.Cleanup(unregister)
	Register(conversionHooks())
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(recovered.(string), "other than the one it is asked for") {
			t.Errorf("recovered %v", recovered)
		}
	}()
	_ = UsageFromInternal[standCost](types.TokenUsage{})
}

func TestSumsAreHeldAtTheEdgesOfTheirType(t *testing.T) {
	t.Parallel()
	ints := []struct{ a, b, want int }{
		{1, 2, 3}, {0, 0, 0}, {-1, -2, -3}, {5, -7, -2},
		{math.MaxInt, 1, math.MaxInt}, {math.MaxInt, math.MaxInt, math.MaxInt}, {math.MaxInt - 1, 1, math.MaxInt},
		{math.MinInt, -1, math.MinInt}, {math.MinInt, math.MinInt, math.MinInt}, {math.MaxInt, math.MinInt, -1},
	}
	for _, c := range ints {
		if got := SumInt(c.a, c.b); got != c.want {
			t.Errorf("SumInt(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	durations := []struct{ a, b, want time.Duration }{
		{time.Second, time.Second, 2 * time.Second}, {0, 0, 0}, {-time.Second, -time.Second, -2 * time.Second},
		{math.MaxInt64, 1, math.MaxInt64}, {math.MaxInt64, math.MaxInt64, math.MaxInt64},
		{math.MinInt64, -1, math.MinInt64}, {math.MinInt64, math.MinInt64, math.MinInt64}, {math.MaxInt64, math.MinInt64, -1},
		{time.Hour, -time.Minute, 59 * time.Minute}, {math.MaxInt64 - 1, 1, math.MaxInt64},
	}
	for _, c := range durations {
		if got := SumDuration(c.a, c.b); got != c.want {
			t.Errorf("SumDuration(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAProviderNameIsAPlaceholderOnlyIfItIsOneOfTheTwo(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"local": true, "mock": true, " Local ": true, "MOCK": true, "\tmock\n": true,
		"": false, "openai": false, "localhost": false, "mocking": false, "local mock": false, "lo cal": false,
	} {
		if got := PlaceholderProvider(name); got != want {
			t.Errorf("PlaceholderProvider(%q) = %v, want %v", name, got, want)
		}
	}
	for _, name := range []string{"local", " MOCK "} {
		advice := PlaceholderAdvice(name)
		if !strings.Contains(advice, "schemafluxtest") || !strings.Contains(advice, strings.ToLower(strings.TrimSpace(name))) {
			t.Errorf("PlaceholderAdvice(%q) = %q", name, advice)
		}
	}
}

func TestAKeylessDeclarationIsPerNameAndIgnoresCaseAndSpace(t *testing.T) {
	t.Parallel()
	DeclareKeyless(" Bind-Test-Keyless ")
	DeclareKeyless("bind-test-keyless")
	for name, want := range map[string]bool{
		"bind-test-keyless": true, "BIND-TEST-KEYLESS": true, "  bind-test-keyless": true,
		"bind-test-keyless-2": false, "": false, "bind-test": false,
	} {
		if got := Keyless(name); got != want {
			t.Errorf("Keyless(%q) = %v, want %v", name, got, want)
		}
	}
}
