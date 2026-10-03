package tests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// These tests run RunOpManyPartial and RunOpManyRecover through the root with a
// scripted provider, and count what the provider was asked, because "only the bad
// item was asked again" is a fact about calls and not about the values returned.

const refusedItem = "item-3"

var fiveItems = []string{"item-1", "item-2", "item-3", "item-4", "item-5"}

// providerRefusing answers every item with "GOT: <item>" except the one named,
// which it fails. A plain error is not classified, so the retry policies treat it
// as worth sending again.
func providerRefusing(failing string) *schemafluxtest.Provider {
	return schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		if req.UserPrompt == failing {
			return "", errors.New("the provider could not serve this item")
		}
		return "GOT: " + req.UserPrompt, nil
	})
}

// callsFor counts the requests whose user prompt was exactly prompt.
func callsFor(p *schemafluxtest.Provider, prompt string) int {
	count := 0
	for _, req := range p.Requests() {
		if req.UserPrompt == prompt {
			count++
		}
	}
	return count
}

// The third of five items fails under every policy. What differs is what the
// other four are told, how many calls the third costs, and whether the call
// itself reports an error.
func TestRunOpManyPartialWithTheThirdOfFiveItemsFailingUnderEachPolicy(t *testing.T) {
	succeeded, failed, cancelled := schemaflux.ItemSucceeded, schemaflux.ItemFailed, schemaflux.ItemCancelled
	cases := []struct {
		name           string
		policy         schemaflux.BatchPolicy
		statuses       []schemaflux.ItemStatus
		wantErr        bool
		callsForThird  int
		totalCalls     int
		wantPolicyNote bool
	}{
		{"fail fast stops at the failure", schemaflux.PolicyFailFast,
			[]schemaflux.ItemStatus{succeeded, succeeded, failed, cancelled, cancelled}, true, 1, 3, false},
		{"collect failures runs every item once and reports no error", schemaflux.PolicyCollectFailures,
			[]schemaflux.ItemStatus{succeeded, succeeded, failed, succeeded, succeeded}, false, 1, 5, false},
		{"require all runs every item once and fails the call", schemaflux.PolicyRequireAll,
			[]schemaflux.ItemStatus{succeeded, succeeded, failed, succeeded, succeeded}, true, 1, 5, false},
		{"retry failed items retries only the third and fails the call", schemaflux.PolicyRetryFailedItems,
			[]schemaflux.ItemStatus{succeeded, succeeded, failed, succeeded, succeeded}, true, 3, 7, false},
		{"retry then collect retries only the third and reports no error", schemaflux.PolicyRetryThenCollect,
			[]schemaflux.ItemStatus{succeeded, succeeded, failed, succeeded, succeeded}, false, 3, 7, false},
		{"an unspecified policy collects the failures, as it does for any number of items", schemaflux.PolicyUnspecified,
			[]schemaflux.ItemStatus{succeeded, succeeded, failed, succeeded, succeeded}, false, 1, 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := providerRefusing(refusedItem)
			schemafluxtest.Install(t, provider)

			// One item at a time, so that the items after the failure are ones
			// that were never started and not ones in flight when it happened.
			result, err := schemaflux.RunOpManyPartial(context.Background(), echoOp(), fiveItems, schemaflux.OpOptions{},
				schemaflux.PartialConfig{Policy: tc.policy, Concurrency: 1})

			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want an error: %v", err, tc.wantErr)
			}
			if len(result.Items) != len(fiveItems) {
				t.Fatalf("%d item results for %d items: a partial run reports every item", len(result.Items), len(fiveItems))
			}
			for i, item := range result.Items {
				if item.Index != i {
					t.Errorf("item %d has Index %d", i, item.Index)
				}
				if item.Status != tc.statuses[i] {
					t.Errorf("item %d: status %s, want %s", i, item.Status, tc.statuses[i])
				}
				switch item.Status {
				case schemaflux.ItemSucceeded:
					if want := "GOT: " + fiveItems[i]; item.Value != want || item.Err != nil {
						t.Errorf("item %d: value %q, err %v, want %q and no error", i, item.Value, item.Err, want)
					}
				default:
					if item.Value != "" {
						t.Errorf("item %d did not succeed and carries the value %q", i, item.Value)
					}
				}
			}

			if got := callsFor(provider, refusedItem); got != tc.callsForThird {
				t.Errorf("the failing item was sent %d times, want %d", got, tc.callsForThird)
			}
			if got := len(provider.Requests()); got != tc.totalCalls {
				t.Errorf("the provider was called %d times, want %d", got, tc.totalCalls)
			}

			// A failed item is never reported as Complete, and the failures name
			// the third item without reproducing it.
			if result.Complete() {
				t.Error("a run with a failed item reports itself complete")
			}
			failures := result.Failures()
			if len(failures) == 0 || failures[0].Index != 2 {
				t.Fatalf("Failures() = %v, want it to start with the third item", failures)
			}
			if third := result.Items[2]; third.Err == nil || strings.Contains(third.Err.Error(), refusedItem) {
				t.Errorf("the failed item's error is %v: it has to exist and not hold the item", third.Err)
			}
			if err != nil {
				if strings.Contains(err.Error(), refusedItem) {
					t.Errorf("the call's error %q holds the item", err.Error())
				}
				if schemaflux.KindOf(err) != schemaflux.KindOf(result.Items[2].Err) {
					t.Errorf("the call's kind is %v, the failed item's is %v", schemaflux.KindOf(err), schemaflux.KindOf(result.Items[2].Err))
				}
			}

			values, complete := result.Values()
			if complete || len(values) != result.Summary.Succeeded {
				t.Errorf("Values() = %v, %v: want the succeeded values and false", values, complete)
			}
			if result.Summary.Total != 5 || result.Summary.Succeeded+result.Summary.Failed+result.Summary.Cancelled != 5 {
				t.Errorf("summary %+v does not account for five items", result.Summary)
			}
			if result.Summary.Policy == schemaflux.PolicyUnspecified {
				t.Error("the summary leaves the policy unresolved")
			}
			if tc.wantPolicyNote != (result.Summary.PolicyReason != "") {
				t.Errorf("PolicyReason = %q, want one only for a resolved policy", result.Summary.PolicyReason)
			}
		})
	}
}

// A failure that does not repeat is what the retry policies are for: the third
// item fails once, and is the only item sent again.
func TestRunOpManyPartialRetryRecoversAnItemThatFailsOnce(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	provider := schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		mu.Lock()
		seen[req.UserPrompt]++
		count := seen[req.UserPrompt]
		mu.Unlock()
		if req.UserPrompt == refusedItem && count == 1 {
			return "", errors.New("the provider dropped the connection")
		}
		return "GOT: " + req.UserPrompt, nil
	})
	schemafluxtest.Install(t, provider)

	result, err := schemaflux.RunOpManyPartial(context.Background(), echoOp(), fiveItems, schemaflux.OpOptions{},
		schemaflux.PartialConfig{Policy: schemaflux.PolicyRetryFailedItems})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("the retry did not resolve the item: %s", result)
	}
	values, complete := result.Values()
	if !complete || len(values) != 5 || values[2] != "GOT: item-3" {
		t.Errorf("Values() = %v, %v", values, complete)
	}
	if got := callsFor(provider, refusedItem); got != 2 {
		t.Errorf("the failing item was sent %d times, want 2", got)
	}
	if got := len(provider.Requests()); got != 6 {
		t.Errorf("the provider was called %d times, want 6: five items and one retry", got)
	}
	if result.Items[2].Attempts < 2 {
		t.Errorf("the retried item reports %d attempts, want the first failed one counted too", result.Items[2].Attempts)
	}
	for _, i := range []int{0, 1, 3, 4} {
		if result.Items[i].Attempts != 1 {
			t.Errorf("item %d was never retried and reports %d attempts", i, result.Items[i].Attempts)
		}
	}
	if metrics := result.Metrics(); metrics.ValidItemRatio != 1 || metrics.Failed != 0 {
		t.Errorf("metrics %+v for a run whose every item succeeded", metrics)
	}
}

// --- RunOpManyRecover --------------------------------------------------------

type lengthAnswer struct {
	Length int `json:"length"`
}

// lengthOp is an operation batched through the id protocol: the answer for an
// item is its length, in a batch and alone alike.
func lengthOp() (schemaflux.Op[string, lengthAnswer], schemaflux.BatchItemCodec[string, lengthAnswer]) {
	read := func(raw []byte) (lengthAnswer, error) {
		var answer lengthAnswer
		if err := json.Unmarshal(raw, &answer); err != nil {
			return lengthAnswer{}, err
		}
		return answer, nil
	}
	codec := schemaflux.BatchItemCodec[string, lengthAnswer]{
		DecodeItem: func(raw json.RawMessage) (lengthAnswer, error) { return read(raw) },
	}
	op := schemaflux.Op[string, lengthAnswer]{
		ID:        schemaflux.OperationID{Name: "recover-length", Version: "v1"},
		Semantics: schemaflux.Semantics{Stability: schemaflux.StabilityExperimental},
		Contract: schemaflux.OutputContract[lengthAnswer]{
			Decode: func(body string) (lengthAnswer, error) { return read([]byte(body)) },
		},
		Batch: schemaflux.NewIDBatchAlgebra(schemaflux.BatchItemwise, schemaflux.AlgebraIndependent, "Give the length of each item.", codec),
		BuildPrompt: func(input string, _ schemaflux.OpOptions) (string, string) {
			return "give the length of this item", input
		},
	}
	return op, codec
}

const batchPreface = "Process these items:\n"

// offeredItems reads the ids and items a batched request carried.
func offeredItems(t *testing.T, req schemaflux.CompletionRequest) []struct{ ID, Item string } {
	t.Helper()
	if !strings.HasPrefix(req.UserPrompt, batchPreface) {
		return nil
	}
	var offered []struct {
		ID   string `json:"id"`
		Item string `json:"item"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(req.UserPrompt, batchPreface)), &offered); err != nil {
		t.Fatalf("the batched request is not the tagged list: %v", err)
	}
	out := make([]struct{ ID, Item string }, len(offered))
	for i, o := range offered {
		out[i] = struct{ ID, Item string }{o.ID, o.Item}
	}
	return out
}

// providerWithABadElement answers a batch with every element correct except the
// one for badItem, whose answer is the wrong type, for as many of the batches that
// carry it as badFor says. An atomic call is answered correctly.
func providerWithABadElement(t *testing.T, badItem string, badFor int) *schemafluxtest.Provider {
	var mu sync.Mutex
	badSoFar := 0
	return schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		offered := offeredItems(t, req)
		if offered == nil {
			return fmt.Sprintf(`{"length":%d}`, len(req.UserPrompt)), nil
		}
		var elements []string
		for _, o := range offered {
			output := fmt.Sprintf(`{"length":%d}`, len(o.Item))
			if o.Item == badItem {
				mu.Lock()
				if badSoFar < badFor {
					output = `{"length":"not a number"}`
					badSoFar++
				}
				mu.Unlock()
			}
			elements = append(elements, fmt.Sprintf(`{"id":%q,"output":%s}`, o.ID, output))
		}
		return `{"items":[` + strings.Join(elements, ",") + `]}`, nil
	})
}

// One element of a five-item answer does not read as the type asked for. The
// four good ones are kept, and the next call carries the one that failed and
// nothing else.
func TestRunOpManyRecoverAsksAgainOnlyForTheItemWhoseAnswerWasBad(t *testing.T) {
	provider := providerWithABadElement(t, "item-3", 1)
	schemafluxtest.Install(t, provider)
	op, codec := lengthOp()

	result, err := schemaflux.RunOpManyRecover(context.Background(), op, fiveItems, codec, schemaflux.OpOptions{},
		schemaflux.RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("the bad element was not recovered: %s", result)
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("the provider was called %d times, want 2: one batch of five and one of the failed item", len(requests))
	}
	if first := offeredItems(t, requests[0]); len(first) != 5 {
		t.Errorf("the first call carried %d items, want 5", len(first))
	}
	second := offeredItems(t, requests[1])
	if len(second) != 1 || second[0].Item != "item-3" {
		t.Errorf("the second call carried %v, want only item-3", second)
	}

	for i, item := range result.Items {
		if item.Status != schemaflux.ItemSucceeded || item.Err != nil {
			t.Errorf("item %d: status %s, err %v", i, item.Status, item.Err)
		}
		if want := len(fiveItems[i]); item.Value.Length != want {
			t.Errorf("item %d: length %d, want %d", i, item.Value.Length, want)
		}
		if item.Mode != schemaflux.ShapeMDSP.String() {
			t.Errorf("item %d: mode %q, want it resolved from a batched answer", i, item.Mode)
		}
		wantAttempts := 1
		if i == 2 {
			wantAttempts = 2
		}
		if item.Attempts != wantAttempts {
			t.Errorf("item %d: %d attempts, want %d", i, item.Attempts, wantAttempts)
		}
	}

	metrics := result.Metrics()
	if metrics.Omissions != 1 || metrics.AtomicFallbacks != 0 || metrics.ValidItemRatio != 1 {
		t.Errorf("metrics %+v: want one item that needed an isolate pass, no fallback and every item valid", metrics)
	}
	if len(result.Ledger.Entries) == 0 || !strings.Contains(result.Ledger.Explain(), "chunk 1") {
		t.Errorf("the ledger does not record the chunk: %q", result.Ledger.Explain())
	}
}

// The same item is wrong again in the smaller batch, so it goes to a call of its
// own, and only it does.
func TestRunOpManyRecoverFallsBackToOneCallForAnItemTheIsolatePassDidNotResolve(t *testing.T) {
	provider := providerWithABadElement(t, "item-3", 2)
	schemafluxtest.Install(t, provider)
	op, codec := lengthOp()

	result, err := schemaflux.RunOpManyRecover(context.Background(), op, fiveItems, codec, schemaflux.OpOptions{},
		schemaflux.RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	if !result.Complete() {
		t.Fatalf("the item was not recovered by its own call: %s", result)
	}
	requests := provider.Requests()
	if len(requests) != 3 {
		t.Fatalf("the provider was called %d times, want 3: the batch, the isolate pass and one atomic call", len(requests))
	}
	if offeredItems(t, requests[2]) != nil || requests[2].UserPrompt != "item-3" {
		t.Errorf("the last call is not the item alone: %q", requests[2].UserPrompt)
	}
	third := result.Items[2]
	if third.Mode != schemaflux.ModeAtomicFallback || third.Value.Length != len("item-3") {
		t.Errorf("third item: mode %q, length %d", third.Mode, third.Value.Length)
	}
	if metrics := result.Metrics(); metrics.AtomicFallbacks != 1 {
		t.Errorf("metrics report %d atomic fallbacks, want 1", metrics.AtomicFallbacks)
	}
}

// An item the cascade cannot resolve is reported as failed beside the others,
// and the call does not fail: the result is what says what happened.
func TestRunOpManyRecoverReportsAnItemItCouldNotResolveAndStillSucceeds(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		offered := offeredItems(t, req)
		if offered == nil {
			if req.UserPrompt == "item-3" {
				return "", errors.New("the provider could not serve this item")
			}
			return fmt.Sprintf(`{"length":%d}`, len(req.UserPrompt)), nil
		}
		var elements []string
		for _, o := range offered {
			if o.Item == "item-3" {
				continue
			}
			elements = append(elements, fmt.Sprintf(`{"id":%q,"output":{"length":%d}}`, o.ID, len(o.Item)))
		}
		return `{"items":[` + strings.Join(elements, ",") + `]}`, nil
	})
	schemafluxtest.Install(t, provider)
	op, codec := lengthOp()

	result, err := schemaflux.RunOpManyRecover(context.Background(), op, fiveItems, codec, schemaflux.OpOptions{},
		schemaflux.RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil {
		t.Fatalf("RunOpManyRecover returned %v: an unresolved item is for the result to report", err)
	}
	if result.Complete() {
		t.Fatal("a run with an unresolved item reports itself complete")
	}
	if result.Summary.Succeeded != 4 || result.Summary.Failed != 1 {
		t.Errorf("summary %+v, want four succeeded and one failed", result.Summary)
	}
	failures := result.Failures()
	if len(failures) != 1 || failures[0].Index != 2 || failures[0].Err == nil {
		t.Fatalf("Failures() = %v, want only the third item, with its error", failures)
	}
	if strings.Contains(failures[0].Err.Error(), "item-3") {
		t.Errorf("the failed item's error %q holds the item", failures[0].Err.Error())
	}
}

// --- What the two runs report beside their items -----------------------------

// A nil context is refused by both runs as an invalid request, and nothing is
// sent: neither reads it as Background.
func TestPartialAndRecoveringRunsRefuseANilContext(t *testing.T) {
	provider := providerRefusing("")
	schemafluxtest.Install(t, provider)
	op, codec := lengthOp()

	var noContext context.Context
	partial, err := schemaflux.RunOpManyPartial(noContext, echoOp(), fiveItems, schemaflux.OpOptions{},
		schemaflux.PartialConfig{Policy: schemaflux.PolicyCollectFailures})
	if schemaflux.KindOf(err) != types.KindInvalidRequest || partial.Items != nil {
		t.Errorf("partial: err %v, items %v; want an invalid request and nothing beside it", err, partial.Items)
	}
	recovered, err := schemaflux.RunOpManyRecover(noContext, op, fiveItems, codec, schemaflux.OpOptions{}, schemaflux.RecoverConfig{})
	if schemaflux.KindOf(err) != types.KindInvalidRequest || recovered.Items != nil {
		t.Errorf("recover: err %v, items %v; want an invalid request and nothing beside it", err, recovered.Items)
	}
	if got := len(provider.Requests()); got != 0 {
		t.Errorf("%d requests were sent", got)
	}
}

// BatchResult.Total is what every request of the run cost, and it agrees with the
// number of requests the provider was sent: the batch, the batch asked again for
// the item that was bad, and the call of its own.
func TestRecoveringRunReportsWhatItCostInTotal(t *testing.T) {
	provider := providerWithABadElement(t, "item-3", 2)
	schemafluxtest.Install(t, provider)
	op, codec := lengthOp()

	result, err := schemaflux.RunOpManyRecover(context.Background(), op, fiveItems, codec, schemaflux.OpOptions{},
		schemaflux.RecoverConfig{MaxItemsPerCall: 5, StartItemsPerCall: 5})
	if err != nil || !result.Complete() {
		t.Fatalf("RunOpManyRecover: %v, %s", err, result)
	}
	requests := len(provider.Requests())
	if requests != 3 {
		t.Fatalf("%d requests, want the batch, the isolate pass and the item's own call", requests)
	}
	if result.Total.Attempts != requests {
		t.Errorf("Total.Attempts = %d, the provider was sent %d requests", result.Total.Attempts, requests)
	}
	if result.Total.Operation != op.ID.String() || result.Total.Strategy != schemaflux.ShapeMDSP.String() {
		t.Errorf("Total = %s via %s", result.Total.Operation, result.Total.Strategy)
	}
}

// A run that collects its failures reports them in Total as well: the requests
// that failed were made, and the partial run's Total counts each one.
func TestPartialRunReportsWhatItCostInTotal(t *testing.T) {
	provider := providerRefusing(refusedItem)
	schemafluxtest.Install(t, provider)

	result, err := schemaflux.RunOpManyPartial(context.Background(), echoOp(), fiveItems, schemaflux.OpOptions{},
		schemaflux.PartialConfig{Policy: schemaflux.PolicyCollectFailures, Concurrency: 1})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	if got := len(provider.Requests()); result.Total.Attempts != got || got != 5 {
		t.Errorf("Total.Attempts = %d, requests = %d, want both 5", result.Total.Attempts, got)
	}
	if result.Total.Strategy != schemaflux.ShapeAtomic.String() {
		t.Errorf("Strategy = %q", result.Total.Strategy)
	}
}

// A model that drops one answer and renumbers the rest from the first id, which
// moves every later answer onto the item before it when ids run in a sequence, moves
// nothing: the ids the library offers are random and new for every request, so the
// renumbered ones answer no item, and each item is answered by a call of its own with
// the length of its own name.
func TestRunOpManyRecoverDoesNotShiftAnAnswerOntoAnotherItemWhenAModelRenumbersItsIDs(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		offered := offeredItems(t, req)
		if offered == nil {
			return fmt.Sprintf(`{"length":%d}`, len(req.UserPrompt)), nil
		}
		var elements []string
		for place, o := range offered {
			if place == 2 {
				continue // the third answer is dropped
			}
			elements = append(elements, fmt.Sprintf(`{"id":"i-%06d","output":{"length":%d}}`, len(elements)+1, len(o.Item)))
		}
		return `{"items":[` + strings.Join(elements, ",") + `]}`, nil
	})
	schemafluxtest.Install(t, provider)
	op, codec := lengthOp()
	items := []string{"a", "bb", "ccc", "dddd", "eeeee", "ffffff"}

	result, err := schemaflux.RunOpManyRecover(context.Background(), op, items, codec, schemaflux.OpOptions{}, schemaflux.RecoverConfig{})
	if err != nil {
		t.Fatalf("RunOpManyRecover: %v", err)
	}
	for i, item := range result.Items {
		if item.Status == schemaflux.ItemSucceeded && item.Value.Length != len(items[i]) {
			t.Errorf("item %d holds the answer %d, which is another item's (its own is %d): a wrong answer returned as a success", i, item.Value.Length, len(items[i]))
		}
		if item.Mode == schemaflux.ModeMDSP {
			t.Errorf("item %d was taken from a batched answer whose ids nobody offered", i)
		}
	}
	if !result.Complete() {
		t.Errorf("Summary = %+v, want every item answered by a call of its own", result.Summary)
	}
}

// Nothing a vendor said is in an item's error, through the root.
func TestPartialRunItemErrorsDoNotCarryTheVendorsWords(t *testing.T) {
	provider := schemafluxtest.New().ReplyFunc(func(_ int, req schemaflux.CompletionRequest) (string, error) {
		return "", errors.New("upstream said CANARY-VENDOR quota for " + req.UserPrompt)
	})
	schemafluxtest.Install(t, provider)

	result, err := schemaflux.RunOpManyPartial(context.Background(), echoOp(), fiveItems, schemaflux.OpOptions{},
		schemaflux.PartialConfig{Policy: schemaflux.PolicyCollectFailures})
	if err != nil {
		t.Fatalf("RunOpManyPartial: %v", err)
	}
	for i, item := range result.Items {
		if item.Err == nil || strings.Contains(item.Err.Error(), "CANARY") || strings.Contains(item.Err.Error(), "upstream said") {
			t.Fatalf("item %d: %v", i, item.Err)
		}
		if want := fmt.Sprintf("item %d of 5", i+1); !strings.Contains(item.Err.Error(), want) {
			t.Errorf("item %d's error %q does not say %q", i, item.Err, want)
		}
	}
}
