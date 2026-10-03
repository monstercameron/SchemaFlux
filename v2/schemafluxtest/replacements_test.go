package schemafluxtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// The thirteen provider stubs that the tests of this repository wrote by hand,
// for what the old testing package could not script, are rewritten below on this
// package, to show that the package makes them unnecessary. Each test names the
// stub it replaces and the file it came from. Where one cannot be replaced
// whole, the test covers what can be and says what is missing: those are gaps in
// this package. The operations these stubs were written to test are not in the
// v2 root yet, so each test calls the provider as the library would, and calls
// an internal operation through a client where the stub's point was the call.

// chunkEchoProvider (tests/collection_integration_test.go): answers a chunk of
// items with the id of the first item in it.
func TestAChunkEchoProviderIsARespondFunction(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		start := strings.Index(req.User, "[")
		if start < 0 {
			return schemaflux.Response{Content: `{"ids":[]}`, FinishReason: "stop"}, nil
		}
		var chunk []struct {
			ID   string          `json:"id"`
			Item json.RawMessage `json:"item"`
		}
		if err := json.Unmarshal([]byte(req.User[start:]), &chunk); err != nil || len(chunk) == 0 {
			return schemaflux.Response{Content: `{"ids":[]}`, FinishReason: "stop"}, nil
		}
		kept, _ := json.Marshal(map[string][]string{"ids": {chunk[0].ID}})
		return schemaflux.Response{Content: string(kept), FinishReason: "stop"}, nil
	})
	cases := []struct{ user, want string }{
		{`items: [{"id":"a","item":{"sku":1}},{"id":"b","item":{"sku":2}}]`, `{"ids":["a"]}`},
		{`[{"id":"z","item":null}]`, `{"ids":["z"]}`},
		{`no items here`, `{"ids":[]}`},
		{`[]`, `{"ids":[]}`},
		{`[not json`, `{"ids":[]}`},
	}
	for i, tc := range cases {
		resp, err := model.Complete(t.Context(), schemaflux.Request{User: tc.user})
		if err != nil || resp.Content != tc.want || resp.FinishReason != "stop" {
			t.Errorf("case %d: %q %v, want %q", i, resp.Content, err, tc.want)
		}
	}
	if model.Calls() != len(cases) {
		t.Errorf("Calls = %d, want %d: it replaces the stub's own counter", model.Calls(), len(cases))
	}
}

// dx12MineProvider (tests/dx12_custom_provider_integration_test.go): a provider
// of an application's own, with no model for a tier, that records the models it
// is sent.
func TestACustomProviderWithNoModelOfItsOwnIsAFakeWithItsModelsHidden(t *testing.T) {
	t.Parallel()
	// A struct that embeds the interface has Complete and Name and nothing else,
	// so it has no ModelFor, as a provider written outside the library has none.
	type mine struct{ schemaflux.Provider }
	var model schemafluxtest.Provider
	model.SetName("mine")
	model.Reply(`{"q1":true}`)
	custom := mine{&model}
	if _, has := any(custom).(interface{ ModelFor(schemaflux.Tier) string }); has {
		t.Fatal("the wrapper still has the fake's models")
	}
	if custom.Name() != "mine" {
		t.Errorf("Name = %q", custom.Name())
	}

	// No model named anywhere: refused before anything is sent.
	bare := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: custom})
	_, err := ops.Is(bare, "a message", "Is this about billing?", types.OpOptions{})
	if err == nil {
		t.Fatal("a call that named no model, to a provider the library has no model for, was sent")
	}
	if model.Calls() != 0 {
		t.Errorf("the provider was sent %d requests", model.Calls())
	}

	// The client names one, then the call does.
	named := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: custom, Model: "my-model"})
	ok, err := ops.Is(named, "a message", "Is this about billing?", types.OpOptions{})
	if err != nil || !ok {
		t.Fatalf("with the client's model: %v %v", ok, err)
	}
	if _, err := ops.Is(bare, "a message", "Is this about billing?", types.OpOptions{Model: "call-model"}); err != nil {
		t.Fatalf("with the call's model: %v", err)
	}
	var seen []string
	for _, r := range model.Requests() {
		seen = append(seen, r.Model)
	}
	if !slices.Equal(seen, []string{"my-model", "call-model"}) {
		t.Errorf("the provider saw the models %v", seen)
	}
}

// dx12Provider (tests/dx12_result_integration_test.go): one body, an error, and
// a fixed usage, with every request kept.
func TestAProviderOfOneBodyAndAFixedUsageIsReplyAndSetUsage(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.Reply(`{"title":"Standup","body":"Ship it."}`)
	model.SetUsage(schemaflux.Usage{Input: 30, Output: 12, Total: 42})
	resp, err := model.Complete(t.Context(), schemaflux.Request{Model: "m", User: "a note about standup"})
	if err != nil || resp.Content != `{"title":"Standup","body":"Ship it."}` || resp.Model != "m" || resp.FinishReason != "stop" {
		t.Fatalf("%+v %v", resp, err)
	}
	if resp.Usage != (schemaflux.Usage{Input: 30, Output: 12, Total: 42}) {
		t.Errorf("usage %+v", resp.Usage)
	}
	if model.Calls() != 1 || model.Last().User != "a note about standup" {
		t.Errorf("calls %d, last %+v", model.Calls(), model.Last())
	}

	boom := errors.New("provider down")
	model.Fail(boom)
	if _, err := model.Complete(t.Context(), schemaflux.Request{}); !errors.Is(err, boom) {
		t.Errorf("the scripted error: %v", err)
	}
	if model.Calls() != 2 {
		t.Errorf("a failed request is counted: Calls = %d", model.Calls())
	}
}

// stagedProvider (tests/e2e_pipeline_test.go): the next scripted body for each
// call, and a failure at a chosen call.
func TestAStagedProviderIsRespondWithACallNumberAndRecordsAsATape(t *testing.T) {
	t.Parallel()
	errStageFailed := &schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "stage provider failed"}
	bodies := []string{`{"number":"INV-4417","vendor":"Northwind"}`, `{"headline":"Northwind invoice INV-4417"}`}
	const failAt = 3
	var model schemafluxtest.Provider
	model.Respond(func(call int, req schemaflux.Request) (schemaflux.Response, error) {
		if call+1 == failAt {
			return schemaflux.Response{}, errStageFailed
		}
		return schemaflux.Response{Content: bodies[min(call, len(bodies)-1)], Model: req.Model, FinishReason: "stop"}, nil
	})
	tape := schemafluxtest.Record(&model, t.TempDir())
	var got []string
	for range 4 {
		resp, err := tape.Complete(t.Context(), schemaflux.Request{Model: "fake-model"})
		if err != nil {
			got = append(got, "error")
			continue
		}
		got = append(got, resp.Content)
	}
	want := []string{bodies[0], bodies[1], "error", bodies[1]}
	if !slices.Equal(got, want) {
		t.Errorf("answers %v, want %v", got, want)
	}
	if model.Calls() != 4 {
		t.Errorf("Calls = %d", model.Calls())
	}

	// What the stage provider did is a cassette now: the same four answers, in
	// the same order, from a Player, with the failure of the third call as it
	// was classified.
	player := schemafluxtest.ReplayFrom(tape.Recorded()...)
	player.Lenient()
	for i := range 4 {
		resp, err := player.Complete(t.Context(), schemaflux.Request{})
		switch {
		case i == 2 && !errors.Is(err, schemaflux.ErrUnavailable):
			t.Errorf("call 3 replayed as %v", err)
		case i != 2 && (err != nil || resp.Content != want[i]):
			t.Errorf("call %d replayed %q, %v", i+1, resp.Content, err)
		}
	}
}

// flakyProvider (tests/e2e_repair_test.go): unusable answers first, then a good
// one.
func TestAFlakyProviderIsAReplyListAndItsRecordingIsACassette(t *testing.T) {
	t.Parallel()
	const good = `{"number":"INV-4417","vendor":"Northwind"}`
	var model schemafluxtest.Provider
	model.Reply("this is not JSON at all", good)
	tape := schemafluxtest.Record(&model, t.TempDir())
	var got []string
	for range 3 {
		resp, err := tape.Complete(t.Context(), schemaflux.Request{Model: "fake-model", User: "the raw document"})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, resp.Content)
	}
	if want := []string{"this is not JSON at all", good, good}; !slices.Equal(got, want) {
		t.Errorf("answers %v, want %v: the last body repeats", got, want)
	}
	if model.Calls() != 3 {
		t.Errorf("Calls = %d, which is the stub's callCount", model.Calls())
	}
	player := schemafluxtest.ReplayFrom(tape.Recorded()...)
	for i := range 3 {
		resp, err := player.Complete(t.Context(), schemaflux.Request{Model: "fake-model", User: "the raw document"})
		if err != nil || resp.Content != got[i] {
			t.Errorf("replay %d: %q %v", i+1, resp.Content, err)
		}
	}
}

// aheadProvider (tests/lat007_ahead_integration_test.go): answers each question
// after a delay of its own, either a body or an error, and records which
// questions were asked, which were answered and which were cancelled first.
//
// What a Respond function can do is the answer of each question and the list of
// those asked. It cannot do the rest, and that is a gap in this package, in two
// parts: (1) Respond is given the call number and the request and no context,
// so a script cannot see that its request was cancelled (the specification says a
// test "reads it in Respond", and the function has nowhere to read it from); (2)
// Delay is one wait for every request, so one question cannot be slower than
// another. The stub's tests of which questions were cancelled once an earlier
// one answered need both.
func TestAnAheadProviderCanAnswerByQuestionButNotDelayOrObserveCancellationByQuestion(t *testing.T) {
	t.Parallel()
	type scripted struct {
		body string
		err  error
	}
	answers := map[string]scripted{
		"is it a refund": {body: `{"q1":true}`},
		"is it urgent":   {err: &schemaflux.Error{Kind: schemaflux.ErrTimeout}},
	}
	var model schemafluxtest.Provider
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		for question, answer := range answers {
			if strings.Contains(req.User, `"`+question+`"`) {
				return schemaflux.Response{Content: answer.body, Model: req.Model}, answer.err
			}
		}
		return schemaflux.Response{}, errors.New("a question nobody scripted")
	})
	resp, err := model.Complete(t.Context(), schemaflux.Request{User: `{"question":"is it a refund"}`})
	if err != nil || resp.Content != `{"q1":true}` {
		t.Errorf("the refund question: %q %v", resp.Content, err)
	}
	if _, err := model.Complete(t.Context(), schemaflux.Request{User: `{"question":"is it urgent"}`}); !errors.Is(err, schemaflux.ErrTimeout) {
		t.Errorf("the urgent question: %v", err)
	}
	if _, err := model.Complete(t.Context(), schemaflux.Request{User: `{"question":"is it spam"}`}); err == nil {
		t.Error("a question nobody scripted was answered")
	}
	var asked []string
	for _, r := range model.Requests() {
		asked = append(asked, r.User)
	}
	if len(asked) != 3 {
		t.Errorf("the requests asked: %v", asked)
	}
}

// lx126Provider (tests/lx126_client_limits_integration_test.go): holds each call
// for a while, and counts how many are in flight at once and how many were made,
// for a call and for a stream.
//
// The count of concurrent calls is a Respond function that holds the call itself.
// What it cannot do is end its hold when the call's context ends, for the reason
// given at the test above: a Respond function has no context. A hold that ends
// with the context is Delay, which cannot count what is in flight while it waits.
func TestALimitsProviderCountsConcurrentCallsInRespondAndStreamsThroughIt(t *testing.T) {
	t.Parallel()
	const n = 6
	var inFlight, peak, calls atomic.Int64
	var allIn sync.WaitGroup
	allIn.Add(n)
	var model schemafluxtest.Provider
	model.Respond(func(call int, req schemaflux.Request) (schemaflux.Response, error) {
		now := inFlight.Add(1)
		defer inFlight.Add(-1)
		calls.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		// The first n calls hold until all n are in, so that they overlap
		// whatever the machine is doing; the stream's call after them does not.
		if call < n {
			allIn.Done()
			allIn.Wait()
		}
		return schemaflux.Response{Content: `{"category":"billing","confidence":0.9,"reasoning":"r"}`, Model: req.Model, FinishReason: "stop"}, nil
	})
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := model.Complete(t.Context(), schemaflux.Request{Model: "m"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak.Load() != n || calls.Load() != n {
		t.Errorf("peak %d, calls %d, want %d of each", peak.Load(), calls.Load(), n)
	}

	// A stream goes through the same script, and is counted the same way.
	var text strings.Builder
	for chunk, err := range model.Stream(t.Context(), schemaflux.Request{Model: "m"}) {
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(chunk.Delta)
	}
	if calls.Load() != n+1 || !strings.Contains(text.String(), "billing") {
		t.Errorf("calls %d, text %q", calls.Load(), text.String())
	}

	// The hold that ends with the context is Delay.
	var slow schemafluxtest.Provider
	slow.Delay(time.Hour)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := slow.Complete(ctx, schemaflux.Request{}); err == nil {
		t.Error("a held call outlived its context")
	}
}

// namedProvider (tests/lx126_routers_integration_test.go): a provider with a name
// of its own that fails or answers a classification, with a usage, and counts.
func TestANamedProviderIsSetNameFailAndReply(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.SetName("primary")
	model.SetUsage(schemaflux.Usage{Input: 20, Output: 10, Total: 30})
	model.Reply(`{"category":"billing","confidence":0.9,"reasoning":"r"}`)
	resp, err := model.Complete(t.Context(), schemaflux.Request{Model: "m"})
	if err != nil || model.Name() != "primary" || resp.Usage.Total != 30 || !strings.Contains(resp.Content, "billing") {
		t.Fatalf("%q %+v %v", model.Name(), resp, err)
	}
	// The stub reported its own name in the answer. An answer that names no
	// provider is the one that was asked, which is what this means.
	if resp.Provider != "" {
		t.Errorf("Response.Provider = %q", resp.Provider)
	}
	model.Fail(errors.New("down"))
	if _, err := model.Complete(t.Context(), schemaflux.Request{}); err == nil {
		t.Error("the failure did not happen")
	}
	if model.Calls() != 2 {
		t.Errorf("Calls = %d", model.Calls())
	}
	// Two fakes in two tests have two names, which is what a router test needs.
	var backup schemafluxtest.Provider
	backup.SetName("backup")
	if backup.Name() == model.Name() {
		t.Error("two named fakes have one name")
	}
}

// routeProvider (tests/lx_round11_integration_test.go): answers under a provider
// and a model of its choosing, or under none, or fails; remembers the model asked.
func TestARouteProviderIsRespondWithTheRouteItAnswersUnder(t *testing.T) {
	t.Parallel()
	body := `{"category":"billing","confidence":0.9,"reasoning":"r"}`
	cases := []struct {
		name          string
		provider      string
		model         string
		wantProvider  string
		wantModel     string
		namesNothing  bool
		failWith      error
		wantErrorKind schemaflux.Kind
	}{
		{name: "answers as itself", wantModel: "asked"},
		{name: "answers as a gateway", provider: "gateway", wantProvider: "gateway", wantModel: "asked"},
		{name: "answers from a dated snapshot", model: "asked-2026-01-01", wantModel: "asked-2026-01-01"},
		{name: "answers as another provider and model", provider: "other", model: "other-model", wantProvider: "other", wantModel: "other-model"},
		{name: "names nothing", namesNothing: true},
		{name: "fails", failWith: &schemaflux.Error{Kind: schemaflux.ErrUnavailable}, wantErrorKind: schemaflux.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.SetName("route")
			model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
				if tc.failWith != nil {
					return schemaflux.Response{}, tc.failWith
				}
				resp := schemaflux.Response{Content: body, Model: req.Model, Provider: tc.provider, FinishReason: "stop",
					Usage: schemaflux.Usage{Input: 20, Output: 10, Total: 30}}
				if tc.model != "" {
					resp.Model = tc.model
				}
				if tc.namesNothing {
					resp.Model, resp.Provider = "", ""
				}
				return resp, nil
			})
			resp, err := model.Complete(t.Context(), schemaflux.Request{Model: "asked"})
			if model.Last().Model != "asked" || model.Calls() != 1 {
				t.Errorf("the model asked for is %q after %d calls", model.Last().Model, model.Calls())
			}
			if tc.failWith != nil {
				if !errors.Is(err, tc.wantErrorKind) {
					t.Errorf("err = %v", err)
				}
				return
			}
			if err != nil || resp.Provider != tc.wantProvider || (!tc.namesNothing && resp.Model != tc.wantModel) || (tc.namesNothing && resp.Model != "") {
				t.Errorf("answered as %q/%q (%v), want %q/%q", resp.Provider, resp.Model, err, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

// r12bAnswerer (tests/lx_round12b_routes_integration_test.go): a provider with a
// name that fails or answers with a Response a function builds from the model.
func TestAnAnswererIsRespondWithTheModelItWasAsked(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.SetName("backup")
	model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
		resp := schemaflux.Response{Content: `{"number":"INV-4417"}`, FinishReason: "stop", Usage: schemaflux.Usage{Input: 20, Output: 10, Total: 30}}
		resp.Provider, resp.Model = "backup", req.Model
		return resp, nil
	})
	resp, err := model.Complete(t.Context(), schemaflux.Request{Model: "backup-model"})
	if err != nil || resp.Provider != "backup" || resp.Model != "backup-model" || model.Calls() != 1 {
		t.Errorf("%+v %v calls %d", resp, err, model.Calls())
	}
	model.Fail(errors.New("the primary is down"))
	if _, err := model.Complete(t.Context(), schemaflux.Request{}); err == nil {
		t.Error("the failure did not happen")
	}
}

// fakeStreamingProvider (tests/rootwrap_streaming_test.go): delivers a list of
// deltas, then the whole answer.
//
// The fake delivers a scripted answer in pieces of its own (four runes each), so
// a test of what the pieces add up to is replaced whole. A test of the boundaries
// the pieces fall on, which the stub set by choosing its deltas, is not: the
// deltas of a Provider cannot be scripted. That is a gap only for a test that
// asserts a boundary, and none of the stub's tests does.
func TestAStreamingProviderIsAFakeThatStreamsItsReply(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.Reply("a summary " + "of the text.")
	var streamer provider.Streamer = &model
	var deltas []string
	var done schemaflux.Chunk
	for chunk, err := range streamer.Stream(t.Context(), schemaflux.Request{Model: "m"}) {
		if err != nil {
			t.Fatal(err)
		}
		if chunk.Done {
			done = chunk
			continue
		}
		deltas = append(deltas, chunk.Delta)
	}
	if got := strings.Join(deltas, ""); got != "a summary of the text." {
		t.Errorf("the deltas add up to %q", got)
	}
	if len(deltas) < 2 {
		t.Errorf("the answer came as %d piece: a streamed operation has nothing to assemble", len(deltas))
	}
	if !done.Done || done.Response.Content != "a summary of the text." || done.Response.FinishReason != "stop" {
		t.Errorf("the last chunk: %+v", done)
	}
}

// truncationProvider (tests/truncation_maxtokens_integration_test.go): answers
// with a finish reason and content of the test's choosing, and keeps every
// request.
func TestATruncationProviderIsRespondWithAFinishReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, finish, content, wantContent string
	}{
		{"cut off at the ceiling", "length", `{"incomplete`, `{"incomplete`},
		{"complete", "stop", "a short answer.", "a short answer."},
		{"complete with no text given", "stop", "", "a complete answer"},
		{"a tool call finish", "tool_calls", "", "a complete answer"},
		{"a filtered answer", "content_filter", "x", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var model schemafluxtest.Provider
			model.Respond(func(_ int, req schemaflux.Request) (schemaflux.Response, error) {
				content := tc.content
				if content == "" {
					content = "a complete answer"
				}
				return schemaflux.Response{Content: content, Provider: model.Name(), Model: req.Model, FinishReason: tc.finish}, nil
			})
			resp, err := model.Complete(t.Context(), schemaflux.Request{Model: "m", MaxTokens: 321})
			if err != nil || resp.FinishReason != tc.finish || resp.Content != tc.wantContent {
				t.Errorf("%+v %v", resp, err)
			}
			if got := model.Requests(); len(got) != 1 || got[0].MaxTokens != 321 {
				t.Errorf("the requests kept: %+v", got)
			}
		})
	}
}

// captureCompletionProvider (tests/truncation_maxtokens_integration_test.go):
// answers "ok" and keeps every request it is sent.
func TestACapturingProviderIsReplyAndRequests(t *testing.T) {
	t.Parallel()
	var model schemafluxtest.Provider
	model.Reply("ok")
	_, err := ops.CallLLM(t.Context(), bind.ToInternal(&model), "You are a concise assistant.", "Summarize this text.",
		types.OpOptions{Model: "m", Intelligence: types.Fast, MaxOutputTokens: 321})
	if err != nil {
		t.Fatalf("CallLLM: %v", err)
	}
	if model.Calls() == 0 {
		t.Fatal("the provider received no request")
	}
	if got := model.Last().MaxTokens; got != 321 {
		t.Errorf("MaxTokens = %d, want the per-call override of 321", got)
	}
	if got := model.Last().System; !strings.HasSuffix(got, "You are a concise assistant.") {
		t.Errorf("System = %q, which does not end with the caller's steering", got)
	}
}
