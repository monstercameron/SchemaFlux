package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config/configtest"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// The progressive recovery cascade, and the counters
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

// requestDeadline is what a request's context looked like when it arrived.
type requestDeadline struct {
	bounded bool
	left    time.Duration
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

// placeOf is the place of an item in the request it was offered in, from the name
// these tests give it: "i-000003" is the third. The ids the library offers are
// random and new for every request, so a handler that has to leave out or repeat
// one item finds it by its place in the items it parsed, never by an id it could
// have known.
func placeOf(name string) int {
	number, err := strconv.Atoi(strings.TrimPrefix(name, "i-"))
	if err != nil {
		return -1
	}
	return number - 1
}

// realIDs writes the ids the library offered into text that names items by place.
func realIDs(text string, tagged []taggedItem[recoverWidget]) string {
	for place, t := range tagged {
		text = strings.ReplaceAll(text, fmt.Sprintf("i-%06d", place+1), t.ID)
	}
	return text
}

// omitBatch answers every id except the items named in skip, by place.
func omitBatch(skip ...string) recoverBatchHandler {
	skipSet := map[int]bool{}
	for _, s := range skip {
		skipSet[placeOf(s)] = true
	}
	return func(tagged []taggedItem[recoverWidget]) (string, error) {
		var items []idBatchItem
		for place, t := range tagged {
			if skipSet[place] {
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

// duplicateBatch answers every id normally but answers the item named dupID, by
// place, twice, with two different (and therefore untrustworthy) payloads.
func duplicateBatch(dupID string) recoverBatchHandler {
	return func(tagged []taggedItem[recoverWidget]) (string, error) {
		var items []idBatchItem
		for place, t := range tagged {
			items = append(items, idBatchItem{ID: t.ID, Output: json.RawMessage(fmt.Sprintf(`{"length":%d}`, len(t.Item.Name)))})
			if place == placeOf(dupID) {
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

	// batchHook, when set, runs first for every batched request with the
	// request's context and its index, and may fail it or hold it until the
	// context ends.
	batchHook func(ctx context.Context, call int) error

	// answeredBy, when set, names the route that answers each batched request
	// (llm.CompletionResponse.Provider), by the request's index.
	answeredBy func(call int) string

	// batchFinish, when set, is the finish reason of each batched response, by the
	// request's index: "length" is an answer cut off, "content_filter" one the
	// provider's filter withheld.
	batchFinish func(call int) string

	// atomicFail reports whether an atomic fallback call for this user
	// prompt (the widget's Name) should fail, and atomicErr, when it returns an
	// error, fails the call with that error instead.
	atomicFail  func(name string) bool
	atomicErr   func(name string) error
	atomicCalls int

	// completed is how many requests were answered, as opposed to failed.
	completed int

	// deadlines holds, for each request that reached the provider, whether its
	// context was bounded and how long it had left, split into the batched
	// requests and the atomic ones.
	batchDeadlines, atomicDeadlines []requestDeadline

	model  string
	priced bool
}

func newRecoverFakeProvider(model string) *recoverFakeProvider {
	return &recoverFakeProvider{model: model, batchDefault: fullSuccessBatch}
}

func (p *recoverFakeProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	isBatch := strings.Contains(req.UserPrompt, "Process these items:")
	seen := requestDeadline{}
	if deadline, bounded := ctx.Deadline(); bounded {
		seen = requestDeadline{bounded: true, left: time.Until(deadline)}
	}
	p.mu.Lock()
	if isBatch {
		p.batchDeadlines = append(p.batchDeadlines, seen)
	} else {
		p.atomicDeadlines = append(p.atomicDeadlines, seen)
	}
	p.mu.Unlock()

	var body string
	var err error
	answeredBy := ""
	finish := "stop"
	var finishFor func(call int) string
	batchIndex := 0

	if isBatch {
		p.mu.Lock()
		idx := p.batchCalls
		p.batchCalls++
		handler := p.batchDefault
		if idx < len(p.batchScript) {
			handler = p.batchScript[idx]
		}
		hook, route := p.batchHook, p.answeredBy
		finishFor = p.batchFinish
		p.mu.Unlock()
		batchIndex = idx

		if hook != nil {
			if hookErr := hook(ctx, idx); hookErr != nil {
				return llm.CompletionResponse{}, hookErr
			}
		}
		if route != nil {
			answeredBy = route(idx)
		}
		tagged := parseTaggedBatchRequest(req.UserPrompt)
		body, err = handler(tagged)
		if finishFor != nil {
			finish = finishFor(batchIndex)
		}
	} else {
		p.mu.Lock()
		p.atomicCalls++
		fail := p.atomicFail != nil && p.atomicFail(req.UserPrompt)
		var forced error
		if p.atomicErr != nil {
			forced = p.atomicErr(req.UserPrompt)
		}
		p.mu.Unlock()

		if forced != nil {
			return llm.CompletionResponse{}, forced
		}
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
	p.mu.Lock()
	p.completed++
	p.mu.Unlock()
	provider := "recover-fake"
	if answeredBy != "" {
		provider = answeredBy
	}
	return llm.CompletionResponse{
		Content:      body,
		Provider:     provider,
		Model:        p.model,
		FinishReason: finish,
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
	payload := user
	if at := strings.Index(user, prefix); at >= 0 {
		// A steered request carries the caller's instructions in front of the batch.
		payload = user[at+len(prefix):]
	}
	var tagged []taggedItem[recoverWidget]
	_ = json.Unmarshal([]byte(payload), &tagged)
	return tagged
}

func recoverCtx(t *testing.T, provider llm.Provider) context.Context {
	t.Helper()
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
		t.Fatalf("RunOpManyRecover returned an error; the contract is a nil error with the failure named in BatchResult: %v", err)
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
// size.

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

// --- 12: the Omissions and AtomicFallbacks counters read real numbers
// off a cascade's own BatchResult, computed from Mode and Attempts
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

// --- 13: a plain RunOpManyPartial result (no MDSP involved at all)
// reports zero Omissions and zero AtomicFallbacks -- the honest-zero this
// task's own doc comment promises, not a metric that silently never applies.

func TestBatchMetricsPartialResultReportsZeroOmissionsAndFallbacks(t *testing.T) {
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
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
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-4")
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

// A rate limit, an outage or a timeout is not the model breaking the protocol
// and a smaller chunk does not cure it. Each was read as a malformed protocol,
// which halves the chunk size.
func TestRunOpManyRecoverTransportErrorsDoNotShrinkTheChunk(t *testing.T) {
	failWith := func(kind types.ErrorKind) recoverBatchHandler {
		return func([]taggedItem[recoverWidget]) (string, error) {
			return "", &types.OperationError{Kind: kind, Op: "test", Message: "scripted"}
		}
	}
	for _, kind := range []types.ErrorKind{types.KindRateLimited, types.KindProviderUnavailable, types.KindTimeout} {
		t.Run(kind.String(), func(t *testing.T) {
			items := recoverWidgets(12)
			provider := newRecoverFakeProvider("fake-model")
			// The first chunk's request fails in transport, and its items are asked one
			// request each at the end of the run; every later call succeeds.
			provider.batchScript = []recoverBatchHandler{failWith(kind)}
			ctx := recoverCtx(t, provider)

			result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 6, StartItemsPerCall: 6})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			if !result.Complete() {
				t.Fatalf("Complete() = false: %+v", result.Summary)
			}
			// Chunk one: one failed request. Chunk two keeps the size of six, so it is
			// one call; had the size halved it would be two. The six items of chunk one
			// are asked alone, and the failed request is not sent again.
			batchCalls, atomicCalls := provider.counts()
			if batchCalls != 2 || atomicCalls != 6 {
				t.Fatalf("batchCalls = %d, atomicCalls = %d, want 2 and 6: the chunk size changed after a transport error, or the failed request was sent again", batchCalls, atomicCalls)
			}
			// The ledger says why the size was left alone, and does not record the
			// failure as an outcome it could not interpret.
			explained := result.Ledger.Explain()
			if !strings.Contains(explained, "says nothing about the chunk size") {
				t.Errorf("the ledger does not say why the size was left alone: %s", explained)
			}
			if strings.Contains(explained, "ignored an outcome") {
				t.Errorf("the ledger records a transport error as an unknown outcome: %s", explained)
			}
		})
	}
}

// A malformed protocol is still a sizing signal.
func TestRunOpManyRecoverMalformedBodyStillShrinksTheChunk(t *testing.T) {
	items := recoverWidgets(12)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{malformedBatch, malformedBatch}
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 6, StartItemsPerCall: 6})
	if err != nil || !result.Complete() {
		t.Fatalf("RunOpManyRecover: %v, %+v", err, result.Summary)
	}
	// The first chunk is asked, and then each half of it (the second of those is
	// answered); the chunk size is three, so the second six items are two requests.
	// Had the size not halved, the run would have made four.
	if batchCalls, _ := provider.counts(); batchCalls != 5 {
		t.Fatalf("batchCalls = %d, want 5: a malformed body should halve the chunk to 3", batchCalls)
	}
}

func TestOutcomeForCallErrReadsTheKind(t *testing.T) {
	cases := []struct {
		kind types.ErrorKind
		want AdaptiveOutcome
	}{
		{types.KindOutputTruncated, OutcomeTruncated},
		{types.KindContextTooLarge, OutcomeTruncated},
		{types.KindRateLimited, outcomeNoSignal},
		{types.KindProviderUnavailable, outcomeNoSignal},
		{types.KindTimeout, outcomeNoSignal},
		{types.KindCircuitOpen, outcomeNoSignal},
		// A scheduler that refuses to queue the request says its queue is full,
		// which is nothing about how large the chunk was.
		{types.KindAdmissionRejected, outcomeNoSignal},
		{types.KindMalformedOutput, OutcomeMalformedProtocol},
		{types.KindSchemaViolation, OutcomeMalformedProtocol},
		{types.KindUnknown, OutcomeMalformedProtocol},
		{types.KindInvalidRequest, OutcomeMalformedProtocol},
	}
	for _, tc := range cases {
		err := fmt.Errorf("wrapped: %w", &types.OperationError{Kind: tc.kind})
		if got := outcomeForCallErr(err); got != tc.want {
			t.Errorf("kind %v: outcome = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// Nothing comes back beside an error.
func TestRunOpManyRecoverReturnsAnEmptyResultBesideAnError(t *testing.T) {
	op := recoverWidgetOp()
	op.Batch.Encode = nil
	result, err := RunOpManyRecover(context.Background(), op, recoverWidgets(3), recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if types.KindOf(err) != types.KindConfiguration {
		t.Errorf("kind = %v, want KindConfiguration", types.KindOf(err))
	}
	if result.Summary.Total != 0 || result.Items != nil {
		t.Errorf("a result came back beside the error: %+v", result.Summary)
	}
	op = recoverWidgetOp()
	op.BuildPrompt = nil
	if result, err := RunOpManyRecover(context.Background(), op, recoverWidgets(3), recoverCodec(), types.OpOptions{}, RecoverConfig{}); err == nil || result.Summary.Total != 0 {
		t.Errorf("no BuildPrompt: %v, %+v", err, result.Summary)
	}
	op = recoverWidgetOp()
	op.Contract.Decode = nil
	if result, err := RunOpManyRecover(context.Background(), op, recoverWidgets(3), recoverCodec(), types.OpOptions{}, RecoverConfig{}); err == nil || result.Summary.Total != 0 {
		t.Errorf("no Decode: %v, %+v", err, result.Summary)
	}

	// A contract floor above what a batch establishes refuses the run after the
	// batch has been sent; the answer that was decoded is not handed back.
	ctx := WithContractPolicy(recoverCtx(t, newRecoverFakeProvider("fake-model")), types.DataPolicy{MinimumContract: types.ContractFullyGoverned})
	result, err = RunOpManyRecover(ctx, recoverWidgetOp(), recoverWidgets(3), recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err == nil {
		t.Fatal("a floor above what a batch establishes was not refused")
	}
	if types.KindOf(err) != types.KindPolicyViolation {
		t.Errorf("kind = %v, want KindPolicyViolation", types.KindOf(err))
	}
	if result.Summary.Total != 0 || result.Items != nil || result.Summary.PolicyReason != "" {
		t.Errorf("a result came back beside the floor refusal: %+v", result.Summary)
	}
}

// An encode that fails is not tried again on a further pass: the items go
// straight to the atomic fallback.
func TestRunOpManyRecoverEncodeErrorGoesStraightToAtomic(t *testing.T) {
	op := recoverWidgetOp()
	encodes := 0
	op.Batch.Encode = func([]recoverWidget, types.OpOptions) (string, string, error) {
		encodes++
		return "", "", fmt.Errorf("cannot encode")
	}
	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)
	result, err := RunOpManyRecover(ctx, op, recoverWidgets(3), recoverCodec(), types.OpOptions{},
		RecoverConfig{MaxItemsPerCall: 3, StartItemsPerCall: 3, MaxIsolatePasses: 3})
	if err != nil || !result.Complete() {
		t.Fatalf("RunOpManyRecover: %v, %+v", err, result.Summary)
	}
	// Once for the plan, and once for the pass: the failed encode is not tried again
	// on an isolate pass.
	if encodes != 2 {
		t.Errorf("Encode ran %d times, want 2", encodes)
	}
	for i, item := range result.Items {
		if item.Mode != types.ModeAtomicFallback {
			t.Errorf("item %d: Mode = %q", i, item.Mode)
		}
	}
}

// A step that panics, or ends its goroutine, in the last rung of the cascade is
// that item's failure and not the process's: the other items still get their
// call, and the run reports the item as failed. The work was run on goroutines
// of the library's own with no guard, so one panic ended the program.
func TestRunOpManyRecoverAtomicFallbackSurvivesAStepThatPanicsOrEndsItsGoroutine(t *testing.T) {
	for _, concurrency := range []int{1, 2, 5} {
		t.Run(fmt.Sprintf("concurrency %d", concurrency), func(t *testing.T) {
			op := recoverWidgetOp()
			op.Batch.Encode = func([]recoverWidget, types.OpOptions) (string, string, error) {
				return "", "", fmt.Errorf("cannot encode")
			}
			op.BuildPrompt = func(w recoverWidget, _ types.OpOptions) (string, string) {
				switch len(w.Name) {
				case 2:
					panic("secret payload in a panic")
				case 4:
					runtime.Goexit()
				}
				return "summarize one widget", w.Name
			}
			provider := newRecoverFakeProvider("fake-model")
			ctx := recoverCtx(t, provider)

			items := recoverWidgets(5)
			result, err := RunOpManyRecover(ctx, op, items, recoverCodec(), types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5, Concurrency: concurrency})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			if result.Summary.Total != 5 || result.Summary.Succeeded != 3 || result.Summary.Failed != 2 {
				t.Fatalf("Summary = %+v, want 5 items, 3 succeeded, 2 failed", result.Summary)
			}
			for i, item := range result.Items {
				if item.Mode != types.ModeAtomicFallback {
					t.Errorf("item %d: Mode = %q, want %q", i, item.Mode, types.ModeAtomicFallback)
				}
				if i == 1 || i == 3 {
					if item.Status != types.ItemFailed {
						t.Errorf("item %d: Status = %s, want failed", i, item.Status)
					}
					if types.KindOf(item.Err) != types.KindInvariantViolation {
						t.Errorf("item %d: error kind = %v, want KindInvariantViolation (%v)", i, types.KindOf(item.Err), item.Err)
					}
					if item.Err != nil && strings.Contains(item.Err.Error(), "secret payload") {
						t.Errorf("item %d: the error carries the panic's value: %v", i, item.Err)
					}
					if item.Value != (recoverSummary{}) {
						t.Errorf("item %d: a value beside the failure: %+v", i, item.Value)
					}
					continue
				}
				if item.Status != types.ItemSucceeded || item.Value.Length != len(items[i].Name) {
					t.Errorf("item %d: %+v, want it to have succeeded with its own length", i, item)
				}
			}
		})
	}
}

// elementBatch answers every id normally except the third offered one, whose
// element is the given text, written in place of what the model should have said.
func elementBatch(third string) recoverBatchHandler {
	return func(tagged []taggedItem[recoverWidget]) (string, error) {
		elements := make([]string, len(tagged))
		for i, t := range tagged {
			elements[i] = fmt.Sprintf(`{"id":%q,"output":{"length":%d}}`, t.ID, len(t.Item.Name))
			if i == 2 {
				elements[i] = realIDs(third, tagged)
			}
		}
		return `{"items":[` + strings.Join(elements, ",") + `]}`, nil
	}
}

// One bad element costs the item it answers and not its chunk: the other four are
// kept from the first pass and only the third is asked again, alone, so the
// recovery is two calls and no atomic fallback.
func TestRunOpManyRecoverAsksAgainOnlyForTheItemWhoseElementWasBad(t *testing.T) {
	for name, third := range map[string]string{
		"no output":         `{"id":"i-000003"}`,
		"a null output":     `{"id":"i-000003","output":null}`,
		"no id":             `{"output":{"length":3}}`,
		"a null id":         `{"id":null,"output":{"length":3}}`,
		"a null element":    `null`,
		"an id nobody sent": `{"id":"i-999999","output":{"length":3}}`,
		"a repeated id":     `{"id":"i-000002","output":{"length":2}}`,
	} {
		t.Run(name, func(t *testing.T) {
			items := recoverWidgets(5)
			provider := newRecoverFakeProvider("fake-model")
			provider.batchScript = []recoverBatchHandler{elementBatch(third), fullSuccessBatch}
			ctx := recoverCtx(t, provider)

			result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			if !result.Complete() {
				t.Fatalf("Complete() = false: %+v", result.Summary)
			}
			asked := map[int]int{2: 2}
			if name == "a repeated id" {
				// The id answered twice has no answer to trust, so it is asked again
				// with the third item.
				asked[1] = 2
			}
			for i, item := range result.Items {
				want := 1
				if again, ok := asked[i]; ok {
					want = again
				}
				if item.Attempts != want || item.Value.Length != len(items[i].Name) || item.Mode != ModeMDSP {
					t.Errorf("item %d: %+v, want %d attempts and its own answer by MDSP", i, item, want)
				}
			}
			batchCalls, atomicCalls := provider.counts()
			if batchCalls != 2 || atomicCalls != 0 {
				t.Errorf("batchCalls=%d atomicCalls=%d, want 2/0", batchCalls, atomicCalls)
			}
		})
	}
}

// RunOpBatch is the path that must not return a partial batch: any bad element
// refuses the whole answer, and nothing is returned beside the error.
func TestRunOpBatchRefusesTheWholeAnswerWhenOneElementIsBad(t *testing.T) {
	for name, third := range map[string]string{
		"no output":      `{"id":"i-000003"}`,
		"a null output":  `{"id":"i-000003","output":null}`,
		"no id":          `{"output":{"length":3}}`,
		"a null element": `null`,
		"not an object":  `7`,
	} {
		t.Run(name, func(t *testing.T) {
			provider := newRecoverFakeProvider("fake-model")
			provider.batchDefault = elementBatch(third)
			ctx := recoverCtx(t, provider)

			outputs, err := RunOpBatch(ctx, recoverWidgetOp(), recoverWidgets(5), types.OpOptions{})
			if err == nil || outputs != nil {
				t.Fatalf("outputs = %+v, err = %v, want the whole answer refused", outputs, err)
			}
		})
	}
	provider := newRecoverFakeProvider("fake-model")
	outputs, err := RunOpBatch(recoverCtx(t, provider), recoverWidgetOp(), recoverWidgets(5), types.OpOptions{})
	if err != nil || len(outputs) != 5 || outputs[4].Length != 5 {
		t.Errorf("a complete answer: %+v, %v", outputs, err)
	}
}

// A nil context is the caller's mistake. It was not looked at, so it reached the
// call recording and panicked inside the cascade. It is refused as an invalid
// request, with nothing sent and an empty result, whatever the items and the
// configuration.
func TestRunOpManyRecoverRefusesANilContext(t *testing.T) {
	configs := []RecoverConfig{{}, {MaxItemsPerCall: 2}, {MaxItemsPerCall: 5, StartItemsPerCall: 5}, {Concurrency: 3}}
	for _, count := range []int{0, 1, 5} {
		for i, cfg := range configs {
			t.Run(fmt.Sprintf("%d items, configuration %d", count, i), func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("RunOpManyRecover panicked on a nil context: %v", r)
					}
				}()
				var noContext context.Context
				result, err := RunOpManyRecover(noContext, recoverWidgetOp(), recoverWidgets(count), recoverCodec(), types.OpOptions{}, cfg)
				if types.KindOf(err) != types.KindInvalidRequest || !errors.Is(err, types.ErrInvalidRequest) {
					t.Fatalf("err = %v, want an invalid request", err)
				}
				requireNoBatchResult(t, result)
			})
		}
	}
}

// What the cascade cannot be run for is refused before anything is sent, as a
// configuration error and with an empty result. A codec with no DecodeItem was
// not looked at, and the first batched answer was decoded through a nil function.
func TestRunOpManyRecoverRefusesAnUnusableCallBeforeSendingAnything(t *testing.T) {
	type defect struct {
		name string
		set  func(*Op[recoverWidget, recoverSummary], *BatchItemCodec[recoverWidget, recoverSummary])
		want types.ErrorKind
		ctx  func(context.Context) context.Context
	}
	defects := []defect{
		{"no batch Encode", func(op *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			op.Batch.Encode = nil
		}, types.KindConfiguration, nil},
		{"no BuildPrompt", func(op *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			op.BuildPrompt = nil
		}, types.KindConfiguration, nil},
		{"no Decode", func(op *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			op.Contract.Decode = nil
		}, types.KindConfiguration, nil},
		{"no DecodeItem", func(_ *Op[recoverWidget, recoverSummary], codec *BatchItemCodec[recoverWidget, recoverSummary]) {
			codec.DecodeItem = nil
		}, types.KindConfiguration, nil},
		{"a floor the batch cannot reach", func(*Op[recoverWidget, recoverSummary], *BatchItemCodec[recoverWidget, recoverSummary]) {},
			types.KindPolicyViolation, func(ctx context.Context) context.Context {
				return WithContractPolicy(ctx, types.DataPolicy{MinimumContract: types.ContractFullyGoverned})
			}},
	}
	for _, d := range defects {
		for _, count := range []int{0, 1, 3} {
			t.Run(fmt.Sprintf("%s over %d items", d.name, count), func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("a panic escaped: %v", r)
					}
				}()
				provider := newRecoverFakeProvider("fake-model")
				ctx := recoverCtx(t, provider)
				if d.ctx != nil {
					ctx = d.ctx(ctx)
				}
				op, codec := recoverWidgetOp(), recoverCodec()
				d.set(&op, &codec)

				result, err := RunOpManyRecover(ctx, op, recoverWidgets(count), codec, types.OpOptions{}, RecoverConfig{})
				if types.KindOf(err) != d.want {
					t.Fatalf("err = %v, want kind %v", err, d.want)
				}
				requireNoBatchResult(t, result)
				if batchCalls, atomicCalls := provider.counts(); batchCalls+atomicCalls != 0 {
					t.Errorf("%d requests were sent", batchCalls+atomicCalls)
				}
			})
		}
	}
}

// Nothing a caller can read names a task or a protocol by the name its code gave
// it: not the summary's reason, not the ledger, and not the error of any item,
// whichever rung the item failed on. Every error is classified.
func TestRunOpManyRecoverTextsCarryNoTicketOrJargonAndEveryErrorHasAKind(t *testing.T) {
	failAll := func(kind types.ErrorKind) recoverBatchHandler {
		return func([]taggedItem[recoverWidget]) (string, error) {
			return "", &types.OperationError{Kind: kind, Op: "test", Message: "scripted"}
		}
	}
	atomicFailsAlways := func(string) bool { return true }
	cases := []struct {
		name         string
		batch        recoverBatchHandler
		atomicFail   func(string) bool
		encodeFails  bool
		invariantBad bool
	}{
		{name: "a refusal by policy stops the cascade", batch: failAll(types.KindPolicyViolation)},
		{name: "a spent budget stops the cascade", batch: failAll(types.KindBudgetExceeded)},
		{name: "a key the provider refuses stops the cascade", batch: failAll(types.KindAuthentication)},
		{name: "a configuration that cannot run stops the cascade", batch: failAll(types.KindConfiguration)},
		{name: "an outage in every rung", batch: failAll(types.KindProviderUnavailable), atomicFail: atomicFailsAlways},
		{name: "a malformed body in every rung", batch: malformedBatch, atomicFail: atomicFailsAlways},
		{name: "every id omitted in every rung", batch: omitBatch("i-000001", "i-000002", "i-000003", "i-000004"), atomicFail: atomicFailsAlways},
		{name: "an id invented and the real ones omitted", batch: inventBatch("i-999999"), atomicFail: atomicFailsAlways},
		{name: "an encode that fails, then the atomic rung failing", encodeFails: true, atomicFail: atomicFailsAlways},
		{name: "an invariant broken, then the atomic rung failing", invariantBad: true, atomicFail: atomicFailsAlways},
		{name: "a duplicated id", batch: duplicateBatch("i-000002")},
		{name: "everything works", batch: fullSuccessBatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newRecoverFakeProvider("fake-model")
			if tc.batch != nil {
				provider.batchDefault = tc.batch
			}
			provider.atomicFail = tc.atomicFail
			op := recoverWidgetOp()
			if tc.encodeFails {
				op.Batch.Encode = func([]recoverWidget, types.OpOptions) (string, string, error) {
					return "", "", fmt.Errorf("cannot encode")
				}
			}
			if tc.invariantBad {
				op.Contract.Invariants = []func(recoverSummary) error{func(recoverSummary) error { return errors.New("never good enough") }}
			}
			result, err := RunOpManyRecover(recoverCtx(t, provider), op, recoverWidgets(4), recoverCodec(), types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 2, StartItemsPerCall: 2})
			if err != nil {
				requireNoTicketOrJargon(t, err.Error())
				if types.KindOf(err) == types.KindUnknown {
					t.Errorf("an unclassified error left the call: %v", err)
				}
			}
			texts := []string{result.Summary.PolicyReason, result.Ledger.Explain()}
			for i, item := range result.Items {
				if item.Err == nil {
					continue
				}
				if types.KindOf(item.Err) == types.KindUnknown {
					t.Errorf("item %d has an unclassified error: %v", i, item.Err)
				}
				texts = append(texts, item.Err.Error())
			}
			for _, text := range texts {
				requireNoTicketOrJargon(t, text)
			}
		})
	}
}

// The atomic rung is bounded as the other engines are: by the limit of the
// client's scheduler when it sets one, and otherwise by the default. It had a
// bound of its own that ignored the scheduler.
func TestRunOpManyRecoverAtomicRungSharesTheBoundOfTheOtherEngines(t *testing.T) {
	cases := []struct {
		name     string
		ctx      func(*testing.T, *gatedProvider) context.Context
		workers  int
		wantPeak int
	}{
		{"no scheduler", func(_ *testing.T, p *gatedProvider) context.Context { return gatedContext(p) }, 0, DefaultConcurrency},
		{"an explicit number wins", func(_ *testing.T, p *gatedProvider) context.Context { return gatedContext(p) }, 2, 2},
		{"a scheduler of one", func(t *testing.T, p *gatedProvider) context.Context {
			ctx, _ := schedulerContext(t, p, SchedulerLimits{MaxConcurrent: 1})
			return ctx
		}, 0, 1},
		{"a scheduler of three", func(t *testing.T, p *gatedProvider) context.Context {
			ctx, _ := schedulerContext(t, p, SchedulerLimits{MaxConcurrent: 3})
			return ctx
		}, 0, 3},
		{"a scheduler of seven", func(t *testing.T, p *gatedProvider) context.Context {
			ctx, _ := schedulerContext(t, p, SchedulerLimits{MaxConcurrent: 7})
			return ctx
		}, 0, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newGatedProvider()
			ctx := tc.ctx(t, provider)
			op := widgetOp()
			op.Batch.Encode = func([]widget, types.OpOptions) (string, string, error) { return "", "", errors.New("cannot encode") }
			codec := BatchItemCodec[widget, widgetSummary]{DecodeItem: func(raw json.RawMessage) (widgetSummary, error) {
				var s widgetSummary
				return s, json.Unmarshal(raw, &s)
			}}

			type returned struct {
				result types.BatchResult[widgetSummary]
				err    error
			}
			done := make(chan returned, 1)
			go func() {
				result, err := RunOpManyRecover(ctx, op, widgets(30), codec, types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 30, Concurrency: tc.workers})
				done <- returned{result, err}
			}()
			provider.awaitArrivals(t, tc.wantPeak)
			settle()
			if inFlight, _, _, _ := provider.snapshot(); inFlight != tc.wantPeak {
				t.Fatalf("%d calls in flight, want %d", inFlight, tc.wantPeak)
			}
			provider.open()
			got := <-done
			if got.err != nil || !got.result.Complete() {
				t.Fatalf("result %+v, error %v", got.result.Summary, got.err)
			}
			if _, peak, _, _ := provider.snapshot(); peak != tc.wantPeak {
				t.Errorf("peak = %d, want %d", peak, tc.wantPeak)
			}
		})
	}
}

// recoverSchemaOp is recoverWidgetOp with a schema named and an invariant, so
// that a batch of it establishes the level above well-formed JSON on a route that
// enforces schemas.
func recoverSchemaOp() Op[recoverWidget, recoverSummary] {
	op := recoverWidgetOp()
	op.Contract.SchemaName = "recoverSummary"
	op.Contract.Invariants = []func(recoverSummary) error{func(recoverSummary) error { return nil }}
	return op
}

// The level a route delivered is enforced after each batched request, as the
// floor is before the first. The route that answers may not be the one that was
// planned, a fallback's alternate say, and may enforce less. The cascade accepted
// every item of every chunk and checked the floor only once, on the way in. A
// refusal stops the cascade, the items already resolved are kept, and the others
// carry the refusal; nothing the refused request decoded is accepted.
func TestRunOpManyRecoverIsHeldToTheLevelEachRequestDelivered(t *testing.T) {
	const (
		planned   = "recover-fake"
		elsewhere = "elsewhere"
	)
	allPlanned := func(int) string { return planned }
	only := func(elsewhereAt ...int) func(int) string {
		return func(call int) string {
			for _, at := range elsewhereAt {
				if call == at {
					return elsewhere
				}
			}
			return planned
		}
	}
	cases := []struct {
		name       string
		native     bool // the planned route is registered as enforcing schemas
		floor      types.ContractLevel
		route      func(int) string
		items      int
		perChunk   int
		omitFirst  bool // the first request omits the third item, so there is an isolate pass
		resolved   int  // items that succeed, which are the first ones
		batchCalls int
		refused    bool
	}{
		{name: "no floor, answered by a route that enforces nothing", native: true, floor: types.ContractPromptOnly, route: only(0), items: 4, perChunk: 4, resolved: 4, batchCalls: 1},
		{name: "a floor of well-formed JSON, answered by a route that enforces nothing", native: true, floor: types.ContractJSONWellFormed, route: only(0), items: 4, perChunk: 4, resolved: 4, batchCalls: 1},
		{name: "a floor of schema constrained, the planned route answers", native: true, floor: types.ContractSchemaConstrained, route: allPlanned, items: 4, perChunk: 4, resolved: 4, batchCalls: 1},
		{name: "the highest floor, the planned route answers", native: true, floor: types.ContractSchemaAndInvariantChecked, route: allPlanned, items: 4, perChunk: 4, resolved: 4, batchCalls: 1},
		{name: "a floor of schema constrained, another route answers the only chunk", native: true, floor: types.ContractSchemaConstrained, route: only(0), items: 4, perChunk: 4, resolved: 0, batchCalls: 1, refused: true},
		{name: "the highest floor, another route answers the only chunk", native: true, floor: types.ContractSchemaAndInvariantChecked, route: only(0), items: 4, perChunk: 4, resolved: 0, batchCalls: 1, refused: true},
		{name: "the second of two chunks is answered by another route", native: true, floor: types.ContractSchemaConstrained, route: only(1), items: 4, perChunk: 2, resolved: 2, batchCalls: 2, refused: true},
		{name: "the first of two chunks is answered by another route", native: true, floor: types.ContractSchemaConstrained, route: only(0), items: 4, perChunk: 2, resolved: 0, batchCalls: 1, refused: true},
		{name: "the middle of three chunks is answered by another route", native: true, floor: types.ContractSchemaConstrained, route: only(1), items: 6, perChunk: 2, resolved: 2, batchCalls: 2, refused: true},
		{name: "the isolate pass is answered by another route", native: true, floor: types.ContractSchemaConstrained, route: only(1), items: 4, perChunk: 4, omitFirst: true, resolved: 3, batchCalls: 2, refused: true},
		{name: "a route nothing is known of cannot reach the floor, so nothing is sent", native: false, floor: types.ContractSchemaConstrained, route: allPlanned, items: 4, perChunk: 4, resolved: 0, batchCalls: 0, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			if tc.native {
				llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: planned, Model: "fake-model",
					Supports: map[llm.Capability]bool{llm.CapNativeJSONSchema: true}})
			}
			provider := newRecoverFakeProvider("fake-model")
			provider.answeredBy = tc.route
			if tc.omitFirst {
				provider.batchScript = []recoverBatchHandler{omitBatch("i-000003"), fullSuccessBatch}
			}
			ctx := WithContractPolicy(recoverCtx(t, provider), types.DataPolicy{MinimumContract: tc.floor})
			opt := types.OpOptions{Model: "fake-model", JSONSchema: map[string]any{"type": "object"}}

			result, err := RunOpManyRecover(ctx, recoverSchemaOp(), recoverWidgets(tc.items), recoverCodec(), opt,
				RecoverConfig{MaxItemsPerCall: tc.perChunk, StartItemsPerCall: tc.perChunk})
			if tc.batchCalls == 0 {
				if types.KindOf(err) != types.KindPolicyViolation {
					t.Fatalf("err = %v, want the floor refused before anything is sent", err)
				}
				requireNoBatchResult(t, result)
			} else if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			batchCalls, atomicCalls := provider.counts()
			if batchCalls != tc.batchCalls || atomicCalls != 0 {
				t.Errorf("batchCalls=%d atomicCalls=%d, want %d and no atomic call", batchCalls, atomicCalls, tc.batchCalls)
			}
			if tc.batchCalls == 0 {
				return
			}
			if result.Summary.Succeeded != tc.resolved || result.Summary.Failed != tc.items-tc.resolved {
				t.Fatalf("summary %+v, want %d resolved", result.Summary, tc.resolved)
			}
			for i, item := range result.Items {
				if item.Status == types.ItemSucceeded {
					continue
				}
				if !tc.refused || types.KindOf(item.Err) != types.KindPolicyViolation {
					t.Errorf("item %d failed with %v, want a policy violation", i, item.Err)
				}
				if item.Value != (recoverSummary{}) {
					t.Errorf("item %d holds a value beside its failure: %+v", i, item.Value)
				}
			}
		})
	}
}

// ErrBusy and an unsupported capability are read for what they are. A scheduler
// that refuses to queue a request says nothing about the chunk's size, so the size
// is left alone; a route that cannot take the request repeats identically, so the
// cascade stops and every unanswered item carries the refusal. Both were read as
// a malformed protocol, which halves the chunk and asks the same route again.
func TestRunOpManyRecoverReadsAnAdmissionRefusalAndAnUnsupportedCapability(t *testing.T) {
	t.Run("a refusal to queue leaves the chunk size alone", func(t *testing.T) {
		items := recoverWidgets(12)
		provider := newRecoverFakeProvider("fake-model")
		busy := func([]taggedItem[recoverWidget]) (string, error) {
			return "", &types.OperationError{Kind: types.KindAdmissionRejected, Op: "test", Message: "queue full"}
		}
		provider.batchScript = []recoverBatchHandler{busy}
		result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
			RecoverConfig{MaxItemsPerCall: 6, StartItemsPerCall: 6})
		if err != nil || !result.Complete() {
			t.Fatalf("RunOpManyRecover: %v, %+v", err, result.Summary)
		}
		if batchCalls, _ := provider.counts(); batchCalls != 2 {
			t.Errorf("batchCalls = %d, want 2: the chunk size changed after a refusal to queue", batchCalls)
		}
		if !strings.Contains(result.Ledger.Explain(), "says nothing about the chunk size") {
			t.Errorf("the ledger does not say the size was left alone: %s", result.Ledger.Explain())
		}
	})

	for _, count := range []int{2, 3, 8} {
		t.Run(fmt.Sprintf("an unsupported capability stops the cascade over %d items", count), func(t *testing.T) {
			provider := newRecoverFakeProvider("fake-model")
			provider.batchDefault = func([]taggedItem[recoverWidget]) (string, error) {
				return "", &types.OperationError{Kind: types.KindUnsupportedCapability, Op: "test", Message: "no such feature"}
			}
			result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), recoverWidgets(count), recoverCodec(), types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 3, StartItemsPerCall: 3})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			if batchCalls, atomicCalls := provider.counts(); batchCalls != 1 || atomicCalls != 0 {
				t.Errorf("batchCalls=%d atomicCalls=%d, want 1 and 0: the same refusal was asked for again", batchCalls, atomicCalls)
			}
			for i, item := range result.Items {
				if item.Status != types.ItemFailed || types.KindOf(item.Err) != types.KindUnsupportedCapability {
					t.Errorf("item %d = %v, %v; want the unsupported capability", i, item.Status, item.Err)
				}
			}
		})
	}
}

// cascadeStops says whether a failed request would fail the same way if it were
// made again. The caller's own context ending does; one request's own deadline
// does not, which is what outcomeForCallErr calls a timeout: no signal about the
// size, with the cascade going on.
func TestCascadeStopsReadsTheKindAndTheCallersContext(t *testing.T) {
	live := context.Background()
	over, cancel := context.WithCancel(context.Background())
	cancel()
	timedOut := &types.OperationError{Kind: types.KindTimeout, Op: "test", Message: "one request's own deadline", Cause: context.DeadlineExceeded}
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"no error", live, nil, false},
		{"a refusal by policy", live, types.Newf(types.KindPolicyViolation, "t", "x"), true},
		{"a spent budget", live, types.Newf(types.KindBudgetExceeded, "t", "x"), true},
		{"a key the provider refuses", live, types.Newf(types.KindAuthentication, "t", "x"), true},
		{"a configuration that cannot run", live, types.Newf(types.KindConfiguration, "t", "x"), true},
		{"a cancellation", live, types.Newf(types.KindCanceled, "t", "x"), true},
		{"a scheduler that is closed", live, types.Newf(types.KindShutdown, "t", "x"), true},
		{"a route that cannot take the request", live, types.Newf(types.KindUnsupportedCapability, "t", "x"), true},
		{"a rate limit", live, types.Newf(types.KindRateLimited, "t", "x"), false},
		{"an outage", live, types.Newf(types.KindProviderUnavailable, "t", "x"), false},
		{"a refusal to queue", live, types.Newf(types.KindAdmissionRejected, "t", "x"), false},
		{"a circuit that is open", live, types.Newf(types.KindCircuitOpen, "t", "x"), false},
		{"a malformed body", live, types.Newf(types.KindMalformedOutput, "t", "x"), false},
		{"a request's own deadline", live, timedOut, false},
		{"a request's own deadline, wrapped", live, fmt.Errorf("batch: %w", timedOut), false},
		{"an error of no kind", live, errors.New("plain"), false},
		{"the caller's context over, whatever the error", over, types.Newf(types.KindMalformedOutput, "t", "x"), true},
		{"the caller's context over and a request's own deadline", over, timedOut, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cascadeStops(tc.ctx, tc.err); got != tc.want {
				t.Errorf("cascadeStops = %v, want %v", got, tc.want)
			}
		})
	}
}

// One request's own timeout is no reason to give up on the items it carried, or on
// chunks that were never sent: the cascade goes on, and the timeout does not
// shrink the chunk. The caller's own context ending stops everything.
func TestRunOpManyRecoverGoesOnAfterARequestsOwnTimeoutAndStopsWhenTheCallerEnds(t *testing.T) {
	t.Run("a request's own timeout", func(t *testing.T) {
		provider := newRecoverFakeProvider("fake-model")
		provider.batchHook = func(_ context.Context, call int) error {
			if call < 1 {
				return &types.OperationError{Kind: types.KindTimeout, Op: "test", Message: "one request's own deadline", Cause: context.DeadlineExceeded}
			}
			return nil
		}
		result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), recoverWidgets(12), recoverCodec(), types.OpOptions{},
			RecoverConfig{MaxItemsPerCall: 6, StartItemsPerCall: 6})
		if err != nil || !result.Complete() {
			t.Fatalf("RunOpManyRecover: %v, %+v", err, result.Summary)
		}
		if batchCalls, atomicCalls := provider.counts(); batchCalls != 2 || atomicCalls != 6 {
			t.Errorf("batchCalls=%d atomicCalls=%d, want 2 and 6: the second chunk was never sent, or the size changed", batchCalls, atomicCalls)
		}
	})

	t.Run("the caller's context is cancelled during the first request", func(t *testing.T) {
		provider := newRecoverFakeProvider("fake-model")
		ctx, cancel := context.WithCancel(recoverCtx(t, provider))
		t.Cleanup(cancel)
		provider.batchHook = func(ctx context.Context, _ int) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		result, err := RunOpManyRecover(ctx, recoverWidgetOp(), recoverWidgets(12), recoverCodec(), types.OpOptions{},
			RecoverConfig{MaxItemsPerCall: 6, StartItemsPerCall: 6})
		if err != nil {
			t.Fatalf("RunOpManyRecover: %v", err)
		}
		if batchCalls, atomicCalls := provider.counts(); batchCalls != 1 || atomicCalls != 0 {
			t.Errorf("batchCalls=%d atomicCalls=%d, want 1 and 0", batchCalls, atomicCalls)
		}
		for i, item := range result.Items {
			if item.Status != types.ItemCancelled || types.KindOf(item.Err) != types.KindCanceled || !errors.Is(item.Err, context.Canceled) {
				t.Errorf("item %d = %v, %v; want a cancelled item whose error matches context.Canceled", i, item.Status, item.Err)
			}
		}
	})

	t.Run("the caller's deadline passes during the first request", func(t *testing.T) {
		provider := newRecoverFakeProvider("fake-model")
		ctx, cancel := context.WithTimeout(recoverCtx(t, provider), 100*time.Millisecond)
		t.Cleanup(cancel)
		provider.batchHook = func(ctx context.Context, _ int) error {
			<-ctx.Done()
			return ctx.Err()
		}
		result, err := RunOpManyRecover(ctx, recoverWidgetOp(), recoverWidgets(8), recoverCodec(), types.OpOptions{},
			RecoverConfig{MaxItemsPerCall: 4, StartItemsPerCall: 4})
		if err != nil {
			t.Fatalf("RunOpManyRecover: %v", err)
		}
		if batchCalls, atomicCalls := provider.counts(); batchCalls != 1 || atomicCalls != 0 {
			t.Errorf("batchCalls=%d atomicCalls=%d, want 1 and 0", batchCalls, atomicCalls)
		}
		for i, item := range result.Items {
			if item.Status != types.ItemCancelled || types.KindOf(item.Err) != types.KindTimeout || !errors.Is(item.Err, context.DeadlineExceeded) {
				t.Errorf("item %d = %v, %v; want a cancelled item whose error is a timeout that matches context.DeadlineExceeded", i, item.Status, item.Err)
			}
		}
	})
}

// The client's deadline binds each request the cascade sends: a batched request
// whose context carries none would wait on a hung provider for ever. Each request
// has a deadline of its own, so the wait of one does not use up the next one's.
func TestRunOpManyRecoverBindsTheClientsDeadlineToEveryRequest(t *testing.T) {
	const timeout = 400 * time.Millisecond
	cases := []struct {
		name        string
		timeout     time.Duration
		hangFirst   int // batched requests that hang until their deadline
		wantBounded bool
		batchCalls  int
	}{
		{"a client with a timeout, two batched requests hang", timeout, 2, true, 2},
		{"a client with a timeout, one batched request hangs", timeout, 1, true, 2},
		{"a client with a timeout, nothing hangs", timeout, 0, true, 2},
		{"a client with no timeout", 0, 0, false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newRecoverFakeProvider("fake-model")
			provider.batchHook = func(ctx context.Context, call int) error {
				if call < tc.hangFirst {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Timeout: tc.timeout})
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")

			started := time.Now()
			result, err := RunOpManyRecover(ctx, recoverWidgetOp(), recoverWidgets(4), recoverCodec(), types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 2, StartItemsPerCall: 2})
			if err != nil || !result.Complete() {
				t.Fatalf("RunOpManyRecover: %v, %+v", err, result.Summary)
			}
			if elapsed := time.Since(started); elapsed > 20*time.Second {
				t.Errorf("the run took %v", elapsed)
			}
			if batchCalls, _ := provider.counts(); batchCalls != tc.batchCalls {
				t.Errorf("batchCalls = %d, want %d", batchCalls, tc.batchCalls)
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			for kind, seen := range map[string][]requestDeadline{"batched": provider.batchDeadlines, "atomic": provider.atomicDeadlines} {
				for i, request := range seen {
					if request.bounded != tc.wantBounded {
						t.Errorf("%s request %d: bounded = %v, want %v", kind, i, request.bounded, tc.wantBounded)
					}
					if tc.wantBounded && (request.left > timeout || request.left < timeout/10) {
						t.Errorf("%s request %d had %v left on arrival, want most of the %v: the deadline is not its own", kind, i, request.left, timeout)
					}
				}
			}
		})
	}
}

// One call of the operation is one unit to an observer, with every request the
// cascade makes inside it: the batched passes and the atomic rung alike.
func TestRunOpManyRecoverIsOneOperationToAnObserver(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*recoverFakeProvider)
	}{
		{"every pass compliant", func(*recoverFakeProvider) {}},
		{"an omission recovered by an isolate pass", func(p *recoverFakeProvider) {
			p.batchScript = []recoverBatchHandler{omitBatch("i-000002"), fullSuccessBatch}
		}},
		{"an omission that falls to the atomic rung", func(p *recoverFakeProvider) { p.batchDefault = omitBatch("i-000002") }},
		{"a malformed body in every pass", func(p *recoverFakeProvider) { p.batchDefault = malformedBatch }},
		{"a stop", func(p *recoverFakeProvider) {
			p.batchDefault = func([]taggedItem[recoverWidget]) (string, error) {
				return "", &types.OperationError{Kind: types.KindAuthentication, Op: "test", Message: "scripted"}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := installCollector(t)
			provider := newRecoverFakeProvider("fake-model")
			tc.setup(provider)
			_, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), recoverWidgets(4), recoverCodec(), types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 2, StartItemsPerCall: 2})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			started, finished := collector.snapshot()
			operations := scopes(started, telemetry.ScopeOperation, eventScope)
			if len(operations) != 1 || operations[0].Operation != "recoverWidget" {
				t.Fatalf("%d operation units started, want one named for the operation that covers the whole run", len(operations))
			}
			if len(scopes(finished, telemetry.ScopeOperation, resultScope)) != 1 {
				t.Errorf("the operation unit did not end exactly once")
			}
			if started[0].Scope != telemetry.ScopeOperation {
				t.Error("a request started before the operation did")
			}
			if len(scopes(started, telemetry.ScopeRequest, eventScope)) < 1 {
				t.Error("the requests were not seen inside the operation")
			}
		})
	}
}

// The caller's own code runs inside the cascade: the batch Encode, the contract's
// invariants and Normalize, and the codec's DecodeItem. A panic in any of them is
// the failure of the chunk or the item it ran for, as a panic in the atomic rung
// is, and never the end of the process. The panic's value is not in the error.
func TestRunOpManyRecoverSurvivesCallerCodeThatPanics(t *testing.T) {
	const secret = "a customer's name in a panic"
	cases := []struct {
		name       string
		set        func(*Op[recoverWidget, recoverSummary], *BatchItemCodec[recoverWidget, recoverSummary])
		failed     []int // items that end failed
		fallback   []int // items that end resolved by a request of their own
		batchCalls int   // 0 means not checked
	}{
		{"Encode panics", func(op *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			op.Batch.Encode = func([]recoverWidget, types.OpOptions) (string, string, error) { panic(secret) }
		}, nil, []int{0, 1, 2, 3, 4}, 0},
		{"DecodeItem panics for one item", func(_ *Op[recoverWidget, recoverSummary], codec *BatchItemCodec[recoverWidget, recoverSummary]) {
			inner := codec.DecodeItem
			codec.DecodeItem = func(raw json.RawMessage) (recoverSummary, error) {
				if strings.Contains(string(raw), `"length":3`) {
					panic(secret)
				}
				return inner(raw)
			}
		}, nil, []int{2}, 2},
		{"an invariant panics for one item", func(op *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			op.Contract.Invariants = []func(recoverSummary) error{func(s recoverSummary) error {
				if s.Length == 4 {
					panic(secret)
				}
				return nil
			}}
		}, []int{3}, nil, 2},
		{"Normalize panics for one item", func(op *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			op.Contract.Normalize = func(s recoverSummary) recoverSummary {
				if s.Length == 5 {
					panic(secret)
				}
				return s
			}
		}, []int{4}, nil, 2},
		{"DecodeItem panics for every item", func(_ *Op[recoverWidget, recoverSummary], codec *BatchItemCodec[recoverWidget, recoverSummary]) {
			codec.DecodeItem = func(json.RawMessage) (recoverSummary, error) { panic(secret) }
		}, nil, []int{0, 1, 2, 3, 4}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("a panic escaped the cascade: %v", r)
				}
			}()
			provider := newRecoverFakeProvider("fake-model")
			op, codec := recoverWidgetOp(), recoverCodec()
			tc.set(&op, &codec)
			items := recoverWidgets(5)

			result, err := RunOpManyRecover(recoverCtx(t, provider), op, items, codec, types.OpOptions{},
				RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			failed, fallback := map[int]bool{}, map[int]bool{}
			for _, i := range tc.failed {
				failed[i] = true
			}
			for _, i := range tc.fallback {
				fallback[i] = true
			}
			for i, item := range result.Items {
				switch {
				case failed[i]:
					if item.Status != types.ItemFailed || types.KindOf(item.Err) != types.KindInvariantViolation {
						t.Errorf("item %d = %v, %v; want a failure of kind invariant violation", i, item.Status, item.Err)
					}
					if item.Value != (recoverSummary{}) {
						t.Errorf("item %d holds a value beside its failure", i)
					}
				case item.Status != types.ItemSucceeded || item.Value.Length != len(items[i].Name):
					t.Errorf("item %d = %+v, want it resolved with its own length", i, item)
				case fallback[i] != (item.Mode == types.ModeAtomicFallback):
					t.Errorf("item %d: mode %q, want a request of its own = %v", i, item.Mode, fallback[i])
				}
				if item.Err != nil && strings.Contains(item.Err.Error(), "customer") {
					t.Errorf("item %d: the panic's value is in the error: %v", i, item.Err)
				}
			}
			if batchCalls, _ := provider.counts(); tc.batchCalls != 0 && batchCalls != tc.batchCalls {
				t.Errorf("batchCalls = %d, want %d", batchCalls, tc.batchCalls)
			}
		})
	}
}

// BatchResult.Total is what every request of the run cost, summed from the
// envelopes of the batched passes and of the atomic calls alike, and it agrees
// with the provider's own count. Nothing of the kind was reported: tokens, timing
// and attempts were lost, and only an even share of each pass was kept per item.
func TestRunOpManyRecoverTotalIsWhatEveryRequestCost(t *testing.T) {
	cases := []struct {
		name   string
		items  int
		config RecoverConfig
		setup  func(*recoverFakeProvider, *Op[recoverWidget, recoverSummary])
	}{
		{"one compliant chunk", 5, RecoverConfig{MaxItemsPerCall: 5}, func(*recoverFakeProvider, *Op[recoverWidget, recoverSummary]) {}},
		{"two compliant chunks", 8, RecoverConfig{MaxItemsPerCall: 4}, func(*recoverFakeProvider, *Op[recoverWidget, recoverSummary]) {}},
		{"an omission recovered by an isolate pass", 5, RecoverConfig{MaxItemsPerCall: 5}, func(p *recoverFakeProvider, _ *Op[recoverWidget, recoverSummary]) {
			p.batchScript = []recoverBatchHandler{omitBatch("i-000003"), fullSuccessBatch}
		}},
		{"an omission that falls to the atomic rung", 5, RecoverConfig{MaxItemsPerCall: 5}, func(p *recoverFakeProvider, _ *Op[recoverWidget, recoverSummary]) {
			p.batchDefault = omitBatch("i-000003")
		}},
		{"a malformed body in every pass", 4, RecoverConfig{MaxItemsPerCall: 2}, func(p *recoverFakeProvider, _ *Op[recoverWidget, recoverSummary]) {
			p.batchDefault = malformedBatch
		}},
		{"every item sent alone", 6, RecoverConfig{MaxItemsPerCall: 6, Concurrency: 3}, func(_ *recoverFakeProvider, op *Op[recoverWidget, recoverSummary]) {
			op.Batch.Encode = func([]recoverWidget, types.OpOptions) (string, string, error) {
				return "", "", errors.New("cannot encode")
			}
		}},
		{"an atomic request that fails", 4, RecoverConfig{MaxItemsPerCall: 4}, func(p *recoverFakeProvider, _ *Op[recoverWidget, recoverSummary]) {
			p.batchDefault = omitBatch("i-000002")
			p.atomicFail = func(name string) bool { return len(name) == 2 }
		}},
		{"a cascade that stops", 6, RecoverConfig{MaxItemsPerCall: 3}, func(p *recoverFakeProvider, _ *Op[recoverWidget, recoverSummary]) {
			p.batchDefault = func([]taggedItem[recoverWidget]) (string, error) {
				return "", &types.OperationError{Kind: types.KindPolicyViolation, Op: "test", Message: "refused"}
			}
		}},
		{"a single item", 1, RecoverConfig{}, func(*recoverFakeProvider, *Op[recoverWidget, recoverSummary]) {}},
		{"an invariant that rejects one batched answer", 3, RecoverConfig{MaxItemsPerCall: 3}, func(_ *recoverFakeProvider, op *Op[recoverWidget, recoverSummary]) {
			op.Contract.Invariants = []func(recoverSummary) error{func(s recoverSummary) error {
				if s.Length == 2 {
					return errors.New("no")
				}
				return nil
			}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configtest.Setenv(t, "SCHEMAFLUX_MODEL", "gpt-4")
			provider := newRecoverFakeProvider("gpt-4")
			provider.priced = true
			op := recoverWidgetOp()
			tc.setup(provider, &op)
			ctx := WithProvider(context.Background(), provider)

			began := time.Now()
			result, err := RunOpManyRecover(ctx, op, recoverWidgets(tc.items), recoverCodec(), types.OpOptions{}, tc.config)
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			batchCalls, atomicCalls := provider.counts()
			asked := batchCalls + atomicCalls

			// The provider answers a request that does not fail with 150 tokens.
			provider.mu.Lock()
			completed := provider.completed
			provider.mu.Unlock()
			total := result.Total
			if total.Operation != op.ID.String() || total.Strategy != types.ShapeMDSP.String() {
				t.Errorf("operation %q, strategy %q", total.Operation, total.Strategy)
			}
			if total.Attempts != asked {
				t.Errorf("Total.Attempts = %d, the provider was asked %d times", total.Attempts, asked)
			}
			if want := 150 * completed; total.Usage.TotalTokens != want {
				t.Errorf("Total.Usage.TotalTokens = %d, want %d", total.Usage.TotalTokens, want)
			}
			if total.Elapsed < 0 || total.Elapsed > time.Since(began) {
				t.Errorf("Total.Elapsed = %v, want a time within the call's own", total.Elapsed)
			}
			if total.RequestedContract != types.ContractJSONWellFormed {
				t.Errorf("RequestedContract = %s", total.RequestedContract)
			}
			if result.Summary.Succeeded > 0 && total.DeliveredContract != types.ContractJSONWellFormed {
				t.Errorf("DeliveredContract = %s, want the level the items that succeeded were delivered at", total.DeliveredContract)
			}
			if completed > 0 && (!total.Cost.Priced || total.Cost.TotalCost <= 0) {
				t.Fatalf("Total.Cost = %+v, want a priced total", total.Cost)
			}
			if completed == 0 && total.Cost.Priced {
				t.Fatalf("Total.Cost = %+v: nothing was answered, so there is nothing priced", total.Cost)
			}
			// The items' costs are an attribution of the same money: they add up to
			// what the passes cost, plus the atomic calls, exactly. A share that was
			// lost would leave the sum short, and one counted twice would exceed it.
			var attributed float64
			for _, item := range result.Items {
				attributed += item.Cost.TotalCost
			}
			if diff := attributed - total.Cost.TotalCost; diff > total.Cost.TotalCost*1e-9 || -diff > total.Cost.TotalCost*1e-9 {
				t.Errorf("the items' costs add up to %v, and the run cost %v", attributed, total.Cost.TotalCost)
			}
		})
	}
}

// A run that sent nothing has nothing to report.
func TestRunOpManyRecoverTotalOfARunThatSentNothingIsEmpty(t *testing.T) {
	result, err := RunOpManyRecover(context.Background(), recoverWidgetOp(), nil, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if result.Total.Attempts != 0 || result.Total.Usage.TotalTokens != 0 || result.Total.Cost.Priced {
		t.Errorf("Total = %+v", result.Total)
	}
}

// ModeMDSP is a constant, so no caller can reassign what every item reports, and
// it is the word the shape itself goes by in a plan and a result's Strategy.
func TestModeMDSPIsAConstantThatMatchesTheShape(t *testing.T) {
	const same = ModeMDSP // a constant expression: this does not compile for a variable
	if same != types.ShapeMDSP.String() {
		t.Errorf("ModeMDSP = %q, ShapeMDSP.String() = %q", ModeMDSP, types.ShapeMDSP.String())
	}
	provider := newRecoverFakeProvider("fake-model")
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), recoverWidgets(3), recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil || !result.Complete() {
		t.Fatalf("RunOpManyRecover: %v, %+v", err, result.Summary)
	}
	for i, item := range result.Items {
		if item.Mode != types.ShapeMDSP.String() {
			t.Errorf("item %d: Mode = %q", i, item.Mode)
		}
	}
	// What the metrics compare a batched item's mode against is the same word: an
	// item resolved by a second batched pass is an omission only if it matches.
	provider = newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{omitBatch("i-000002"), fullSuccessBatch}
	result, err = RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), recoverWidgets(3), recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil || result.Metrics().Omissions != 1 {
		t.Errorf("omissions = %d (%v), want the one item that needed an isolate pass", result.Metrics().Omissions, err)
	}
}

type mergeNumber struct {
	N int `json:"n"`
}

func mergeNumberCodec() BatchItemCodec[string, mergeNumber] {
	return BatchItemCodec[string, mergeNumber]{DecodeItem: func(raw json.RawMessage) (mergeNumber, error) {
		var n mergeNumber
		return n, json.Unmarshal(raw, &n)
	}}
}

// mergeIDBatchPartial keeps what decoded and says why each of the others did not,
// with a cause that is classified and names the item's id and nothing of the
// answer.
func TestMergeIDBatchPartialKeepsWhatDecodedAndSaysWhyTheOthersDidNot(t *testing.T) {
	items := []string{"a", "b", "c"}
	answer := func(id, output string) string { return fmt.Sprintf(`{"id":%q,"output":%s}`, id, output) }
	body := func(elements ...string) string { return `{"items":[` + strings.Join(elements, ",") + `]}` }
	panicking := mergeNumberCodec()
	panicking.DecodeItem = func(raw json.RawMessage) (mergeNumber, error) {
		if strings.Contains(string(raw), "2") {
			return mergeNumber{}, stepPanicked("batch")
		}
		return mergeNumber{N: 1}, nil
	}

	cases := []struct {
		name       string
		body       string
		codec      BatchItemCodec[string, mergeNumber]
		resolved   []int
		unresolved []int
		kinds      map[int]types.ErrorKind
		wantErr    types.ErrorKind
	}{
		{"every id answered once", body(answer("i-000001", `{"n":1}`), answer("i-000002", `{"n":2}`), answer("i-000003", `{"n":3}`)), mergeNumberCodec(), []int{0, 1, 2}, nil, nil, types.KindUnknown},
		{"the answers out of order", body(answer("i-000003", `{"n":3}`), answer("i-000001", `{"n":1}`), answer("i-000002", `{"n":2}`)), mergeNumberCodec(), []int{0, 1, 2}, nil, nil, types.KindUnknown},
		{"one id never answered", body(answer("i-000001", `{"n":1}`), answer("i-000003", `{"n":3}`)), mergeNumberCodec(), []int{0, 2}, []int{1}, map[int]types.ErrorKind{1: types.KindBatchProtocolViolation}, types.KindUnknown},
		{"one id answered twice", body(answer("i-000001", `{"n":1}`), answer("i-000002", `{"n":2}`), answer("i-000002", `{"n":9}`), answer("i-000003", `{"n":3}`)), mergeNumberCodec(), []int{0, 2}, []int{1}, map[int]types.ErrorKind{1: types.KindBatchProtocolViolation}, types.KindUnknown},
		{"an id nobody offered", body(answer("i-000001", `{"n":1}`), answer("i-000002", `{"n":2}`), answer("i-000003", `{"n":3}`), answer("i-000009", `{"n":9}`)), mergeNumberCodec(), []int{0, 1, 2}, nil, nil, types.KindUnknown},
		{"an answer of null", body(answer("i-000001", `{"n":1}`), answer("i-000002", `null`), answer("i-000003", `{"n":3}`)), mergeNumberCodec(), []int{0, 2}, []int{1}, map[int]types.ErrorKind{1: types.KindBatchProtocolViolation}, types.KindUnknown},
		{"an answer of the wrong type", body(answer("i-000001", `{"n":1}`), answer("i-000002", `{"n":"two"}`), answer("i-000003", `{"n":3}`)), mergeNumberCodec(), []int{0, 2}, []int{1}, map[int]types.ErrorKind{1: types.KindSchemaViolation}, types.KindUnknown},
		{"an answer that is not JSON at all", body(answer("i-000001", `{"n":1}`), answer("i-000002", `"x"`), answer("i-000003", `{"n":3}`)), mergeNumberCodec(), []int{0, 2}, []int{1}, map[int]types.ErrorKind{1: types.KindSchemaViolation}, types.KindUnknown},
		{"a decoder that panicked for one item", body(answer("i-000001", `{"n":1}`), answer("i-000002", `{"n":2}`), answer("i-000003", `{"n":3}`)), panicking, []int{0, 2}, []int{1}, map[int]types.ErrorKind{1: types.KindInvariantViolation}, types.KindUnknown},
		{"a body that is not JSON", `not json`, mergeNumberCodec(), nil, []int{0, 1, 2}, nil, types.KindMalformedOutput},
		{"a body with no items", `{"items":[]}`, mergeNumberCodec(), nil, []int{0, 1, 2}, map[int]types.ErrorKind{0: types.KindBatchProtocolViolation, 1: types.KindBatchProtocolViolation, 2: types.KindBatchProtocolViolation}, types.KindUnknown},
		{"a codec with no DecodeItem", body(answer("i-000001", `{"n":1}`)), BatchItemCodec[string, mergeNumber]{}, nil, []int{0, 1, 2}, nil, types.KindConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("a panic escaped: %v", r)
				}
			}()
			merged := mergeIDBatchPartial(tc.body, itemIDs(len(items)), items, tc.codec)
			resolved, unresolved, causes, err := merged.resolved, merged.unresolved, merged.causes, merged.err
			if got := types.KindOf(err); got != tc.wantErr {
				t.Errorf("err kind = %v (%v), want %v", got, err, tc.wantErr)
			}
			if len(resolved) != len(tc.resolved) {
				t.Errorf("resolved %v, want the items %v", resolved, tc.resolved)
			}
			for _, i := range tc.resolved {
				if _, ok := resolved[i]; !ok {
					t.Errorf("item %d was not resolved", i)
				}
			}
			if fmt.Sprint(unresolved) != fmt.Sprint(tc.unresolved) {
				t.Errorf("unresolved = %v, want %v", unresolved, tc.unresolved)
			}
			for i, kind := range tc.kinds {
				if got := types.KindOf(causes[i]); got != kind {
					t.Errorf("item %d cause kind = %v (%v), want %v", i, got, causes[i], kind)
				}
				if causes[i] != nil && (strings.Contains(causes[i].Error(), "two") || strings.Contains(causes[i].Error(), "panicked: ")) {
					t.Errorf("item %d cause carries the answer: %v", i, causes[i])
				}
			}
		})
	}
}

// itemDecodeCause keeps the kind the codec gave its failure, and a failure nobody
// classified is a schema violation. A decoder that panicked is the caller's code
// failing on the item and is reported as that. The text names the field and the
// type a mismatch was found at and never the value.
func TestItemDecodeCauseKeepsTheKindAndNamesNoValue(t *testing.T) {
	var mismatch error
	var target struct {
		Count int `json:"count"`
		Inner struct {
			Size int8 `json:"size"`
		} `json:"inner"`
	}
	mismatch = json.Unmarshal([]byte(`{"count":"seventeen"}`), &target)
	var nested error = json.Unmarshal([]byte(`{"inner":{"size":99999}}`), &target)
	panicked := stepPanicked("batch")

	cases := []struct {
		name         string
		err          error
		kind         types.ErrorKind
		mustContain  []string
		mustNotShow  []string
		wantNarrowed bool
	}{
		{"an error nobody classified", errors.New("it broke on 4417 Elm Street"), types.KindSchemaViolation, []string{"item 7", "does not decode"}, []string{"Elm Street", "4417"}, false},
		{"a malformed answer", types.Newf(types.KindMalformedOutput, "x", "bad"), types.KindMalformedOutput, []string{"item 7"}, nil, false},
		{"an invariant the codec checks", types.Newf(types.KindInvariantViolation, "x", "bad"), types.KindInvariantViolation, nil, nil, false},
		{"a type mismatch names the field", mismatch, types.KindSchemaViolation, []string{"/count", "int"}, []string{"seventeen"}, false},
		{"a nested field is a pointer", nested, types.KindSchemaViolation, []string{"/inner/size", "int8"}, []string{"99999"}, false},
		{"a mismatch inside a wrapper", fmt.Errorf("item: %w", mismatch), types.KindSchemaViolation, []string{"/count"}, []string{"seventeen"}, false},
		{"a classified error wrapping a mismatch keeps its kind", types.Wrap(types.KindEvidenceViolation, "x", mismatch), types.KindEvidenceViolation, nil, []string{"seventeen"}, false},
		{"a decoder that panicked", panicked, types.KindInvariantViolation, []string{"decoder panicked"}, []string{"a step panicked"}, true},
		{"a wrapped panic", fmt.Errorf("decode: %w", panicked), types.KindInvariantViolation, []string{"decoder panicked"}, nil, true},
		{"a context error", context.DeadlineExceeded, types.KindSchemaViolation, []string{"item 7"}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cause := itemDecodeCause(6, tc.err)
			if types.KindOf(cause) != tc.kind {
				t.Errorf("kind = %v, want %v (%v)", types.KindOf(cause), tc.kind, cause)
			}
			for _, want := range tc.mustContain {
				if !strings.Contains(cause.Error(), want) {
					t.Errorf("%q does not contain %q", cause.Error(), want)
				}
			}
			for _, banned := range tc.mustNotShow {
				if strings.Contains(cause.Error(), banned) {
					t.Errorf("%q contains %q", cause.Error(), banned)
				}
			}
			if narrowed := types.NarrowingOf(cause) == types.NarrowPanic; narrowed != tc.wantNarrowed {
				t.Errorf("marked as a panic = %v, want %v", narrowed, tc.wantNarrowed)
			}
		})
	}
}

// --- An answer belongs to the item whose id it carries, and to nobody else.

// recoverRequestLog records the ids every request of a run offered and the length of
// the item each was offered for, so that a scripted model can answer with an id from
// another request, which no later request offered.
type recoverRequestLog struct {
	mu      sync.Mutex
	offered map[string]int
	order   []string
}

func newRecoverRequestLog() *recoverRequestLog {
	return &recoverRequestLog{offered: map[string]int{}}
}

// note records one request's ids and returns the ids of the requests before it.
func (l *recoverRequestLog) note(tagged []taggedItem[recoverWidget]) (earlier []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	earlier = append(earlier, l.order...)
	for _, t := range tagged {
		l.offered[t.ID] = len(t.Item.Name)
		l.order = append(l.order, t.ID)
	}
	return earlier
}

// elementsText writes a list of elements as the body of a response.
func elementsText(elements []string) string {
	return `{"items":[` + strings.Join(elements, ",") + `]}`
}

func rightElement(t taggedItem[recoverWidget]) string {
	return fmt.Sprintf(`{"id":%q,"output":{"length":%d}}`, t.ID, len(t.Item.Name))
}

// wrongElement is an element whose answer no item may ever hold: a length that is
// no item's.
func wrongElement(id string) string {
	return fmt.Sprintf(`{"id":%q,"output":{"length":-1}}`, id)
}

// assertNoWrongAnswers fails when an item of the result holds an answer that is not
// its own: every item here has a name of a different length, and a model answers with
// the length of the name it was shown, or with -1 when it is lying.
func assertNoWrongAnswers(t *testing.T, items []recoverWidget, result types.BatchResult[recoverSummary]) {
	t.Helper()
	if len(result.Items) != len(items) {
		t.Fatalf("%d results for %d items", len(result.Items), len(items))
	}
	for i, item := range result.Items {
		if item.Status == types.ItemSucceeded && item.Value.Length != len(items[i].Name) {
			t.Errorf("item %d holds the answer %d, which is not its own (%d): a wrong answer returned as a success", i, item.Value.Length, len(items[i].Name))
		}
	}
}

// A model that drops one answer and renumbers the rest from the first id, which is
// how an answer is shifted onto the wrong item when ids run in a sequence, moves
// nothing: the ids the library offered are random, so the renumbered ids were never
// offered, answer no item, and every item is answered by a request of its own.
func TestRunOpManyRecoverDoesNotShiftAnswersWhenAModelDropsOneAndRenumbersTheRest(t *testing.T) {
	items := recoverWidgets(8)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		var elements []string
		for place, t := range tagged {
			if place == 2 {
				continue
			}
			elements = append(elements, fmt.Sprintf(`{"id":%q,"output":{"length":%d}}`, fmt.Sprintf("i-%06d", len(elements)+1), len(t.Item.Name)))
		}
		return elementsText(elements), nil
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	assertNoWrongAnswers(t, items, result)
	if !result.Complete() {
		t.Errorf("Summary = %+v, want every item answered once the library asked it alone", result.Summary)
	}
	for i, item := range result.Items {
		if item.Mode == ModeMDSP {
			t.Errorf("item %d was taken from a batched answer whose ids nobody offered", i)
		}
	}
}

// A model that keeps every id and drops one element leaves exactly that item
// unresolved, and no other moves.
func TestRunOpManyRecoverLeavesExactlyTheDroppedItemUnresolved(t *testing.T) {
	items := recoverWidgets(8)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{omitBatch("i-000003")}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	assertNoWrongAnswers(t, items, result)
	for i, item := range result.Items {
		wantAttempts := 1
		if i == 2 {
			wantAttempts = 2
		}
		if item.Mode != ModeMDSP || item.Attempts != wantAttempts {
			t.Errorf("item %d: Mode = %q, Attempts = %d, want %q and %d: only the dropped item is asked again", i, item.Mode, item.Attempts, ModeMDSP, wantAttempts)
		}
	}
	if batch, atomic := provider.counts(); batch != 2 || atomic != 0 {
		t.Errorf("batch=%d atomic=%d, want the request and one more for the dropped item alone", batch, atomic)
	}
}

// Each request offers ids no other request did, and an element that carries the id of
// an earlier request answers nothing in a later one: the ids the first request offered
// are not the ids of the request that asks again.
func TestRunOpManyRecoverNeverReusesAnIDBetweenARequestAndItsReAsk(t *testing.T) {
	items := recoverWidgets(6)
	provider := newRecoverFakeProvider("fake-model")
	log := newRecoverRequestLog()
	var firstIDs []string
	var reused atomic.Bool
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		earlier := log.note(tagged)
		if len(earlier) == 0 {
			// The first request leaves out the last two items.
			for _, t := range tagged {
				firstIDs = append(firstIDs, t.ID)
			}
			var elements []string
			for _, t := range tagged[:len(tagged)-2] {
				elements = append(elements, rightElement(t))
			}
			return elementsText(elements), nil
		}
		// The request that asks again is answered with the ids of the first one.
		for _, t := range tagged {
			for _, id := range firstIDs {
				if t.ID == id {
					reused.Store(true)
				}
			}
		}
		var elements []string
		for _, id := range firstIDs {
			elements = append(elements, wrongElement(id))
		}
		return elementsText(elements), nil
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	assertNoWrongAnswers(t, items, result)
	if !result.Complete() {
		t.Errorf("Summary = %+v", result.Summary)
	}
	if reused.Load() {
		t.Error("an id offered in the first request was offered again in the request that asked again")
	}
}

// The ids are long enough not to collide and are not a sequence a model can renumber.
func TestBatchIDsAreRandomLongAndDistinct(t *testing.T) {
	seen := map[string]bool{}
	for round := 0; round < 200; round++ {
		ids := newBatchIDs(50)
		for position, id := range ids {
			if len(id) != batchIDLength || !strings.HasPrefix(id, batchIDPrefix) {
				t.Fatalf("id %q is not %d characters led by %q", id, batchIDLength, batchIDPrefix)
			}
			for _, character := range id[len(batchIDPrefix):] {
				if !strings.ContainsRune(batchIDAlphabet, character) {
					t.Fatalf("id %q holds %q, which is not in the alphabet", id, character)
				}
			}
			if seen[id] {
				t.Fatalf("id %q was made twice, in %d requests of 50 ids", id, round+1)
			}
			seen[id] = true
			if id == itemID(position) {
				t.Fatalf("id %q is the sequential id of position %d", id, position)
			}
		}
	}
	if strings.Join(newBatchIDs(5), ",") == strings.Join(newBatchIDs(5), ",") {
		t.Error("two requests offered the same ids")
	}
	if got := newBatchIDs(0); len(got) != 0 {
		t.Errorf("ids for no items = %v", got)
	}
}

// Whatever a hostile model does to a response -- drops elements, repeats them, puts
// them in another order, adds ids it was never given, answers with the ids of an
// earlier request or with a sequence of its own, cuts the body short, sends a refusal
// or a failure -- no item ends up holding an answer that is not its own, and the run
// sends no more requests than recoverRequestBound says. Many seeds, because the
// property is about every combination.
func TestRunOpManyRecoverPropertyNoItemHoldsAnotherItemsAnswerAndRequestsStayBounded(t *testing.T) {
	seeds := 250
	if testing.Short() {
		seeds = 50
	}
	type element struct{ id, text string }
	for seed := 0; seed < seeds; seed++ {
		rng := rand.New(rand.NewSource(int64(seed)))
		n := 1 + rng.Intn(40)
		isolate := 1 + rng.Intn(MaxIsolatePassesLimit)
		items := recoverWidgets(n)
		provider := newRecoverFakeProvider("fake-model")
		log := newRecoverRequestLog()
		var mu sync.Mutex
		finishes := map[int]string{}
		provider.batchFinish = func(call int) string {
			mu.Lock()
			defer mu.Unlock()
			if finish, ok := finishes[call]; ok {
				return finish
			}
			return "stop"
		}
		provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			call := len(finishes)
			earlier := log.note(tagged)
			finishes[call] = "stop"

			switch rng.Intn(10) {
			case 0:
				return "", &llm.RateLimitError{APIError: &llm.APIError{Provider: "recover-fake", StatusCode: 429}, RetryAfter: time.Millisecond}
			case 1:
				// The provider's filter withholds the answer: nothing is written.
				finishes[call] = "content_filter"
				return "", nil
			case 2:
				finishes[call] = "length"
				return elementsText(nil), nil
			case 3:
				return "this is not an answer", nil
			}
			elements := make([]element, 0, len(tagged)+4)
			for _, t := range tagged {
				elements = append(elements, element{t.ID, rightElement(t)})
			}
			// Drop some.
			if rng.Intn(2) == 0 {
				for drops := rng.Intn(1 + len(elements)/2); drops > 0 && len(elements) > 0; drops-- {
					at := rng.Intn(len(elements))
					elements = append(elements[:at], elements[at+1:]...)
				}
			}
			// Repeat some of the ids that are still answered, with an answer no item may
			// hold. (An id answered once, with a wrong answer, is a model that lies under
			// the right id, which nothing can detect.)
			if rng.Intn(3) == 0 {
				answered := map[string]bool{}
				for _, e := range elements {
					answered[e.id] = true
				}
				for _, t := range tagged {
					if answered[t.ID] && rng.Intn(4) == 0 {
						elements = append(elements, element{t.ID, wrongElement(t.ID)})
					}
				}
			}
			// Add ids the model was never given, in this request or any.
			if rng.Intn(2) == 0 {
				for k := rng.Intn(4); k > 0; k-- {
					var id string
					switch rng.Intn(3) {
					case 0:
						id = fmt.Sprintf("i-%06d", 1+rng.Intn(60))
					case 1:
						id = fmt.Sprintf("made-up-%d", rng.Intn(1000))
					default:
						if len(earlier) == 0 {
							continue
						}
						id = earlier[rng.Intn(len(earlier))]
					}
					elements = append(elements, element{id, wrongElement(id)})
				}
			}
			// Answer in another order.
			rng.Shuffle(len(elements), func(i, j int) { elements[i], elements[j] = elements[j], elements[i] })
			// Renumber from the first id, as a model that counts its own answers does.
			if rng.Intn(4) == 0 {
				for k := range elements {
					id := fmt.Sprintf("i-%06d", k+1)
					elements[k] = element{id, wrongElement(id)}
				}
			}
			texts := make([]string, len(elements))
			for k, e := range elements {
				texts[k] = e.text
			}
			body := elementsText(texts)
			// Cut the body short inside its last element, when no other element has that
			// element's id: a cut that took the second of two answers to one id would
			// leave the first alone, and the first of them is then an answer under the
			// right id, which nothing can tell from a lie.
			if rng.Intn(5) == 0 && len(elements) > 0 {
				last := elements[len(elements)-1]
				unique := true
				for _, e := range elements[:len(elements)-1] {
					unique = unique && e.id != last.id
				}
				if unique {
					start := strings.LastIndex(body, last.text)
					body = body[:start+rng.Intn(len(last.text))]
				}
			}
			return body, nil
		}
		provider.atomicFail = func(string) bool { return rng.Intn(6) == 0 }

		result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{},
			RecoverConfig{MaxIsolatePasses: isolate, Concurrency: 1 + rng.Intn(4)})
		if err != nil {
			t.Fatalf("seed %d: RunOpManyRecover: %v", seed, err)
		}
		assertNoWrongAnswers(t, items, result)
		if t.Failed() {
			t.Fatalf("seed %d: n=%d isolate=%d", seed, n, isolate)
		}
		batch, atomicCalls := provider.counts()
		if bound := recoverRequestBound(n, isolate); batch+atomicCalls > bound {
			t.Fatalf("seed %d: %d requests (%d batched, %d alone) for %d items with %d isolate passes, more than the bound of %d", seed, batch+atomicCalls, batch, atomicCalls, n, isolate, bound)
		}
	}
}

// The bound is a function of the items and the isolate passes, written where the
// documentation says it is.
func TestRecoverRequestBoundIsTheDocumentedFunction(t *testing.T) {
	cases := []struct{ n, passes, want int }{
		{0, 1, 0}, {-3, 1, 0}, {1, 1, 2}, {2, 1, 6}, {3, 1, 8}, {8, 1, 24}, {8, 2, 32}, {8, 3, 40}, {100, 1, 300}, {101, 1, 302}, {5000, 1, 15000},
	}
	for _, tc := range cases {
		if got := recoverRequestBound(tc.n, tc.passes); got != tc.want {
			t.Errorf("recoverRequestBound(%d, %d) = %d, want %d", tc.n, tc.passes, got, tc.want)
		}
	}
}

// --- A request is not sent again as it was, and an extra element costs nothing.

// An isolate pass asks about fewer items than the request before it, and when
// nothing resolved it asks about half of them: the identical request is never sent a
// second time. A model that answers nothing usable is asked, in a batch of six, by two
// batches of three, and then by one request for each item.
func TestRunOpManyRecoverNeverResendsTheSameRequestWhenNothingResolved(t *testing.T) {
	for name, handler := range map[string]recoverBatchHandler{
		"an empty list": func([]taggedItem[recoverWidget]) (string, error) { return `{"items":[]}`, nil },
		"not JSON":      malformedBatch,
		"an empty body": func([]taggedItem[recoverWidget]) (string, error) { return "{}", nil },
		"outputs of null": func(tagged []taggedItem[recoverWidget]) (string, error) {
			elements := make([]string, len(tagged))
			for i, t := range tagged {
				elements[i] = fmt.Sprintf(`{"id":%q,"output":null}`, t.ID)
			}
			return elementsText(elements), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			items := recoverWidgets(6)
			provider := newRecoverFakeProvider("fake-model")
			var sizes []int
			var mu sync.Mutex
			provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
				mu.Lock()
				sizes = append(sizes, len(tagged))
				mu.Unlock()
				return handler(tagged)
			}
			result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
			if err != nil || !result.Complete() {
				t.Fatalf("err %v, %+v", err, result.Summary)
			}
			if fmt.Sprint(sizes) != "[6 3 3]" {
				t.Errorf("the batched requests carried %v items, want [6 3 3]: the first, and then each half, never the same six again", sizes)
			}
			if _, atomic := provider.counts(); atomic != 6 {
				t.Errorf("%d requests of one item, want 6", atomic)
			}
		})
	}
}

// A larger isolate setting is clamped to the documented limit, and no pass is sent
// after one that resolved nothing.
func TestRunOpManyRecoverClampsTheIsolatePassesAndStopsAfterAPassThatResolvedNothing(t *testing.T) {
	items := recoverWidgets(3)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchDefault = func([]taggedItem[recoverWidget]) (string, error) { return `{"items":[]}`, nil }
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxIsolatePasses: 1000})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	batch, atomic := provider.counts()
	if batch > 1+2*MaxIsolatePassesLimit {
		t.Errorf("%d batched requests for three items with nothing usable, more than the limit allows", batch)
	}
	if batch != 2 || atomic != 3 {
		t.Errorf("batch=%d atomic=%d, want the first request, one over half (two items), and one request for each item", batch, atomic)
	}
}

// An element with an id nobody offered, in a response that answers every offered id
// once, fills no item and costs nothing: every item resolves and the chunk size is
// left where it was.
func TestRunOpManyRecoverIgnoresAnExtraElementForSizingAndForAnswers(t *testing.T) {
	const n = 200
	items := recoverWidgets(n)
	sizes := func(extra bool) (requests []int, result types.BatchResult[recoverSummary]) {
		provider := newRecoverFakeProvider("fake-model")
		var mu sync.Mutex
		provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
			mu.Lock()
			requests = append(requests, len(tagged))
			mu.Unlock()
			elements := make([]string, 0, len(tagged)+1)
			for _, t := range tagged {
				elements = append(elements, rightElement(t))
			}
			if extra {
				elements = append(elements, wrongElement("an-id-nobody-offered"))
			}
			return elementsText(elements), nil
		}
		var err error
		result, err = RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 64})
		if err != nil {
			t.Fatalf("RunOpManyRecover: %v", err)
		}
		return requests, result
	}
	plain, _ := sizes(false)
	withExtra, result := sizes(true)
	assertNoWrongAnswers(t, items, result)
	if !result.Complete() {
		t.Errorf("Summary = %+v", result.Summary)
	}
	if fmt.Sprint(plain) != fmt.Sprint(withExtra) {
		t.Errorf("an extra element changed the requests from %v to %v", plain, withExtra)
	}
}

// A truncated list still gives the complete elements before the cut, each by its own
// id, and the rest is asked again.
func TestRunOpManyRecoverKeepsTheCompleteElementsOfAnAnswerCutOffInTheMiddle(t *testing.T) {
	items := recoverWidgets(6)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{func(tagged []taggedItem[recoverWidget]) (string, error) {
		body, _ := fullSuccessBatch(tagged)
		return body[:strings.LastIndex(body, `{"id"`)+12], nil
	}}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	assertNoWrongAnswers(t, items, result)
	// The five complete elements are kept from the first request, and only the one
	// that was cut off is asked again.
	for i, item := range result.Items {
		wantAttempts := 1
		if i == 5 {
			wantAttempts = 2
		}
		if item.Attempts != wantAttempts {
			t.Errorf("item %d was asked %d times, want %d", i, item.Attempts, wantAttempts)
		}
	}
	if !strings.Contains(result.Ledger.Explain(), "first request: 6 item(s) asked, 5 resolved, 1 left") {
		t.Errorf("the ledger does not say five elements were kept:\n%s", result.Ledger.Explain())
	}
}

// --- A provider's refusal of a batch is about some item, not about the run.

// One item the provider's filter refuses does not take the items around it: the
// request that holds it is split down to that item, which fails alone with a refusal,
// where the others, in the same request and in later chunks, are answered.
func TestRunOpManyRecoverSplitsARefusedRequestDownToTheItemThatIsRefused(t *testing.T) {
	const n = 50
	items := recoverWidgets(n)
	bad := items[17].Name
	provider := newRecoverFakeProvider("fake-model")
	var mu sync.Mutex
	refusedCalls := map[int]bool{}
	provider.batchFinish = func(call int) string {
		mu.Lock()
		defer mu.Unlock()
		if refusedCalls[call] {
			return "content_filter"
		}
		return "stop"
	}
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call := len(refusedCalls)
		refusedCalls[call] = false
		for _, t := range tagged {
			if t.Item.Name == bad {
				// The provider's filter withholds the answer: nothing is written.
				refusedCalls[call] = true
				return "", nil
			}
		}
		return fullSuccessBatch(tagged)
	}
	provider.atomicFail = func(name string) bool { return name == bad }

	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 10})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("ledger: %s", strings.ReplaceAll(result.Ledger.Explain(), "\n", " | "))
		}
	})
	assertNoWrongAnswers(t, items, result)
	if result.Summary.Succeeded != n-1 || result.Summary.Failed != 1 {
		t.Fatalf("Summary = %+v, want every item but the refused one answered", result.Summary)
	}
	if result.Items[17].Status != types.ItemFailed {
		t.Errorf("the refused item: %+v", result.Items[17])
	}
	batch, atomic := provider.counts()
	if batch > 20 {
		t.Errorf("%d batched requests for one refused item among %d, want a split of a ten-item chunk and the chunks after it", batch, n)
	}
	if atomic != 1 {
		t.Errorf("%d requests of one item, want only the refused item asked alone", atomic)
	}
}

// A refusal of the client's own policy repeats for every item and no splitting can
// change it, so it still stops the run.
func TestRunOpManyRecoverStillStopsOnAnItemRefusedByThePolicyOfTheClient(t *testing.T) {
	items := recoverWidgets(12)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchDefault = func([]taggedItem[recoverWidget]) (string, error) {
		return "", types.Newf(types.KindPolicyViolation, "client", "the data policy refuses this provider")
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 4})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if batch, atomic := provider.counts(); batch != 1 || atomic != 0 {
		t.Errorf("batch=%d atomic=%d, want the first request and nothing more", batch, atomic)
	}
	if result.Summary.Failed != 12 {
		t.Errorf("Summary = %+v, want every item failed with the policy's refusal", result.Summary)
	}
	for _, item := range result.Items {
		if types.KindOf(item.Err) != types.KindPolicyViolation || types.NarrowingOf(item.Err) == types.NarrowRefusal {
			t.Fatalf("an item's error: kind %v, narrowing %v", types.KindOf(item.Err), types.NarrowingOf(item.Err))
		}
	}
}

// --- A provider that is down is not asked about every item.

// After outageRequestLimit requests in a row fail in transport the run stops, and
// every item that was not answered fails with the last of those failures, where it
// used to send the chunk, its isolation and each item on its own, about four requests
// for each item.
func TestRunOpManyRecoverStopsAfterRepeatedTransportFailures(t *testing.T) {
	for _, n := range []int{10, 100} {
		t.Run(fmt.Sprintf("%d items", n), func(t *testing.T) {
			items := recoverWidgets(n)
			provider := newRecoverFakeProvider("fake-model")
			down := func() error {
				return &llm.RateLimitError{APIError: &llm.APIError{Provider: "recover-fake", StatusCode: 429}, RetryAfter: time.Millisecond}
			}
			// Every request of every kind is refused with a 429.
			provider.batchHook = func(context.Context, int) error { return down() }
			provider.atomicErr = func(string) error { return down() }

			result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 5, Concurrency: 1})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			batch, atomic := provider.counts()
			// Three requests in a row that fail in transport end the run, and with one
			// worker nothing else is in flight: four at the most.
			if outageRequestLimit != 3 || batch+atomic > 4 {
				t.Errorf("%d requests (%d batched, %d alone) against a provider that refuses every one, want at most 4 (the limit is %d)", batch+atomic, batch, atomic, outageRequestLimit)
			}
			if result.Summary.Failed != n {
				t.Errorf("Summary = %+v, want every item failed", result.Summary)
			}
			for _, item := range result.Items {
				if types.KindOf(item.Err) != types.KindRateLimited {
					t.Fatalf("an item's error has kind %v, want the rate limit that ended the run", types.KindOf(item.Err))
				}
			}
		})
	}
}

// Failures that are not in a row do not stop the run: an answer in between starts the
// count again.
func TestRunOpManyRecoverDoesNotStopForTransportFailuresThatAreNotInARow(t *testing.T) {
	items := recoverWidgets(40)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchHook = func(_ context.Context, call int) error {
		if call%2 == 0 {
			return &llm.RateLimitError{APIError: &llm.APIError{Provider: "recover-fake", StatusCode: 429}, RetryAfter: time.Millisecond}
		}
		return nil
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 5})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
}

// --- The planner decides the size of the batches.

// An item too large to share a request with any other is sent alone, without being
// asked in a batch first, and a batch is never larger than the planner cut it.
func TestRunOpManyRecoverSendsAnOversizedItemAloneAndCutsChunksAsThePlannerDoes(t *testing.T) {
	items := recoverWidgets(8)
	items[3] = recoverWidget{Name: strings.Repeat("x", 4*DefaultContextTokens+10)}
	provider := newRecoverFakeProvider("fake-model")
	var mu sync.Mutex
	var sizes []int
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		mu.Lock()
		sizes = append(sizes, len(tagged))
		mu.Unlock()
		for _, t := range tagged {
			if len(t.Item.Name) > DefaultContextTokens {
				return "", fmt.Errorf("the oversized item was sent in a batch")
			}
		}
		return fullSuccessBatch(tagged)
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	if result.Items[3].Mode != types.ShapeAtomic.String() || result.Items[3].Attempts != 1 {
		t.Errorf("the oversized item: mode %q, attempts %d, want one request of its own", result.Items[3].Mode, result.Items[3].Attempts)
	}
	total := 0
	for _, size := range sizes {
		total += size
	}
	if total != 7 {
		t.Errorf("the batches carried %d items, want the 7 that are not oversized", total)
	}
	if batch, atomic := provider.counts(); batch != 2 || atomic != 1 {
		t.Errorf("batch=%d atomic=%d, want the planner's two batches (the oversized item splits the packing) and one request alone", batch, atomic)
	}
	if fmt.Sprint(sizes) != "[3 4]" {
		t.Errorf("the batches carried %v items, want the planner's [3 4]", sizes)
	}
}

// A chunk of one item is that item's own request.
func TestRunOpManyRecoverSendsAChunkOfOneItemAlone(t *testing.T) {
	items := recoverWidgets(5)
	provider := newRecoverFakeProvider("fake-model")
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 1})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	if batch, atomic := provider.counts(); batch != 0 || atomic != 5 {
		t.Errorf("batch=%d atomic=%d, want five requests of one item", batch, atomic)
	}
	for i, item := range result.Items {
		if item.Mode != types.ShapeAtomic.String() {
			t.Errorf("item %d: mode %q, want the mode of an item sent alone", i, item.Mode)
		}
	}
	if m := result.Metrics(); m.AtomicFallbacks != 0 || m.Omissions != 0 {
		t.Errorf("metrics = %+v: items sent alone from the start are no fallback", m)
	}
}

// --- What the run refuses before it sends anything.

func TestRunOpManyRecoverRefusesABatchThatIsNotTheIDProtocolOrCarriesEvidence(t *testing.T) {
	handBuilt := recoverWidgetOp()
	handBuilt.Batch = BatchAlgebra[recoverWidget, recoverSummary]{
		Class: types.BatchItemwise, Kind: types.AlgebraIndependent,
		Encode: func(items []recoverWidget, _ types.OpOptions) (string, string, error) {
			return "csv", strings.Repeat("row\n", len(items)), nil
		},
		Merge: func(string, []recoverWidget) ([]recoverSummary, error) { return nil, nil },
	}
	evidence := recoverWidgetOp()
	evidence.Contract.EvidenceRequired = true
	aggregate := recoverWidgetOp()
	aggregate.Batch.Class = types.BatchAggregate

	for name, op := range map[string]Op[recoverWidget, recoverSummary]{
		"a batch with an Encode and Merge of its own": handBuilt,
		"an operation that requires evidence":         evidence,
		"an aggregate batch":                          aggregate,
	} {
		t.Run(name, func(t *testing.T) {
			provider := newRecoverFakeProvider("fake-model")
			result, err := RunOpManyRecover(recoverCtx(t, provider), op, recoverWidgets(6), recoverCodec(), types.OpOptions{}, RecoverConfig{})
			if types.KindOf(err) != types.KindConfiguration {
				t.Fatalf("err = %v (%v), want a configuration error", err, types.KindOf(err))
			}
			if len(result.Items) != 0 {
				t.Errorf("a result came back beside the error: %+v", result.Summary)
			}
			if batch, atomic := provider.counts(); batch+atomic != 0 {
				t.Errorf("%d requests were sent before the refusal", batch+atomic)
			}
		})
	}
}

// A codec with no DecodeItem used to be refused by the recovering run and to panic
// in the batch's Merge after the provider had been paid. Every path refuses it before
// a request is sent.
func TestACodecWithNoDecodeItemIsRefusedBeforeAnythingIsSent(t *testing.T) {
	op := recoverWidgetOp()
	op.Batch = NewIDBatchAlgebra[recoverWidget, recoverSummary](types.BatchItemwise, types.AlgebraIndependent, "system", BatchItemCodec[recoverWidget, recoverSummary]{})
	provider := newRecoverFakeProvider("fake-model")
	ctx := recoverCtx(t, provider)

	if outputs, err := RunOpBatch(ctx, op, recoverWidgets(3), types.OpOptions{}); types.KindOf(err) != types.KindConfiguration || outputs != nil {
		t.Errorf("RunOpBatch: outputs %v, err %v (%v)", outputs, err, types.KindOf(err))
	}
	if _, err := RunOpBatchResult(ctx, op, recoverWidgets(3), types.OpOptions{}); types.KindOf(err) != types.KindConfiguration {
		t.Errorf("RunOpBatchResult: err %v (%v)", err, types.KindOf(err))
	}
	if _, err := op.Batch.Merge(`{"items":[]}`, recoverWidgets(3)); types.KindOf(err) != types.KindConfiguration {
		t.Errorf("Merge: err %v (%v)", err, types.KindOf(err))
	}
	if _, _, err := op.Batch.Encode(recoverWidgets(3), types.OpOptions{}); types.KindOf(err) != types.KindConfiguration {
		t.Errorf("Encode: err %v (%v)", err, types.KindOf(err))
	}
	if batch, atomic := provider.counts(); batch+atomic != 0 {
		t.Errorf("%d requests were sent", batch+atomic)
	}
}

// A context that is already over runs nothing, and none of the caller's functions.
func TestRunOpManyRecoverRefusesAContextThatIsAlreadyOver(t *testing.T) {
	provider := newRecoverFakeProvider("fake-model")
	ctx, cancel := context.WithCancel(recoverCtx(t, provider))
	cancel()
	encoded := 0
	op := recoverWidgetOp()
	inner := op.Batch.Encode
	op.Batch.Encode = func(items []recoverWidget, opt types.OpOptions) (string, string, error) {
		encoded++
		return inner(items, opt)
	}
	result, err := RunOpManyRecover(ctx, op, recoverWidgets(4), recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if types.KindOf(err) != types.KindCanceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v (%v), want a cancellation that matches context.Canceled", err, types.KindOf(err))
	}
	if len(result.Items) != 0 || encoded != 0 {
		t.Errorf("a result came back or the caller's Encode was called %d times", encoded)
	}
	if batch, atomic := provider.counts(); batch+atomic != 0 {
		t.Errorf("%d requests were sent", batch+atomic)
	}
}

// --- Cancelled is cancelled.

// A run the caller's context cut short keeps the items that finished, and marks the
// others cancelled, with an error that matches the context's, in the recovering run as
// under every policy of the partial one.
func TestRunOpManyRecoverMarksTheItemsACancelledContextCutOffAsCancelled(t *testing.T) {
	items := recoverWidgets(30)
	provider := newRecoverFakeProvider("fake-model")
	ctx, cancel := context.WithCancel(recoverCtx(t, provider))
	defer cancel()
	provider.batchHook = func(_ context.Context, call int) error {
		if call == 2 {
			cancel()
		}
		return nil
	}
	result, err := RunOpManyRecover(ctx, recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if result.Summary.Succeeded == 0 || result.Summary.Cancelled == 0 || result.Summary.Failed != 0 {
		t.Fatalf("Summary = %+v, want finished items kept and the rest cancelled, none failed", result.Summary)
	}
	for _, item := range result.Items {
		if item.Status != types.ItemCancelled {
			continue
		}
		if types.KindOf(item.Err) != types.KindCanceled || !errors.Is(item.Err, context.Canceled) {
			t.Fatalf("a cancelled item's error: %v (%v)", item.Err, types.KindOf(item.Err))
		}
	}
	if result.Complete() {
		t.Error("a cut-short run reads as complete")
	}
}

// --- The ledger says what each request did.

func TestRunOpManyRecoverLedgerRecordsEachRequestAndWhatItResolved(t *testing.T) {
	items := recoverWidgets(5)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchScript = []recoverBatchHandler{omitBatch("i-000002", "i-000004"), fullSuccessBatch}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	text := result.Ledger.Explain()
	for _, want := range []string{"first request: 5 item(s) asked, 3 resolved, 2 left", "isolate pass: 2 item(s) asked, 2 resolved, 0 left", "omission"} {
		if !strings.Contains(text, want) {
			t.Errorf("the ledger does not say %q:\n%s", want, text)
		}
	}
	if m := result.Metrics(); m.Requests != 2 {
		t.Errorf("BatchMetrics.Requests = %d, want the 2 requests the provider answered", m.Requests)
	}
}

// --- The caller's own functions fail an item and not the caller's goroutine.

func TestRunOpManyRecoverSurvivesCallerFunctionsThatEndTheirGoroutineInTheBatchedRung(t *testing.T) {
	type breakAt struct {
		name  string
		build func() (Op[recoverWidget, recoverSummary], BatchItemCodec[recoverWidget, recoverSummary])
	}
	cases := []breakAt{
		{"Encode", func() (Op[recoverWidget, recoverSummary], BatchItemCodec[recoverWidget, recoverSummary]) {
			op := recoverWidgetOp()
			inner := op.Batch.Encode
			op.Batch.Encode = func(items []recoverWidget, opt types.OpOptions) (string, string, error) {
				runtime.Goexit()
				return inner(items, opt)
			}
			return op, recoverCodec()
		}},
		{"DecodeItem", func() (Op[recoverWidget, recoverSummary], BatchItemCodec[recoverWidget, recoverSummary]) {
			codec := recoverCodec()
			codec.DecodeItem = func(json.RawMessage) (recoverSummary, error) { runtime.Goexit(); return recoverSummary{}, nil }
			return recoverWidgetOp(), codec
		}},
		{"an invariant", func() (Op[recoverWidget, recoverSummary], BatchItemCodec[recoverWidget, recoverSummary]) {
			op := recoverWidgetOp()
			op.Contract.Invariants = []func(recoverSummary) error{func(recoverSummary) error { runtime.Goexit(); return nil }}
			return op, recoverCodec()
		}},
		{"Normalize", func() (Op[recoverWidget, recoverSummary], BatchItemCodec[recoverWidget, recoverSummary]) {
			op := recoverWidgetOp()
			op.Contract.Normalize = func(recoverSummary) recoverSummary { runtime.Goexit(); return recoverSummary{} }
			return op, recoverCodec()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, codec := tc.build()
			done := make(chan struct{})
			var result types.BatchResult[recoverSummary]
			var err error
			go func() {
				defer close(done)
				result, err = RunOpManyRecover(recoverCtx(t, newRecoverFakeProvider("fake-model")), op, recoverWidgets(4), codec, types.OpOptions{}, RecoverConfig{})
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the caller's goroutine ended inside the cascade, and it never returned")
			}
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			if len(result.Items) != 4 {
				t.Fatalf("%d items", len(result.Items))
			}
		})
	}
}

// A function of the caller's that panics on the answer of a request in the final rung
// does not take that request's cost with it: Total is what the provider billed.
func TestRunOpManyRecoverTotalIncludesTheRequestsWhoseAnswerACallersFunctionPanickedOn(t *testing.T) {
	items := recoverWidgets(3)
	provider := newRecoverFakeProvider("fake-model")
	provider.priced = true
	provider.batchDefault = func([]taggedItem[recoverWidget]) (string, error) { return `{"items":[]}`, nil }
	op := recoverWidgetOp()
	op.Contract.Decode = func(string) (recoverSummary, error) { panic("the caller's decoder broke") }
	ctx := recoverCtx(t, provider)

	result, err := RunOpManyRecover(ctx, op, items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	batch, atomic := provider.counts()
	want := (batch + atomic) * 150
	if result.Total.Usage.TotalTokens != want || result.Total.Attempts != batch+atomic {
		t.Errorf("Total = %d tokens over %d attempts, want what the provider billed: %d tokens over %d requests", result.Total.Usage.TotalTokens, result.Total.Attempts, want, batch+atomic)
	}
	if result.Summary.Failed != 3 {
		t.Errorf("Summary = %+v", result.Summary)
	}
	for _, item := range result.Items {
		if types.NarrowingOf(item.Err) != types.NarrowPanic || strings.Contains(item.Err.Error(), "the caller's decoder broke") {
			t.Fatalf("an item's error: %v", item.Err)
		}
	}
}

// --- The text of an item's error is the library's.

// Nothing a vendor said, and nothing the caller's own Decode, DecodeItem or invariant
// reported, is in an item's error, in the ledger or in the summary, whichever rung
// the item ended on: only the operation, the kind and the item's place. The caller's
// own error is the cause, reached with errors.Is. The repair prompt's boilerplate
// never appears.
func TestRunOpManyRecoverErrorTextsCarryNoVendorOrCallerText(t *testing.T) {
	const vendor, callerText, marker = "CANARY-VENDOR", "CANARY-CALLER", "CANARY-ITEM"
	callerErr := errors.New(callerText + " cannot read " + marker)

	cases := map[string]func(*recoverFakeProvider, *Op[recoverWidget, recoverSummary], *BatchItemCodec[recoverWidget, recoverSummary]){
		"the batched requests fail with the vendor's words": func(p *recoverFakeProvider, _ *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			p.batchDefault = func([]taggedItem[recoverWidget]) (string, error) {
				return "", errors.New("upstream said " + vendor + " quota for " + marker)
			}
			p.atomicErr = func(string) error { return errors.New("upstream said " + vendor + " quota for " + marker) }
		},
		"DecodeItem fails with the caller's words": func(p *recoverFakeProvider, op *Op[recoverWidget, recoverSummary], codec *BatchItemCodec[recoverWidget, recoverSummary]) {
			codec.DecodeItem = func(json.RawMessage) (recoverSummary, error) { return recoverSummary{}, callerErr }
			op.Contract.Decode = func(string) (recoverSummary, error) { return recoverSummary{}, callerErr }
		},
		"an invariant fails with the caller's words": func(p *recoverFakeProvider, op *Op[recoverWidget, recoverSummary], _ *BatchItemCodec[recoverWidget, recoverSummary]) {
			op.Contract.Invariants = []func(recoverSummary) error{func(recoverSummary) error { return callerErr }}
		},
		"Decode panics with the caller's data": func(p *recoverFakeProvider, op *Op[recoverWidget, recoverSummary], codec *BatchItemCodec[recoverWidget, recoverSummary]) {
			codec.DecodeItem = func(json.RawMessage) (recoverSummary, error) { panic(callerText + marker) }
			op.Contract.Decode = func(string) (recoverSummary, error) { panic(callerText + marker) }
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			provider := newRecoverFakeProvider("fake-model")
			op, codec := recoverWidgetOp(), recoverCodec()
			set(provider, &op, &codec)
			result, err := RunOpManyRecover(recoverCtx(t, provider), op, recoverWidgets(5), codec, types.OpOptions{}, RecoverConfig{})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			if result.Summary.Failed != 5 {
				t.Fatalf("Summary = %+v, want every item failed", result.Summary)
			}
			texts := []string{result.String(), result.Summary.PolicyReason, result.Ledger.Explain()}
			for _, entry := range result.Ledger.Entries {
				texts = append(texts, entry.Node, entry.Decision, entry.Reason)
			}
			for _, item := range result.Items {
				texts = append(texts, item.Err.Error())
			}
			for _, text := range texts {
				for _, canary := range []string{"CANARY", "Your previous answer", "Return a corrected answer", "upstream said"} {
					if strings.Contains(text, canary) {
						t.Errorf("%q carries %q", text, canary)
					}
				}
			}
			for i, item := range result.Items {
				got := item.Err.Error()
				if !strings.HasPrefix(got, "recoverWidget: ") || !strings.Contains(got, fmt.Sprintf("item %d of 5", i+1)) {
					t.Errorf("%q does not name the operation and the item's place", got)
				}
			}
			if strings.HasPrefix(name, "DecodeItem fails") && !errors.Is(result.Items[0].Err, callerErr) {
				t.Errorf("the caller's own error is not in the chain of %v", result.Items[0].Err)
			}
		})
	}
}

// --- A client's budget stops the run, and the final rung's overlap overshoots it by the
// concurrency less one.

func TestRunOpManyRecoverABudgetStopsTheRunAndTheFinalRungOvershootsByAtMostTheConcurrencyLessOne(t *testing.T) {
	t.Run("batched requests", func(t *testing.T) {
		budget := &requestCapBudget{limit: 2}
		provider := newRecoverFakeProvider("fake-model")
		provider.priced = true
		provider.batchDefault = func([]taggedItem[recoverWidget]) (string, error) { return `{"items":[]}`, nil }
		ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Budget: budget})
		configtestModel(t)
		result, err := RunOpManyRecover(ctx, recoverWidgetOp(), recoverWidgets(100), recoverCodec(), types.OpOptions{}, RecoverConfig{Concurrency: 1})
		if err != nil {
			t.Fatalf("RunOpManyRecover: %v", err)
		}
		batch, atomic := provider.counts()
		if batch+atomic != 2 {
			t.Errorf("%d requests with a budget of 2 and a bound of one, want exactly 2", batch+atomic)
		}
		for _, item := range result.Items {
			if types.KindOf(item.Err) != types.KindBudgetExceeded {
				t.Fatalf("an item failed with %v, want the budget", types.KindOf(item.Err))
			}
		}
	})
	for _, workers := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprintf("the final rung with %d workers", workers), func(t *testing.T) {
			budget := &requestCapBudget{limit: 3}
			provider := newRecoverFakeProvider("fake-model")
			provider.priced = true
			ctx := WithExecConfig(context.Background(), &ExecConfig{Provider: provider, Budget: budget})
			configtestModel(t)
			result, err := RunOpManyRecover(ctx, recoverWidgetOp(), recoverWidgets(40), recoverCodec(), types.OpOptions{}, RecoverConfig{MaxItemsPerCall: 1, Concurrency: workers})
			if err != nil {
				t.Fatalf("RunOpManyRecover: %v", err)
			}
			_, atomic := provider.counts()
			if limit := 3 + workers - 1; atomic < 3 || atomic > limit {
				t.Errorf("%d requests with a budget of 3 and %d workers, want between 3 and %d", atomic, workers, limit)
			}
			if result.Summary.Succeeded != atomic {
				t.Errorf("Summary = %+v for %d requests", result.Summary, atomic)
			}
		})
	}
}

// configtestModel names the model the fake provider answers for.
func configtestModel(t *testing.T) {
	t.Helper()
	configtest.Setenv(t, "SCHEMAFLUX_MODEL", "fake-model")
}

// An answer belongs to the item whose id it carries: not the item whose place its
// element has in the response. A model that answers in reverse order, each element
// under its own id, changes nothing.
func TestRunOpManyRecoverAttributesAnAnswerByItsIDAloneAndNeverByPosition(t *testing.T) {
	items := recoverWidgets(7)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		elements := make([]string, 0, len(tagged))
		for place := len(tagged) - 1; place >= 0; place-- {
			elements = append(elements, rightElement(tagged[place]))
		}
		return elementsText(elements), nil
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	assertNoWrongAnswers(t, items, result)
	for i, item := range result.Items {
		if item.Mode != ModeMDSP || item.Attempts != 1 {
			t.Errorf("item %d: mode %q after %d attempts, want its answer taken from the one batched request", i, item.Mode, item.Attempts)
		}
	}
}

// The limit: a model that writes one item's answer under another item's id is
// believed, because nothing in Go can compare an answer with its question, and the
// documentation of both engines and of the root wrappers says so. The remedy is one
// request per item, which RunOpManyPartial sends, and which cannot misattribute.
func TestRunOpManyRecoverBelievesAnAnswerWrittenUnderAnotherItemsIDAndOneRequestPerItemDoesNot(t *testing.T) {
	items := recoverWidgets(6)
	provider := newRecoverFakeProvider("fake-model")
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		elements := make([]string, len(tagged))
		for place, t := range tagged {
			owner := t
			switch place {
			case 0:
				owner = tagged[1]
			case 1:
				owner = tagged[0]
			}
			// The answer of one item, written under the id of the other.
			elements[place] = fmt.Sprintf(`{"id":%q,"output":{"length":%d}}`, t.ID, len(owner.Item.Name))
		}
		return elementsText(elements), nil
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if result.Items[0].Value.Length != len(items[1].Name) || result.Items[1].Value.Length != len(items[0].Name) {
		t.Errorf("the swapped answers were not taken at the model's word: %+v %+v", result.Items[0].Value, result.Items[1].Value)
	}
	for i := 2; i < len(items); i++ {
		if result.Items[i].Value.Length != len(items[i].Name) {
			t.Errorf("item %d, which the model did not touch, holds %d", i, result.Items[i].Value.Length)
		}
	}

	// One request per item answers each item about itself.
	alone, err := RunOpManyPartial(recoverCtx(t, newRecoverFakeProvider("fake-model")), recoverWidgetOp(), items, types.OpOptions{}, PartialConfig{})
	if err != nil || !alone.Complete() {
		t.Fatalf("RunOpManyPartial: %v, %+v", err, alone.Summary)
	}
	assertNoWrongAnswers(t, items, alone)
}

// The isolate passes stop at the documented limit however many the caller asks for,
// when every pass resolves something: a model that answers one item of a request and
// no more is asked about the rest in a request that is one smaller each time, and
// the limit is where that ends.
func TestRunOpManyRecoverStopsIsolatingAtTheDocumentedLimit(t *testing.T) {
	items := recoverWidgets(10)
	provider := newRecoverFakeProvider("fake-model")
	var sizes []int
	var mu sync.Mutex
	provider.batchDefault = func(tagged []taggedItem[recoverWidget]) (string, error) {
		mu.Lock()
		sizes = append(sizes, len(tagged))
		mu.Unlock()
		return elementsText([]string{rightElement(tagged[0])}), nil
	}
	result, err := RunOpManyRecover(recoverCtx(t, provider), recoverWidgetOp(), items, recoverCodec(), types.OpOptions{}, RecoverConfig{MaxIsolatePasses: 1000})
	if err != nil || !result.Complete() {
		t.Fatalf("err %v, %+v", err, result.Summary)
	}
	assertNoWrongAnswers(t, items, result)
	if want := fmt.Sprint([]int{10, 9, 8, 7}); fmt.Sprint(sizes) != want {
		t.Errorf("the batched requests carried %v items, want %s: the first, and %d isolate passes, however many were asked for", sizes, want, MaxIsolatePassesLimit)
	}
	if _, atomic := provider.counts(); atomic != 6 {
		t.Errorf("%d requests of one item, want the 6 the isolate passes did not resolve", atomic)
	}
}
