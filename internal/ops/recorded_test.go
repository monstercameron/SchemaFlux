package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/runtime/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// recordedAnswer is what the calls of these tests extract: the answer of
// callerSuppliedProvider, {"ok":true}.
type recordedAnswer struct {
	Ok bool `json:"ok"`
}

// extractRecorded is an operation as the closure of Recorded builds it: its
// options carry the context the closure was given.
func extractRecorded(ctx context.Context) (recordedAnswer, error) {
	options := NewExtractOptions()
	options.CommonOptions.Context = ctx
	return Extract[recordedAnswer]("some text", options)
}

func TestRecordedBuildsOptionsFromTheLoggedContext(t *testing.T) {
	cases := []struct {
		name     string
		requests int
	}{
		{"one request", 1},
		{"two requests", 2},
		{"three requests", 3},
		{"five requests", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &callerSuppliedProvider{name: "recorded-provider", finishReason: "stop"}
			withDefaultExec(t, &ExecConfig{Provider: provider, Model: "recorded-model"})

			result, err := Recorded(context.Background(), "extract", types.ContractSchemaConstrained,
				func(ctx context.Context) (recordedAnswer, error) {
					var last recordedAnswer
					for range tc.requests {
						var err error
						if last, err = extractRecorded(ctx); err != nil {
							return last, err
						}
					}
					return last, nil
				})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Value.Ok {
				t.Error("the value did not come back")
			}
			meta := result.Meta
			if meta.Usage.TotalTokens != 14*tc.requests {
				t.Errorf("usage = %d tokens, want the %d requests' sum %d: the closure's options did not carry the log", meta.Usage.TotalTokens, tc.requests, 14*tc.requests)
			}
			if meta.Attempts != tc.requests {
				t.Errorf("attempts = %d, want %d", meta.Attempts, tc.requests)
			}
			if meta.Operation != "extract" || meta.Provider != "recorded-provider" || meta.Model != "recorded-model" {
				t.Errorf("identity = %q %q %q", meta.Operation, meta.Provider, meta.Model)
			}
			if meta.RequestID == "" {
				t.Error("no request id")
			}
			if meta.RequestedContract != types.ContractSchemaConstrained || meta.DeliveredContract != types.ContractSchemaConstrained {
				t.Errorf("contracts = %v / %v", meta.RequestedContract, meta.DeliveredContract)
			}
		})
	}
}

func TestRecordedRefusesBelowTheFloorBeforeRunning(t *testing.T) {
	levels := []types.ContractLevel{
		types.ContractPromptOnly, types.ContractJSONWellFormed, types.ContractSchemaConstrained,
		types.ContractSchemaAndInvariantChecked, types.ContractEvidenceChecked, types.ContractFullyGoverned,
	}
	for _, level := range levels {
		for _, floor := range levels {
			t.Run(level.String()+" against a floor of "+floor.String(), func(t *testing.T) {
				withDefaultExec(t, nil)
				ctx := WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: floor})
				ran := false
				result, err := Recorded(ctx, "transform", level, func(context.Context) (int, error) {
					ran = true
					return 7, nil
				})
				if level < floor {
					if err == nil {
						t.Fatal("a call that cannot meet the floor was not refused")
					}
					if ran {
						t.Error("the call was run though it could not meet the floor")
					}
					if types.KindOf(err) != types.KindPolicyViolation {
						t.Errorf("kind = %v, want a policy violation", types.KindOf(err))
					}
					if result.Value != 0 || result.Meta.Operation != "transform" || result.Meta.RequestedContract != level {
						t.Errorf("result = %+v", result)
					}
					return
				}
				if err != nil || !ran || result.Value != 7 {
					t.Errorf("a call at or above the floor: %v, ran %v, value %d", err, ran, result.Value)
				}
				if result.Meta.DeliveredContract != level {
					t.Errorf("delivered = %v, want %v", result.Meta.DeliveredContract, level)
				}
			})
		}
	}
}

// delayingProvider answers after a pause.
type delayingProvider struct {
	callerSuppliedProvider
	pause time.Duration
}

func (p *delayingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	time.Sleep(p.pause)
	return p.callerSuppliedProvider.Complete(ctx, req)
}

func TestRecordedElapsedIsTheWholeCall(t *testing.T) {
	const pause = 40 * time.Millisecond
	provider := &delayingProvider{callerSuppliedProvider: callerSuppliedProvider{name: "slow", finishReason: "stop"}, pause: pause}
	withDefaultExec(t, &ExecConfig{Provider: provider, Model: "slow-model"})

	// Three requests, one after another, as Filter makes them over three chunks.
	result, err := Recorded(context.Background(), "filter", types.ContractJSONWellFormed,
		func(ctx context.Context) (recordedAnswer, error) {
			var last recordedAnswer
			for range 3 {
				var err error
				if last, err = extractRecorded(ctx); err != nil {
					return last, err
				}
			}
			return last, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.Meta.Elapsed < 3*pause {
		t.Errorf("Elapsed = %v, want at least the three requests' %v: it is the last request's, not the call's", result.Meta.Elapsed, 3*pause)
	}
	if result.Meta.Attempts != 3 {
		t.Errorf("attempts = %d", result.Meta.Attempts)
	}

	t.Run("a refusal before running has an elapsed time too", func(t *testing.T) {
		ctx := WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: types.ContractFullyGoverned})
		refused, err := Recorded(ctx, "filter", types.ContractPromptOnly, func(context.Context) (int, error) { return 0, nil })
		if err == nil {
			t.Fatal("not refused")
		}
		if refused.Meta.Elapsed < 0 {
			t.Errorf("Elapsed = %v on a refusal", refused.Meta.Elapsed)
		}
	})
	t.Run("and a failure", func(t *testing.T) {
		failing := errors.New("the closure failed")
		failed, err := Recorded(context.Background(), "filter", types.ContractPromptOnly, func(context.Context) (int, error) {
			time.Sleep(20 * time.Millisecond)
			return 0, failing
		})
		if !errors.Is(err, failing) || failed.Meta.Elapsed < 20*time.Millisecond {
			t.Errorf("err %v, elapsed %v", err, failed.Meta.Elapsed)
		}
	})
}

func operationScopes(started []telemetry.OperationEvent, finished []telemetry.OperationResult) (starts, finishes int, finishKinds []string) {
	for _, event := range started {
		if event.Scope == telemetry.ScopeOperation {
			starts++
		}
	}
	for _, result := range finished {
		if result.Scope == telemetry.ScopeOperation {
			finishes++
			finishKinds = append(finishKinds, result.ErrorKind)
		}
	}
	return starts, finishes, finishKinds
}

func TestRecordedEmitsOneOperationScope(t *testing.T) {
	t.Run("a call that works", func(t *testing.T) {
		collector := installCollector(t)
		provider := &callerSuppliedProvider{name: "scoped", finishReason: "stop"}
		withDefaultExec(t, &ExecConfig{Provider: provider, Model: "scoped-model"})

		if _, err := Recorded(context.Background(), "extract", types.ContractSchemaConstrained, extractRecordedResult); err != nil {
			t.Fatal(err)
		}
		started, finished := collector.snapshot()
		starts, finishes, kinds := operationScopes(started, finished)
		if starts != 1 || finishes != 1 {
			t.Fatalf("operation scopes: %d starts, %d finishes, want one pair", starts, finishes)
		}
		if kinds[0] != "" {
			t.Errorf("the operation finished with kind %q on a success", kinds[0])
		}
		if len(started)-starts != 1 {
			t.Errorf("%d request starts, want 1: the request is the operation's child", len(started)-starts)
		}
		for _, event := range started {
			if event.Scope == telemetry.ScopeOperation && event.Operation != "extract" {
				t.Errorf("the operation scope is named %q", event.Operation)
			}
		}
	})
	t.Run("a refused answer finishes with its kind", func(t *testing.T) {
		collector := installCollector(t)
		provider := &callerSuppliedProvider{name: "scoped", finishReason: "length"}
		withDefaultExec(t, &ExecConfig{Provider: provider, Model: "scoped-model"})

		_, err := Recorded(context.Background(), "extract", types.ContractSchemaConstrained, extractRecordedResult)
		if err == nil {
			t.Fatal("a truncated answer was accepted")
		}
		started, finished := collector.snapshot()
		starts, finishes, kinds := operationScopes(started, finished)
		if starts != 1 || finishes != 1 {
			t.Fatalf("operation scopes: %d starts, %d finishes, want one pair", starts, finishes)
		}
		if kinds[0] == "" {
			t.Errorf("the operation finished with kind %q for %v", kinds[0], err)
		}
	})
	t.Run("a refusal before running still closes its scope", func(t *testing.T) {
		collector := installCollector(t)
		ctx := WithContractPolicy(context.Background(), types.DataPolicy{MinimumContract: types.ContractFullyGoverned})
		if _, err := Recorded(ctx, "transform", types.ContractPromptOnly, func(context.Context) (int, error) { return 0, nil }); err == nil {
			t.Fatal("not refused")
		}
		started, finished := collector.snapshot()
		starts, finishes, kinds := operationScopes(started, finished)
		if starts != 1 || finishes != 1 || kinds[0] == "" {
			t.Errorf("%d starts, %d finishes, kinds %v", starts, finishes, kinds)
		}
	})
	t.Run("with no observer there is none", func(t *testing.T) {
		provider := &callerSuppliedProvider{name: "scoped", finishReason: "stop"}
		withDefaultExec(t, &ExecConfig{Provider: provider, Model: "scoped-model"})
		if _, err := Recorded(context.Background(), "extract", types.ContractSchemaConstrained, extractRecordedResult); err != nil {
			t.Fatal(err)
		}
	})
}

func extractRecordedResult(ctx context.Context) (recordedAnswer, error) { return extractRecorded(ctx) }

func TestObservingAllocatesNothing(t *testing.T) {
	if Observing() {
		t.Skip("another test's observer is installed")
	}
	if got := testing.AllocsPerRun(200, func() { _ = Observing() }); got != 0 {
		t.Errorf("Observing allocates %v times with none installed, want 0", got)
	}
	collector := &collectingObserver{}
	remove := telemetry.AddObserver(collector)
	defer remove()
	if !Observing() {
		t.Fatal("Observing is false with an observer installed")
	}
	if got := testing.AllocsPerRun(200, func() { _ = Observing() }); got != 0 {
		t.Errorf("Observing allocates %v times with one installed, want 0", got)
	}
	remove()
	if Observing() {
		t.Error("Observing is true after the observer was removed")
	}
}
