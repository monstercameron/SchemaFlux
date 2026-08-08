package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// PL-009: the progressive recovery cascade, and the PL-013 counters
// (types.BatchMetrics.Omissions/AtomicFallbacks) it makes real.
//
// The fixture is its own -- recoverWidgetOp -- rather than plan_test.go's
// widgetOp, because RunOpManyRecover needs the BatchItemCodec value directly
// (see recover.go's doc comment on why) and this task's file list does not
// include plan_test.go to add a shared export to.
//
// recoverWidget.Name is built with strings.Repeat so every item in a test
// has a distinct name length -- the value recoverFakeProvider's batch and
// atomic handlers both report back. That makes a value mismatch (item 3's
// answer landing on item 5, say) fail loudly instead of coincidentally
// matching, which a fixture where every item looked alike would not catch.

type recoverWidget struct {
	Name string
}

type recoverSummary struct {
	Length int `json:"length"`
}

func recoverWidgets(n int) []recoverWidget {
	items := make([]recoverWidget, n)
	for i := range items {
		items[i] = recoverWidget{Name: strings.Repeat("a", i+1)}
	}
	return items
}

func recoverCodec() BatchItemCodec[recoverWidget, recoverSummary] {
	return BatchItemCodec[recoverWidget, recoverSummary]{
		DecodeItem: func(raw json.RawMessage) (recoverSummary, error) {
			var s recoverSummary
			if err := json.Unmarshal(raw, &s); err != nil {
				return recoverSummary{}, err
			}
			return s, nil
		},
	}
}

func recoverWidgetOp() Op[recoverWidget, recoverSummary] {
	return Op[recoverWidget, recoverSummary]{
		ID: types.OperationID{Name: "recoverWidget", Version: "v1"},
		Semantics: types.Semantics{
			Category:  types.CategoryTransformation,
			Stability: types.StabilityExperimental,
		},
		Contract: OutputContract[recoverSummary]{
			Decode: func(body string) (recoverSummary, error) {
				var s recoverSummary
				if err := json.Unmarshal([]byte(body), &s); err != nil {
					return recoverSummary{}, err
				}
				return s, nil
			},
		},
		Batch: NewIDBatchAlgebra[recoverWidget, recoverSummary](types.BatchItemwise, types.AlgebraIndependent,
			"Summarize each widget's name length.", recoverCodec()),
		BuildPrompt: func(w recoverWidget, _ types.OpOptions) (string, string) {
			return "summarize one widget", w.Name
		},
	}
}

// recoverBatchHandler answers one MDSP call, given the tagged items the
// request actually carried (parsed from the request, never assumed from
// position) -- the response body, or an error to simulate a transport
// failure.
type recoverBatchHandler func(tagged []taggedItem[recoverWidget]) (string, error)

// fullSuccessBatch is the default batch handler: answers every id it was
// asked about with that item's own name length, derived from the parsed
// request rather than from any positional assumption.
func fullSuccessBatch(tagged []taggedItem[recoverWidget]) (string, error) {
	items := make([]idBatchItem, len(tagged))
	for i, t := range tagged {
		items[i] = idBatchItem{ID: t.ID, Output: json.RawMessage(fmt.Sprintf(`{"length":%d}`, len(t.Item.Name)))}
	}
	body, err := json.Marshal(idBatchResponse{Items: items})
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// omitBatch answers every id except the ones named in skip.
func omitBatch(skip ...string) recoverBatchHandler {
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}
	return func(tagged []taggedItem[recoverWidget]) (string, error) {
		var items []idBatchItem
		for _, t := range tagged {
			if skipSet[t.ID] {
				continue
			}
			items = append(items, idBatchItem{ID: t.ID, Output: json.RawMessage(fmt.Sprintf(`{"length":%d}`, len(t.Item.Name)))})
		}
		body, err := json.Marshal(idBatchResponse{Items: items})
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

// duplicateBatch answers every id normally but answers dupID twice, with
// two different (and therefore untrustworthy) payloads.
func duplicateBatch(dupID string) recoverBatchHandler {
	return func(tagged []taggedItem[recoverWidget]) (string, error) {
		var items []idBatchItem
		for _, t := range tagged {
			items = append(items, idBatchItem{ID: t.ID, Output: json.RawMessage(fmt.Sprintf(`{"length":%d}`, len(t.Item.Name)))})
			if t.ID == dupID {
				items = append(items, idBatchItem{ID: t.ID, Output: json.RawMessage(`{"length":-1}`)})
			}
		}
		body, err := json.Marshal(idBatchResponse{Items: items})
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

// inventBatch answers every real id normally, plus one id that was never
// offered.
func inventBatch(invented string) recoverBatchHandler {
	return func(tagged []taggedItem[recoverWidget]) (string, error) {
		items := make([]idBatchItem, 0, len(tagged)+1)
		for _, t := range tagged {
			items = append(items, idBatchItem{ID: t.ID, Output: json.RawMessage(fmt.Sprintf(`{"length":%d}`, len(t.Item.Name)))})
		}
		items = append(items, idBatchItem{ID: invented, Output: json.RawMessage(`{"length":999}`)})
		body, err := json.Marshal(idBatchResponse{Items: items})
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

func malformedBatch(_ []taggedItem[recoverWidget]) (string, error) {
	return `not json at all {{{`, nil
}

func failingBatch(_ []taggedItem[recoverWidget]) (string, error) {
	return "", fmt.Errorf("simulated transport failure")
}

// recoverFakeProvider is an llm.Provider that tells a batch call (its
// UserPrompt starts with the id-protocol's "Process these items:" prefix,
// batchprotocol.go's Encode) apart from an atomic fallback call (whatever
// recoverWidgetOp.BuildPrompt sends, which never has that prefix), and
// scripts each kind's responses independently and in call order -- the
// order this cascade's own passes run in, which is what makes "response N
// omits item X" a controllable test input instead of a race.
type recoverFakeProvider struct {
	mu sync.Mutex

	batchScript  []recoverBatchHandler
	batchDefault recoverBatchHandler
	batchCalls   int

	// atomicFail reports whether an atomic fallback call for this user
	// prompt (the widget's Name) should fail.
	atomicFail  func(name string) bool
	atomicCalls int

	model  string
	priced bool
}

func newRecoverFakeProvider(model string) *recoverFakeProvider {
	return &recoverFakeProvider{model: model, batchDefault: fullSuccessBatch}
}

func (p *recoverFakeProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	isBatch := strings.HasPrefix(req.UserPrompt, "Process these items:")

	var body string
	var err error

	if isBatch {
		p.mu.Lock()
		idx := p.batchCalls
		p.batchCalls++
		handler := p.batchDefault
		if idx < len(p.batchScript) {
			handler = p.batchScript[idx]
		}
		p.mu.Unlock()

		tagged := parseTaggedBatchRequest(req.UserPrompt)
		body, err = handler(tagged)
	} else {
		p.mu.Lock()
		p.atomicCalls++
		fail := p.atomicFail != nil && p.atomicFail(req.UserPrompt)
		p.mu.Unlock()

		if fail {
			return llm.CompletionResponse{}, fmt.Errorf("simulated atomic fallback failure for %q", req.UserPrompt)
		}
		body = fmt.Sprintf(`{"length":%d}`, len(req.UserPrompt))
	}

	if err != nil {
		return llm.CompletionResponse{}, err
	}

	usage := types.TokenUsage{}
	if p.priced {
		usage = types.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	}
	return llm.CompletionResponse{
		Content:      body,
		Provider:     "recover-fake",
		Model:        p.model,
		FinishReason: "stop",
		Usage:        usage,
	}, nil
}

func (p *recoverFakeProvider) Name() string                               { return "recover-fake" }
func (p *recoverFakeProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *recoverFakeProvider) RetryPolicy() (int, time.Duration)          { return 0, time.Millisecond }

func (p *recoverFakeProvider) counts() (batch, atomic int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.batchCalls, p.atomicCalls
}

// parseTaggedBatchRequest recovers the id-tagged items a batch request
// carried, from the exact format batchprotocol.go's NewIDBatchAlgebra Encode
// produces ("Process these items:\n" + a JSON array of {"id","item"}).
func parseTaggedBatchRequest(user string) []taggedItem[recoverWidget] {
	const prefix = "Process these items:\n"
	payload := strings.TrimPrefix(user, prefix)
	var tagged []taggedItem[recoverWidget]
	_ = json.Unmarshal([]byte(payload), &tagged)
	return tagged
}

func recoverCtx(t *testing.T, provider llm.Provider) context.Context {
	t.Helper()
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	return WithProvider(context.Background(), provider)
}

// --- 1: the happy path -- one chunk, one call, everything resolves.

func TestRunOpManyRecoverFullyCompliantSingleChunk(t *testing.T) {
	items := recoverWidgets(5)
	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if result.Summary.Total != 5 || result.Summary.Succeeded != 5 || result.Summary.Failed != 0 {
		t.Fatalf("Summary = %+v, want 5/5/0", result.Summary)
	}
	for i, item := range result.Items {
		if item.Index != i {
			t.Fatalf("item %d: Index = %d, want %d", i, item.Index, i)
		}
		if item.Status != types.ItemSucceeded {
			t.Fatalf("item %d: Status = %s, want succeeded", i, item.Status)
		}
		if item.Mode != ModeMDSP {
			t.Fatalf("item %d: Mode = %q, want %q", i, item.Mode, ModeMDSP)
		}
		if item.Attempts != 1 {
			t.Fatalf("item %d: Attempts = %d, want 1 (resolved on the first pass)", i, item.Attempts)
		}
		if item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d", i, item.Value.Length, len(items[i].Name))
		}
	}
	batchCalls, atomicCalls := provider.counts()
	if batchCalls != 1 || atomicCalls != 0 {
		t.Fatalf("batchCalls=%d atomicCalls=%d, want 1/0 for a fully compliant single chunk", batchCalls, atomicCalls)
	}
}

// --- 2: an omission is recovered by an isolate pass, not lost.

func TestRunOpManyRecoverOmissionRecoveredByIsolatePass(t *testing.T) {
	items := recoverWidgets(5)
	provider := newRecoverFakeProvider("fake-model")
	// Pass 1 (5 items, local ids i-000001..i-000005) omits item index 2
	// (id i-000003). Pass 2 is the isolate retry over just that one item,
	// where it gets its own local id i-000001 again -- and this time
	// resolves.
	provider.batchScript = []recoverBatchHandler{omitBatch("i-000003"), fullSuccessBatch}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false, want true (the omission was recovered): %+v", result.Summary)
	}
	for i, item := range result.Items {
		if item.Mode != ModeMDSP {
			t.Fatalf("item %d: Mode = %q, want %q (recovered via a second MDSP pass, not atomic)", i, item.Mode, ModeMDSP)
		}
		if item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d", i, item.Value.Length, len(items[i].Name))
		}
	}
	if result.Items[2].Attempts != 2 {
		t.Fatalf("recovered item's Attempts = %d, want 2 (one omitted pass, one isolate pass)", result.Items[2].Attempts)
	}
	for i, item := range result.Items {
		if i == 2 {
			continue
		}
		if item.Attempts != 1 {
			t.Fatalf("item %d (never omitted): Attempts = %d, want 1", i, item.Attempts)
		}
	}
	batchCalls, atomicCalls := provider.counts()
	if batchCalls != 2 || atomicCalls != 0 {
		t.Fatalf("batchCalls=%d atomicCalls=%d, want 2/0", batchCalls, atomicCalls)
	}
}

// --- 3: an omission that survives the isolate pass falls back to atomic.

func TestRunOpManyRecoverOmissionFallsBackToAtomicAfterIsolateExhausted(t *testing.T) {
	items := recoverWidgets(5)
	provider := newRecoverFakeProvider("fake-model")
	// Both the first pass and the (single, default) isolate pass omit the
	// same item -- DefaultMaxIsolatePasses is 1, so nothing is left to try
	// through MDSP after that, and it has to fall back to atomic.
	provider.batchScript = []recoverBatchHandler{omitBatch("i-000003"), omitBatch("i-000001")}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false, want true (atomic fallback recovers it): %+v", result.Summary)
	}
	if result.Items[2].Mode != types.ModeAtomicFallback {
		t.Fatalf("item 2: Mode = %q, want %q", result.Items[2].Mode, types.ModeAtomicFallback)
	}
	if result.Items[2].Value.Length != len(items[2].Name) {
		t.Fatalf("item 2 (atomic fallback): Value.Length = %d, want %d", result.Items[2].Value.Length, len(items[2].Name))
	}
	for i, item := range result.Items {
		if i == 2 {
			continue
		}
		if item.Mode != ModeMDSP {
			t.Fatalf("item %d: Mode = %q, want %q (never omitted)", i, item.Mode, ModeMDSP)
		}
	}
	batchCalls, atomicCalls := provider.counts()
	if batchCalls != 2 || atomicCalls != 1 {
		t.Fatalf("batchCalls=%d atomicCalls=%d, want 2/1", batchCalls, atomicCalls)
	}
}

// --- 4: a duplicated id is treated as unresolved, not as "pick one".

func TestRunOpManyRecoverDuplicateIDTreatedUnresolved(t *testing.T) {
	items := recoverWidgets(4)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{duplicateBatch("i-000002"), fullSuccessBatch}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 4, StartItemsPerCall: 4})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false: %+v", result.Summary)
	}
	// item index 1 (i-000002) was duplicated on pass 1 -- neither payload is
	// trustworthy (-1 is the poisoned duplicate value), so its correct
	// value must have come from the isolate pass, not from the -1 answer.
	if result.Items[1].Value.Length != len(items[1].Name) {
		t.Fatalf("item 1: Value.Length = %d, want %d (must not be the duplicate's -1)", result.Items[1].Value.Length, len(items[1].Name))
	}
	if result.Items[1].Attempts != 2 {
		t.Fatalf("item 1: Attempts = %d, want 2", result.Items[1].Attempts)
	}
}

// --- 5: an invented id is ignored, not mistaken for a real item.

func TestRunOpManyRecoverUnknownIDIgnored(t *testing.T) {
	items := recoverWidgets(3)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{inventBatch("i-999999")}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 3, StartItemsPerCall: 3})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false: %+v", result.Summary)
	}
	for i, item := range result.Items {
		if item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d", i, item.Value.Length, len(items[i].Name))
		}
	}
}

// --- 6: a body that does not parse at all still recovers via a later pass.

func TestRunOpManyRecoverMalformedBodyRecoveredByNextPass(t *testing.T) {
	items := recoverWidgets(3)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{malformedBatch, fullSuccessBatch}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 3, StartItemsPerCall: 3})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false: %+v", result.Summary)
	}
	for i, item := range result.Items {
		if item.Attempts != 2 {
			t.Fatalf("item %d: Attempts = %d, want 2 (malformed pass + recovery pass)", i, item.Attempts)
		}
		if item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d", i, item.Value.Length, len(items[i].Name))
		}
	}
}

// --- 7: a transport failure on every pass exhausts isolation and still
// recovers through atomic, without hanging.

func TestRunOpManyRecoverTransportFailureFallsBackToAtomic(t *testing.T) {
	items := recoverWidgets(2)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{failingBatch, failingBatch}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 2, StartItemsPerCall: 2, MaxIsolatePasses: 1})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false: %+v", result.Summary)
	}
	for i, item := range result.Items {
		if item.Mode != types.ModeAtomicFallback {
			t.Fatalf("item %d: Mode = %q, want %q", i, item.Mode, types.ModeAtomicFallback)
		}
	}
}

// --- 8: an atomic fallback call that itself fails is reported ItemFailed,
// never silently dropped or invented.

func TestRunOpManyRecoverAtomicFallbackFailureReportsItemFailed(t *testing.T) {
	items := recoverWidgets(3)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{omitBatch("i-000002"), omitBatch("i-000001")}
	provider.atomicFail = func(name string) bool { return name == items[1].Name }
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 3, StartItemsPerCall: 3})
	if err != nil {
		t.Fatalf("RunOpManyRecover returned an error; PL-009's contract is a nil error with the failure named in BatchResult: %v", err)
	}
	if result.Complete() {
		t.Fatal("Complete() = true with an unrecoverable item")
	}
	if result.Summary.Succeeded != 2 || result.Summary.Failed != 1 {
		t.Fatalf("Summary = %+v, want 2 succeeded, 1 failed", result.Summary)
	}
	failed := result.Items[1]
	if failed.Status != types.ItemFailed {
		t.Fatalf("item 1: Status = %s, want failed", failed.Status)
	}
	if failed.Mode != types.ModeAtomicFallback {
		t.Fatalf("item 1: Mode = %q, want %q", failed.Mode, types.ModeAtomicFallback)
	}
	if failed.Err == nil {
		t.Fatal("item 1: Err is nil for a failed item")
	}
	failures := result.Failures()
	if len(failures) != 1 || failures[0].Index != 1 {
		t.Fatalf("Failures() = %+v, want exactly index 1", failures)
	}
}

// --- 9: every input item appears in the result exactly once, whatever mix
// of MDSP, isolate, and atomic paths produced it.

func TestRunOpManyRecoverEveryItemExactlyOnce(t *testing.T) {
	items := recoverWidgets(9)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{
		omitBatch("i-000003", "i-000007"), // chunk 1 (9 items): omit index 2 and 6
		omitBatch("i-000002"),             // isolate pass over {2, 6}: still omit index 6 (local id 2)
	}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 9, StartItemsPerCall: 9})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if len(result.Items) != len(items) {
		t.Fatalf("len(Items) = %d, want %d", len(result.Items), len(items))
	}
	seen := make(map[int]int, len(items))
	for _, item := range result.Items {
		seen[item.Index]++
	}
	for i := range items {
		if seen[i] != 1 {
			t.Fatalf("item %d appears %d time(s) in the result, want exactly 1", i, seen[i])
		}
	}
	if result.Summary.Succeeded+result.Summary.Failed+result.Summary.Cancelled != len(items) {
		t.Fatalf("Summary counts do not sum to Total: %+v", result.Summary)
	}
	// Every succeeded item's value is byte-identical to what its own
	// resolving pass produced -- no cross-item contamination from the
	// isolate/atomic bookkeeping.
	for i, item := range result.Items {
		if item.Status == types.ItemSucceeded && item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d", i, item.Value.Length, len(items[i].Name))
		}
	}
}

// --- 10: AdaptiveChunkState actually shrinks the top-level chunk size after
// a real omission, and the next top-level chunk is planned at the smaller
// size -- PL-005's "no caller" gap, closed.

func TestRunOpManyRecoverAdaptiveShrinksAfterOmission(t *testing.T) {
	items := recoverWidgets(12)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{
		omitBatch("i-000006"), // chunk 1 (6 items): omit the last one -> halves 6 -> 3
		fullSuccessBatch,      // isolate pass over the 1 omitted item
	}
	// batchDefault (fullSuccessBatch) answers every call after that.
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 6, StartItemsPerCall: 6})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false: %+v", result.Summary)
	}
	// Without the shrink: chunk1(6)+isolate(1)+chunk2(6) = 3 calls for 12
	// items. With the shrink to 3 after chunk1's omission: chunk1(6) +
	// isolate(1) + chunk2(3) + chunk3(3) = 4 calls. The count is the
	// observable proof of which one happened.
	batchCalls, atomicCalls := provider.counts()
	if atomicCalls != 0 {
		t.Fatalf("atomicCalls = %d, want 0 (isolation resolved everything)", atomicCalls)
	}
	if batchCalls != 4 {
		t.Fatalf("batchCalls = %d, want 4 (proof the chunk size shrank to 3 after the omission, splitting the remaining 6 items into two chunks)", batchCalls)
	}
	for i, item := range result.Items {
		if item.Value.Length != len(items[i].Name) {
			t.Fatalf("item %d: Value.Length = %d, want %d", i, item.Value.Length, len(items[i].Name))
		}
	}
}

// --- 11: zero items is a valid, empty, no-call result.

func TestRunOpManyRecoverZeroItems(t *testing.T) {
	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), nil, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if result.Summary.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("result = %+v, want an empty result", result)
	}
	batchCalls, atomicCalls := provider.counts()
	if batchCalls != 0 || atomicCalls != 0 {
		t.Fatalf("batchCalls=%d atomicCalls=%d, want 0/0 for zero items", batchCalls, atomicCalls)
	}
}

// --- 12: PL-013's Omissions and AtomicFallbacks counters read real numbers
// off a PL-009 cascade's own BatchResult, computed from Mode and Attempts
// rather than tracked as a second, hand-kept counter.

func TestRunOpManyRecoverBatchMetricsCountsOmissionsAndFallbacks(t *testing.T) {
	items := recoverWidgets(6)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{
		omitBatch("i-000002", "i-000004"), // chunk (6 items): omit index 1 and 3
		omitBatch("i-000002"),             // isolate over {1, 3}: index 3 (local id 2) still missing
	}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 6, StartItemsPerCall: 6})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("Complete() = false: %+v", result.Summary)
	}

	metrics := result.Metrics()
	// index 1 recovered via isolate (Omission, not AtomicFallback); index 3
	// needed the atomic fallback (both an Omission and an AtomicFallback).
	if metrics.Omissions != 2 {
		t.Fatalf("Omissions = %d, want 2", metrics.Omissions)
	}
	if metrics.AtomicFallbacks != 1 {
		t.Fatalf("AtomicFallbacks = %d, want 1", metrics.AtomicFallbacks)
	}
	if metrics.ValidItemRatio != 1.0 {
		t.Fatalf("ValidItemRatio = %v, want 1.0", metrics.ValidItemRatio)
	}
}

// --- 13: a plain RunOpManyPartial result (PL-008, no MDSP involved at all)
// reports zero Omissions and zero AtomicFallbacks -- the honest-zero this
// task's own doc comment promises, not a metric that silently never applies.

func TestBatchMetricsPartialResultReportsZeroOmissionsAndFallbacks(t *testing.T) {
	t.Setenv("SCHEMAFLUX_MODEL", "fake-model")
	items := indexedItems(4)
	provider := newKeyedProvider("fake-model", false)
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyPartial(ctx, echoOp(), items, types.OpOptions{},
		PartialConfig{Policy: types.PolicyCollectFailures, Concurrency: 2})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	metrics := result.Metrics()
	if metrics.Omissions != 0 || metrics.AtomicFallbacks != 0 {
		t.Fatalf("Omissions=%d AtomicFallbacks=%d, want 0/0 for an atomic-only run", metrics.Omissions, metrics.AtomicFallbacks)
	}
}

// --- 14: cost is priced end to end when the model has a rate card entry,
// and unpriced never silently becomes free.

func TestRunOpManyRecoverCostPricedWhenModelHasRateCard(t *testing.T) {
	items := recoverWidgets(4)
	provider := newRecoverFakeProvider("gpt-4")
	provider.priced = true
	t.Setenv("SCHEMAFLUX_MODEL", "gpt-4")
	ctx := WithProvider(context.Background(), provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 4, StartItemsPerCall: 4})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	metrics := result.Metrics()
	if !metrics.CostPriced {
		t.Fatal("CostPriced = false against gpt-4, a model with a rate card entry")
	}
	if metrics.AcceptedCost <= 0 {
		t.Fatalf("AcceptedCost = %v, want > 0", metrics.AcceptedCost)
	}
}

// --- 15: RunOpManyRecover refuses cleanly when the op has no batch Encode,
// rather than panicking or silently running atomic-only.

func TestRunOpManyRecoverRequiresBatchEncode(t *testing.T) {
	op := Op[string, string]{
		ID:        types.OperationID{Name: "noBatch", Version: "v1"},
		Semantics: types.Semantics{Stability: types.StabilityExperimental},
		Contract:  OutputContract[string]{Decode: func(body string) (string, error) { return body, nil }},
		Batch:     BatchAlgebra[string, string]{Class: types.BatchNone},
		BuildPrompt: func(input string, _ types.OpOptions) (string, string) {
			return "echo", input
		},
	}
	codec := BatchItemCodec[string, string]{DecodeItem: func(raw json.RawMessage) (string, error) { return string(raw), nil }}

	_, err := RunOpManyRecover(context.Background(), op, []string{"a"}, codec, types.OpOptions{}, RecoverConfig{})
	if err == nil {
		t.Fatal("RunOpManyRecover with no Batch.Encode returned no error")
	}
}
