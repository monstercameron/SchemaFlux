package ops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/llm"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// What the session is across turns: cancelled, used twice at once, resumed from a
// history somebody saved, run for a long time, and on a provider that cannot do
// what it asks.

// holdingProvider answers when released and gives up when its context ends.
type holdingProvider struct {
	mu       sync.Mutex
	once     sync.Once
	release  chan struct{}
	started  chan struct{}
	requests int
	reply    llm.CompletionResponse
}

func newHoldingProvider(reply llm.CompletionResponse) *holdingProvider {
	return &holdingProvider{release: make(chan struct{}), started: make(chan struct{}, 8), reply: reply}
}

func (p *holdingProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
		resp := p.reply
		resp.Model, resp.Provider = req.Model, "local"
		return resp, nil
	case <-ctx.Done():
		return llm.CompletionResponse{}, ctx.Err()
	}
}
func (p *holdingProvider) Name() string                               { return "local" }
func (p *holdingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *holdingProvider) RetryPolicy() (int, time.Duration)          { return 0, 0 }

// open lets every request that is waiting, and every later one, answer.
func (p *holdingProvider) open() { p.once.Do(func() { close(p.release) }) }

func (p *holdingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

func holdingSession(reply llm.CompletionResponse) (*Session, *holdingProvider, context.Context) {
	provider := newHoldingProvider(reply)
	s := &Session{Tools: []llm.Tool{{Name: "get_weather"}}}
	return s, provider, WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: "chat-model"})
}

// settledGoroutines waits for the goroutines the test started to end and returns
// the count, which is what a turn that leaks one shows up in.
func settledGoroutines(want int) int {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

func TestACancelledTurnLeavesTheHistoryAsItWasAndLeaksNothing(t *testing.T) {
	for _, entry := range []string{"Send", "SendResults"} {
		t.Run(entry, func(t *testing.T) {
			s, provider, ctx := holdingSession(llm.CompletionResponse{Content: "done", FinishReason: "stop"})
			if entry == "SendResults" {
				s.History = []llm.Message{userMsg("Oslo?"), asstCalls(weatherCall("call_1"))}
			} else {
				s.History = []llm.Message{userMsg("Oslo?"), asstMsg("7C")}
			}
			before := cloneHistory(s.History)
			goroutines := runtime.NumGoroutine()

			cancelCtx, cancel := context.WithCancel(ctx)
			errs := make(chan error, 1)
			go func() {
				var err error
				if entry == "Send" {
					_, err = s.Send(cancelCtx, "And Rome?")
				} else {
					_, err = s.SendResults(cancelCtx, map[string]string{"call_1": "7C"})
				}
				errs <- err
			}()
			<-provider.started // the request is in flight
			cancel()

			select {
			case err := <-errs:
				if types.KindOf(err) != types.KindCanceled && !errors.Is(err, context.Canceled) {
					t.Errorf("err = %v, want the turn cancelled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the turn did not return after its context was cancelled")
			}
			if !reflect.DeepEqual(s.History, before) {
				t.Errorf("History changed to %+v", s.History)
			}
			if got := settledGoroutines(goroutines); got > goroutines {
				t.Errorf("%d goroutines after the turn, %d before it: one outlived its call", got, goroutines)
			}

			// The same call can be made again, and the guard was released.
			provider.open()
			var err error
			if entry == "Send" {
				_, err = s.Send(ctx, "And Rome?")
			} else {
				_, err = s.SendResults(ctx, map[string]string{"call_1": "7C"})
			}
			if err != nil {
				t.Fatalf("the turn could not be made again: %v", err)
			}
			if n := len(s.History); n != len(before)+2 {
				t.Errorf("History has %d messages after the second try", n)
			}
		})
	}

	t.Run("a context that was over before the turn", func(t *testing.T) {
		s, provider, ctx := holdingSession(llm.CompletionResponse{Content: "done"})
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := s.Send(cancelCtx, "hi")
		if types.KindOf(err) != types.KindCanceled || provider.count() != 0 || s.History != nil {
			t.Errorf("err = %v, %d requests, History %+v", err, provider.count(), s.History)
		}
	})
}

// A second call while one is in flight is refused at once and never interleaves
// with it.
func TestASecondTurnWhileOneIsInFlightIsRefused(t *testing.T) {
	s, provider, ctx := holdingSession(llm.CompletionResponse{Content: "first answer", FinishReason: "stop"})
	first := make(chan error, 1)
	go func() {
		_, err := s.Send(ctx, "first")
		first <- err
	}()
	<-provider.started

	refusals := []struct {
		name string
		do   func() error
	}{
		{"Send", func() error { _, err := s.Send(ctx, "second"); return err }},
		{"SendResults", func() error { _, err := s.SendResults(ctx, map[string]string{"x": "y"}); return err }},
	}
	for _, r := range refusals {
		started := time.Now()
		done := make(chan error, 1)
		go func() { done <- r.do() }()
		var err error
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			// It was let in and is waiting on the provider, which is the
			// interleaving the guard is for. Let it finish so the test can end.
			t.Errorf("%s while one is running did not refuse at once", r.name)
			provider.open()
			err = <-done
		}
		if types.KindOf(err) != types.KindInvalidRequest {
			t.Errorf("%s while one is running: err = %v, want an invalid request", r.name, err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("%s waited %v: it has to refuse at once", r.name, elapsed)
		}
		if err != nil && !strings.Contains(err.Error(), "already running") {
			t.Errorf("%s: %q does not say why", r.name, err.Error())
		}
	}
	if provider.count() != 1 {
		t.Errorf("%d requests were sent, want the one that was in flight", provider.count())
	}

	provider.open()
	if err := <-first; err != nil {
		t.Fatalf("the turn that was running failed: %v", err)
	}
	want := []llm.Message{userMsg("first"), asstMsg("first answer")}
	if !reflect.DeepEqual(s.History, want) {
		t.Errorf("History = %+v, want only the turn that ran", s.History)
	}
	// The guard is released when the turn ends, whatever the turn did.
	if _, err := s.Send(ctx, "third"); err != nil {
		t.Errorf("a turn after the first ended was refused: %v", err)
	}
}

func TestManyTurnsAtOnceMakeOneConversationOfTheOneThatWasAdmitted(t *testing.T) {
	s, provider, ctx := holdingSession(llm.CompletionResponse{Content: "ok", FinishReason: "stop"})
	const callers = 16
	results := make(chan error, callers)
	for range callers {
		go func() {
			_, err := s.Send(ctx, "go")
			results <- err
		}()
	}
	<-provider.started // one is in flight, and the rest have to be refused while it is

	refused := 0
	for refused < callers-1 {
		select {
		case err := <-results:
			if types.KindOf(err) != types.KindInvalidRequest {
				t.Errorf("a turn that arrived while one was running: err = %v, want an invalid request", err)
			}
			refused++
			continue
		case <-time.After(5 * time.Second):
			t.Errorf("only %d of the %d turns that arrived while one was in flight were refused", refused, callers-1)
		}
		break
	}
	provider.open()
	if err := <-results; err != nil {
		t.Errorf("the turn that was admitted failed: %v", err)
	}
	if got := provider.count(); got != 1 {
		t.Errorf("%d requests were sent for one admitted turn", got)
	}
	if want := []llm.Message{userMsg("go"), asstMsg("ok")}; !reflect.DeepEqual(s.History, want) {
		t.Errorf("History = %+v, want the one turn that ran", s.History)
	}
}

// A session's defaults are the tier Smart and the mode Transform, and what it is
// given is what the request says.
func TestASessionsDefaultsAndSettingsReachTheRequest(t *testing.T) {
	cases := []struct {
		name  string
		apply func(s *Session)
		check func(t *testing.T, req llm.CompletionRequest)
	}{
		{"the defaults", func(*Session) {}, func(t *testing.T, req llm.CompletionRequest) {
			if req.MaxTokens != config.GetMaxTokens(types.Smart) {
				t.Errorf("max tokens = %d, want the smart tier's %d", req.MaxTokens, config.GetMaxTokens(types.Smart))
			}
			if req.Temperature != float64(config.GetTemperature(types.TransformMode)) {
				t.Errorf("temperature = %v, want transform's", req.Temperature)
			}
			if req.ReasoningEffort != "" || req.WebSearch || req.ResponseFormat != "text" {
				t.Errorf("effort %q, web search %v, format %q", req.ReasoningEffort, req.WebSearch, req.ResponseFormat)
			}
		}},
		{"the fast tier", func(s *Session) { s.Tier = types.Fast }, func(t *testing.T, req llm.CompletionRequest) {
			if req.MaxTokens != config.GetMaxTokens(types.Fast) {
				t.Errorf("max tokens = %d, want the fast tier's", req.MaxTokens)
			}
		}},
		{"the quick tier", func(s *Session) { s.Tier = types.Quick }, func(t *testing.T, req llm.CompletionRequest) {
			if req.MaxTokens != config.GetMaxTokens(types.Quick) {
				t.Errorf("max tokens = %d, want the quick tier's", req.MaxTokens)
			}
		}},
		{"a creative mode", func(s *Session) { s.Mode = types.Creative }, func(t *testing.T, req llm.CompletionRequest) {
			if req.Temperature != float64(config.GetTemperature(types.Creative)) {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"a strict mode", func(s *Session) { s.Mode = types.Strict }, func(t *testing.T, req llm.CompletionRequest) {
			if req.Temperature != float64(config.GetTemperature(types.Strict)) {
				t.Errorf("temperature = %v", req.Temperature)
			}
		}},
		{"a ceiling", func(s *Session) { s.MaxOutputTokens = 77 }, func(t *testing.T, req llm.CompletionRequest) {
			if req.MaxTokens != 77 {
				t.Errorf("max tokens = %d", req.MaxTokens)
			}
		}},
		{"a pinned model", func(s *Session) { s.Model = "pinned-model" }, func(t *testing.T, req llm.CompletionRequest) {
			if req.Model != "pinned-model" {
				t.Errorf("model = %q", req.Model)
			}
		}},
		{"an effort", func(s *Session) { s.Effort = types.EffortHigh }, func(t *testing.T, req llm.CompletionRequest) {
			if req.ReasoningEffort != "high" || req.ReasoningEffortPreferred {
				t.Errorf("effort %q preferred %v", req.ReasoningEffort, req.ReasoningEffortPreferred)
			}
		}},
		{"web search", func(s *Session) { s.WebSearch = true }, func(t *testing.T, req llm.CompletionRequest) {
			if !req.WebSearch {
				t.Error("web search is not declared")
			}
		}},
		{"no instructions", func(s *Session) { s.Instructions = "" }, func(t *testing.T, req llm.CompletionRequest) {
			if req.SystemPrompt != "" {
				t.Errorf("system = %q", req.SystemPrompt)
			}
		}},
		{"no tools", func(s *Session) { s.Tools = nil }, func(t *testing.T, req llm.CompletionRequest) {
			if len(req.Tools) != 0 {
				t.Errorf("tools = %+v", req.Tools)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, provider, ctx := chatSession(t, said("ok"))
			tc.apply(s)
			if _, err := s.Send(ctx, "hi"); err != nil {
				t.Fatalf("Send: %v", err)
			}
			tc.check(t, provider.sent()[0])
		})
	}
}

// One record per turn, and the running total is their sum.
func TestASessionsTurnsEachHaveARecordAndTheTotalIsTheirSum(t *testing.T) {
	usage := func(in, out int) types.TokenUsage {
		return types.TokenUsage{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out, CachedTokens: in / 2}
	}
	s, _, ctx := chatSession(t,
		chatStep{resp: llm.CompletionResponse{ToolCalls: []llm.ToolCall{weatherCall("call_1")}, FinishReason: "tool_calls", Usage: usage(100, 10), ServiceTier: "priority"}},
		chatStep{resp: llm.CompletionResponse{Content: "7C", FinishReason: "stop", Usage: usage(150, 12)}},
		chatStep{resp: llm.CompletionResponse{Content: "bye", FinishReason: "stop", Usage: usage(200, 4)}},
	)
	var metas []types.Meta
	r1, err := s.Send(ctx, "Oslo?")
	if err != nil {
		t.Fatal(err)
	}
	metas = append(metas, r1.Meta)
	r2, err := s.SendResults(ctx, map[string]string{"call_1": "7C"})
	if err != nil {
		t.Fatal(err)
	}
	metas = append(metas, r2.Meta)
	r3, err := s.Send(ctx, "thanks")
	if err != nil {
		t.Fatal(err)
	}
	metas = append(metas, r3.Meta)

	ids := map[string]bool{}
	for i, m := range metas {
		if m.RequestID == "" || ids[m.RequestID] {
			t.Errorf("turn %d has request id %q: each turn is one request of its own", i+1, m.RequestID)
		}
		ids[m.RequestID] = true
		if m.Attempts != 1 || m.Model != "chat-model" || m.Provider != "local" || m.Usage.TotalTokens == 0 || m.Elapsed < 0 {
			t.Errorf("turn %d record = %+v", i+1, m)
		}
		if m.RequestedContract != types.ContractPromptOnly || m.DeliveredContract != types.ContractPromptOnly {
			t.Errorf("turn %d contract = %v / %v, want prompt only", i+1, m.RequestedContract, m.DeliveredContract)
		}
	}
	if metas[0].ServiceTier != "priority" {
		t.Errorf("the service tier was lost: %q", metas[0].ServiceTier)
	}
	total := s.Total()
	if total.Usage.PromptTokens != 450 || total.Usage.CompletionTokens != 26 || total.Usage.TotalTokens != 476 || total.Usage.CachedTokens != 225 {
		t.Errorf("total usage = %+v, want the sum of the three turns", total.Usage)
	}
	if total.Attempts != 3 || total.Operation != "chat" || total.RequestID != "" {
		t.Errorf("total = %+v", total)
	}
	if total.Model != "chat-model" || total.Provider != "local" {
		t.Errorf("total model %q provider %q, want the one every turn agrees on", total.Model, total.Provider)
	}
}

// A provider that cannot do tools makes the turn fail with the unsupported kind,
// and nothing is recorded in the history.
func TestATurnOnAProviderThatCannotDoToolsIsRefusedAsUnsupported(t *testing.T) {
	cases := []struct {
		name  string
		tools bool
		prior bool
	}{
		{"tools", true, false},
		{"tools in a conversation that has history", true, true},
		{"a conversation with no tools", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &refusingProvider{}
			s := &Session{}
			if tc.tools {
				s.Tools = []llm.Tool{{Name: "get_weather"}}
			}
			if tc.prior {
				s.History = []llm.Message{userMsg("a"), asstMsg("b")}
			}
			before := cloneHistory(s.History)
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: provider, Model: "chat-model"})

			_, err := s.Send(ctx, "hi")
			if !errors.Is(err, types.ErrUnsupportedCapability) {
				t.Fatalf("err = %v, want the unsupported kind", err)
			}
			if !reflect.DeepEqual(s.History, before) {
				t.Errorf("History changed to %+v", s.History)
			}
			if provider.sentOnTheWire != 0 {
				t.Errorf("%d requests reached the wire", provider.sentOnTheWire)
			}
		})
	}
}

// refusingProvider is a provider that, like the built-in ones that cannot carry
// tools or a conversation, refuses a request that sets them before anything is
// sent.
type refusingProvider struct{ sentOnTheWire int }

func (p *refusingProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if err := llm.RefuseUnsupportedFields(req, "refuser", "Tools", "Messages"); err != nil {
		return llm.CompletionResponse{}, err
	}
	p.sentOnTheWire++
	return llm.CompletionResponse{Content: "ok"}, nil
}
func (p *refusingProvider) Name() string                               { return "refuser" }
func (p *refusingProvider) EstimateCost(llm.CompletionRequest) float64 { return 0 }
func (p *refusingProvider) RetryPolicy() (int, time.Duration)          { return 2, time.Microsecond }

// A conversation that outgrows the model's window is refused with the too-large
// kind before it is sent, and one cut to fit is sent.
func TestAConversationThatOutgrowsTheWindowIsRefusedUntilItIsCut(t *testing.T) {
	llm.ResetCapabilityRegistryForTest()
	t.Cleanup(llm.ResetCapabilityRegistryForTest)
	llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "local", Model: "chat-model", ContextWindow: 3000})

	paragraph := strings.Repeat("a long paragraph of what was said ", 30) // about 1,000 bytes, 250 tokens
	steps := make([]chatStep, 40)
	for i := range steps {
		steps[i] = said(paragraph)
	}
	s, provider, ctx := chatSession(t, steps...)
	s.MaxOutputTokens = 200

	turns := 0
	var refusal error
	for ; turns < 40; turns++ {
		if _, err := s.Send(ctx, paragraph); err != nil {
			refusal = err
			break
		}
	}
	if refusal == nil {
		t.Fatalf("40 turns of 500 tokens each fit a window of 3,000")
	}
	if kind := types.KindOf(refusal); kind != types.KindContextTooLarge || !errors.Is(refusal, types.ErrContextTooLarge) {
		t.Fatalf("err = %v, want the context-too-large kind", refusal)
	}
	if !strings.HasPrefix(refusal.Error(), "chat.send") {
		t.Errorf("error %q does not start with the operation", refusal.Error())
	}
	if provider.count() != turns {
		t.Errorf("%d requests for %d turns that fit: the refused one was sent", provider.count(), turns)
	}
	if len(s.History) != 2*turns {
		t.Errorf("History has %d messages after %d turns and a refusal", len(s.History), turns)
	}
	if turns < 2 {
		t.Errorf("only %d turns fit, the window is large enough for more", turns)
	}

	// The caller's way out is to cut the history, and the same call goes.
	s.History = s.History[len(s.History)-2:]
	if _, err := s.Send(ctx, paragraph); err != nil {
		t.Fatalf("the shortened conversation was refused: %v", err)
	}
}

// A history a caller saved and brought back is held to the rules every time, and
// the library never writes into the slice it was given.
func TestAHistoryASaverSuppliedIsCheckedOnEveryCall(t *testing.T) {
	call := weatherCall("call_1")
	s, provider, ctx := chatSession(t, asked(call), said("7C."), said("fine"), said("fine"))
	if _, err := s.Send(ctx, "Oslo?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendResults(ctx, map[string]string{"call_1": "7C"}); err != nil {
		t.Fatal(err)
	}

	// Saved and restored through JSON, as a caller keeps a conversation.
	saved, err := json.Marshal(s.History)
	if err != nil {
		t.Fatal(err)
	}
	var restored []llm.Message
	if err := json.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	resumed := &Session{Tools: s.Tools, Instructions: s.Instructions, History: restored}
	if _, err := resumed.Send(ctx, "And Rome?"); err != nil {
		t.Fatalf("a restored conversation could not go on: %v", err)
	}
	if len(provider.sent()[2].Messages) != 5 {
		t.Errorf("the restored history was not sent whole: %d messages", len(provider.sent()[2].Messages))
	}

	// Edited between turns into something no conversation is: refused, untouched.
	resumed.History[2].ToolCallID = "call_9"
	before := cloneHistory(resumed.History)
	sentBefore := provider.count()
	_, err = resumed.Send(ctx, "Lima?")
	if !errors.Is(err, ErrSessionInvariant) || types.KindOf(err) != types.KindInvalidRequest {
		t.Fatalf("err = %v, want the invalid history refused", err)
	}
	if provider.count() != sentBefore || !reflect.DeepEqual(resumed.History, before) {
		t.Error("a refused history was sent or changed")
	}
	resumed.History[2].ToolCallID = "call_1"
	resumed.History = append(resumed.History, llm.Message{Role: "system", Content: "x"})
	if _, err = resumed.Send(ctx, "Lima?"); !errors.Is(err, ErrSessionInvariant) {
		t.Errorf("a system message in the history was accepted: %v", err)
	}
}

func TestASessionNeverWritesIntoTheSliceItWasGivenOrHandsBackItsOwn(t *testing.T) {
	call := weatherCall("call_1")
	s, _, ctx := chatSession(t, said("one"), asked(call), said("two"))

	// A slice with room to spare: appending to it in place would write into the
	// array the caller still holds.
	backing := make([]llm.Message, 2, 16)
	backing[0], backing[1] = userMsg("earlier"), asstMsg("earlier answer")
	s.History = backing

	if _, err := s.Send(ctx, "now"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backing[:3][2], llm.Message{}) {
		t.Errorf("the session wrote %+v into the array of the slice it was given", backing[:3][2])
	}
	if len(s.History) != 4 {
		t.Fatalf("History has %d messages", len(s.History))
	}

	// The reply is a copy: changing it does not change the history.
	reply, err := s.Send(ctx, "weather?")
	if err != nil || len(reply.Value.ToolCalls) != 1 {
		t.Fatalf("reply = %+v, %v", reply.Value, err)
	}
	reply.Value.ToolCalls[0].Name = "changed"
	reply.Value.ToolCalls[0].Arguments[0] = 'X'
	reply.Value.Content = "changed"
	kept := s.History[len(s.History)-1]
	if kept.ToolCalls[0].Name != "get_weather" || string(kept.ToolCalls[0].Arguments) != `{"city":"Oslo"}` || kept.Content != "" {
		t.Errorf("changing the reply changed the history: %+v", kept)
	}
	if err := CheckTranscript(s.History); err != nil {
		t.Errorf("the history is no longer valid: %v", err)
	}
}

// Twenty rounds of a tool loop: the goroutines do not grow and the total is the
// sum of the turns.
func TestATwentyRoundToolLoopLeaksNothingAndTotalsEveryTurn(t *testing.T) {
	steps := make([]chatStep, 0, 40)
	for i := 0; i < 20; i++ {
		steps = append(steps,
			chatStep{resp: llm.CompletionResponse{ToolCalls: []llm.ToolCall{weatherCall("call_" + string(rune('a'+i)))}, FinishReason: "tool_calls", Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}},
		)
	}
	steps = append(steps, chatStep{resp: llm.CompletionResponse{Content: "done", FinishReason: "stop", Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}})
	s, provider, ctx := chatSession(t, steps...)
	goroutines := runtime.NumGoroutine()

	reply, err := s.Send(ctx, "Keep asking.")
	rounds := 0
	for err == nil && len(reply.Value.ToolCalls) > 0 {
		results := map[string]string{}
		for _, call := range reply.Value.ToolCalls {
			results[call.ID] = "7C"
		}
		rounds++
		reply, err = s.SendResults(ctx, results)
	}
	if err != nil || reply.Value.Content != "done" || rounds != 20 {
		t.Fatalf("after %d rounds: reply %+v, err %v", rounds, reply.Value, err)
	}
	if got := provider.count(); got != 21 {
		t.Errorf("%d requests, want 21", got)
	}
	if total := s.Total(); total.Attempts != 21 || total.Usage.TotalTokens != 21*12 {
		t.Errorf("total = %d attempts, %d tokens, want 21 and %d", total.Attempts, total.Usage.TotalTokens, 21*12)
	}
	if got := settledGoroutines(goroutines); got > goroutines {
		t.Errorf("%d goroutines after the loop, %d before it", got, goroutines)
	}
	if want := 1 + 20*2 + 1; len(s.History) != want {
		t.Errorf("History has %d messages, want %d", len(s.History), want)
	}
	if err := CheckTranscript(s.History); err != nil {
		t.Errorf("the history of the loop is not valid: %v", err)
	}
}

// A session is a turn that delivers the model's own text, so a client that
// requires more of an answer is refused before anything is sent.
func TestASessionTurnIsRefusedUnderAContractFloorAbovePromptOnly(t *testing.T) {
	s, provider, ctx := chatSession(t, said("ok"))
	ctx = WithContractPolicy(ctx, types.DataPolicy{MinimumContract: types.ContractJSONWellFormed})
	result, err := s.Send(ctx, "hi")
	if !errors.Is(err, types.ErrPolicyViolation) {
		t.Fatalf("err = %v, want a policy refusal", err)
	}
	if provider.count() != 0 || s.History != nil {
		t.Errorf("%d requests were sent and History is %+v", provider.count(), s.History)
	}
	if result.Meta.Operation != "chat.send" || result.Meta.Attempts != 0 {
		t.Errorf("the record of a refused turn is %+v, want chat.send and no attempts", result.Meta)
	}
}

// The model's reply that cannot be used is recorded as the check that failed.
func TestARefusedReplyIsRecordedAsTheCheckThatFailed(t *testing.T) {
	s, _, ctx := chatSession(t, asked(llm.ToolCall{ID: "x", Name: "SECRET-NAME", Arguments: json.RawMessage(`{}`)}, weatherCall("y")))
	result, err := s.Send(ctx, "hi")
	if err == nil {
		t.Fatal("the reply was accepted")
	}
	var failed *types.Check
	for i := range result.Meta.Checks {
		if result.Meta.Checks[i].Name == "tool calls" {
			failed = &result.Meta.Checks[i]
		}
	}
	if failed == nil || failed.Passed || !strings.Contains(failed.Detail, "1 name a tool that was not declared") || strings.Contains(failed.Detail, "SECRET") {
		t.Errorf("checks = %+v", result.Meta.Checks)
	}
	if result.Meta.Attempts != 1 || result.Meta.Usage == (types.TokenUsage{}) && result.Meta.Attempts == 0 {
		t.Errorf("the record of a refused reply is %+v: the request was paid for", result.Meta)
	}
	if result.Value.Role != "" || result.Value.Content != "" || len(result.Value.ToolCalls) != 0 {
		t.Errorf("a message %+v came back beside an error", result.Value)
	}
}

// No text of the conversation, no call id, no tool name, no argument and no
// result is in any error a session returns, down every path that refuses.
func TestNoErrorOfASessionCarriesTheConversation(t *testing.T) {
	const marker = "SECRET-MARKER"
	bad := llm.ToolCall{ID: marker + "-ID", Name: marker + "-NAME", Arguments: json.RawMessage(`{"q":"` + marker + `-ARG"}`)}
	cases := []struct {
		name string
		run  func() error
	}{
		{"a history that is not valid", func() error {
			s, _, ctx := chatSession(t, said("x"))
			s.History = []llm.Message{userMsg(marker), userMsg(marker)}
			_, err := s.Send(ctx, marker)
			return err
		}},
		{"results that do not match", func() error {
			s, _, ctx := chatSession(t, said("x"))
			s.History = []llm.Message{userMsg(marker), asstCalls(bad)}
			_, err := s.SendResults(ctx, map[string]string{marker + "-WRONG": marker})
			return err
		}},
		{"results for calls already answered", func() error {
			s, _, ctx := chatSession(t, said("x"))
			s.History = []llm.Message{userMsg(marker), asstCalls(bad), toolMsg(bad.ID, marker)}
			_, err := s.SendResults(ctx, map[string]string{bad.ID: marker})
			return err
		}},
		{"a reply that asks for a tool nobody declared", func() error {
			s, _, ctx := chatSession(t, asked(bad))
			_, err := s.Send(ctx, marker)
			return err
		}},
		{"a reply whose arguments are not an object", func() error {
			call := llm.ToolCall{ID: marker, Name: "get_weather", Arguments: json.RawMessage(`"` + marker + `"`)}
			s, _, ctx := chatSession(t, asked(call))
			_, err := s.Send(ctx, marker)
			return err
		}},
		{"a conversation too large for the window", func() error {
			llm.ResetCapabilityRegistryForTest()
			t.Cleanup(llm.ResetCapabilityRegistryForTest)
			llm.RegisterCapabilities(llm.ProviderCapabilities{Provider: "local", Model: "chat-model", ContextWindow: 100})
			s, _, ctx := chatSession(t, said("x"))
			_, err := s.Send(ctx, strings.Repeat(marker, 400))
			return err
		}},
		{"a provider that cannot take tools", func() error {
			s := &Session{Tools: []llm.Tool{{Name: marker}}}
			ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Provider: &refusingProvider{}, Model: "chat-model"})
			_, err := s.Send(ctx, marker)
			return err
		}},
		{"a tool with no name", func() error {
			s, _, ctx := chatSession(t, said("x"))
			s.Tools = []llm.Tool{{Description: marker}}
			_, err := s.Send(ctx, marker)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), marker) {
				t.Errorf("the error carries the conversation: %q", err.Error())
			}
		})
	}
}
