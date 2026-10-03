package mw

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// yesNo is a decision of one yes-or-no question about state.
func yesNo(state, question string) *schemaflux.Decision {
	return &schemaflux.Decision{State: state, Questions: []schemaflux.DecisionQuestion{
		{Kind: schemaflux.DecisionYesNo, Ask: question},
	}}
}

// choice is a decision of one choice among options.
func choice(question string, options ...string) *schemaflux.Decision {
	return &schemaflux.Decision{State: `"s"`, Questions: []schemaflux.DecisionQuestion{
		{Kind: schemaflux.DecisionChoice, Ask: question, Options: options},
	}}
}

// deciding is a request that carries d, with the same prompts every time.
func deciding(d *schemaflux.Decision) schemaflux.Request {
	return schemaflux.Request{System: "the system prompt", User: "the user message", Decision: d}
}

// answeringByQuestion is a decision provider that answers yes to a question
// that mentions "yes" and no to any other, and counts the requests it answered.
func answeringByQuestion() (*schemafluxtest.Decider, *atomic.Int32) {
	var n atomic.Int32
	d := new(schemafluxtest.Decider)
	d.Respond(func(_ int, decision schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
		n.Add(1)
		answers := make([]schemaflux.DecisionAnswer, len(decision.Questions))
		for i, q := range decision.Questions {
			switch q.Kind {
			case schemaflux.DecisionChoice:
				answers[i] = schemafluxtest.Chose(q.Options[len(q.Options)-1], 0.9)
			default:
				answers[i] = schemafluxtest.No(0.8)
				if len(q.Ask) >= 3 && q.Ask[:3] == "yes" {
					answers[i] = schemafluxtest.Yes(0.8)
				}
			}
		}
		return answers, nil
	})
	return d, &n
}

// mapStore is a CacheStore of a program's own.
type mapStore struct {
	mu      sync.Mutex
	entries map[string]schemaflux.Response
	sets    []time.Duration
}

func (m *mapStore) Get(_ context.Context, key string) (schemaflux.Response, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp, ok := m.entries[key]
	return resp, ok
}

func (m *mapStore) Set(_ context.Context, key string, resp schemaflux.Response, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]schemaflux.Response{}
	}
	m.entries[key] = resp
	m.sets = append(m.sets, ttl)
}

// events collects what OnEvent is told.
type events struct {
	mu   sync.Mutex
	list []CacheEvent
}

func (e *events) add(event CacheEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, event)
}

func (e *events) all() []CacheEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

func TestCache(t *testing.T) {
	t.Parallel()

	t.Run("an identical request is answered from memory and the provider is asked once", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		cached := Cache()(p)
		for i := range 3 {
			resp, err := cached.Complete(t.Context(), ask("same"))
			if err != nil || resp.Content != "answer" {
				t.Fatalf("request %d: %q, %v", i+1, resp.Content, err)
			}
		}
		if calls.Load() != 1 {
			t.Errorf("the provider was asked %d times, want 1", calls.Load())
		}
	})

	t.Run("a hit says it was cached and reports no usage and no timing, and the first answer keeps its own", func(t *testing.T) {
		t.Parallel()
		p, _ := answering("answer")
		cached := Cache()(p)
		miss, _ := cached.Complete(t.Context(), ask("same"))
		hit, _ := cached.Complete(t.Context(), ask("same"))
		if miss.Cached || miss.Usage.Total != 15 {
			t.Errorf("the miss: Cached %v, usage %+v, want it uncached with its own usage", miss.Cached, miss.Usage)
		}
		if !hit.Cached || hit.Usage != (schemaflux.Usage{}) || hit.Timing != (schemaflux.Timing{}) {
			t.Errorf("the hit: Cached %v, usage %+v, timing %+v", hit.Cached, hit.Usage, hit.Timing)
		}
		if hit.Content != miss.Content || hit.Model != miss.Model {
			t.Errorf("the hit answers %q by %q, the miss %q by %q", hit.Content, hit.Model, miss.Content, miss.Model)
		}
	})

	base := schemaflux.Request{
		Model: "m", System: "sys", User: "usr", Temperature: 0.3, MaxTokens: 100, Format: "json",
		Schema: map[string]any{"type": "object"}, SchemaName: "named", Effort: "low", CacheKey: "key",
	}
	for _, tc := range []struct {
		field  string
		change func(*schemaflux.Request)
	}{
		{"System", func(r *schemaflux.Request) { r.System = "other" }},
		{"User", func(r *schemaflux.Request) { r.User = "other" }},
		{"Model", func(r *schemaflux.Request) { r.Model = "other" }},
		{"Temperature", func(r *schemaflux.Request) { r.Temperature = 0.31 }},
		{"MaxTokens", func(r *schemaflux.Request) { r.MaxTokens = 101 }},
		{"Format", func(r *schemaflux.Request) { r.Format = "text" }},
		{"Schema", func(r *schemaflux.Request) { r.Schema = map[string]any{"type": "array"} }},
		{"SchemaName", func(r *schemaflux.Request) { r.SchemaName = "other" }},
		{"Effort", func(r *schemaflux.Request) { r.Effort = "high" }},
		{"EffortPreferred", func(r *schemaflux.Request) { r.EffortPreferred = true }},
		{"CacheKey", func(r *schemaflux.Request) { r.CacheKey = "other" }},
		{"WebSearch", func(r *schemaflux.Request) { r.WebSearch = true }},
	} {
		t.Run("a request that differs in "+tc.field+" is not served the other's answer", func(t *testing.T) {
			t.Parallel()
			p, calls := answering("answer")
			cached := Cache()(p)
			other := base
			tc.change(&other)
			for _, req := range []schemaflux.Request{base, other, base, other} {
				if _, err := cached.Complete(t.Context(), req); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 2 {
				t.Errorf("the provider was asked %d times, want 2: one for each request", calls.Load())
			}
		})
	}

	t.Run("a typed decision is kept with its answers and an identical decision is a hit", func(t *testing.T) {
		t.Parallel()
		d, calls := answeringByQuestion()
		cached := Cache()(d)
		first, err := cached.Complete(t.Context(), deciding(yesNo(`"x"`, "yes, is it?")))
		if err != nil {
			t.Fatal(err)
		}
		second, err := cached.Complete(t.Context(), deciding(yesNo(`"x"`, "yes, is it?")))
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatalf("the decision provider was asked %d times, want 1", calls.Load())
		}
		want := []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.8, ConfidenceReported: true}}
		if !slices.Equal(first.Answers, want) || !slices.Equal(second.Answers, want) || !second.Cached {
			t.Errorf("answers %+v then %+v (cached %v), want %+v twice", first.Answers, second.Answers, second.Cached, want)
		}
	})

	t.Run("the same prompts asked as another decision are never served the first decision's answer", func(t *testing.T) {
		t.Parallel()
		d, calls := answeringByQuestion()
		cached := Cache()(d)
		yes, _ := cached.Complete(t.Context(), deciding(yesNo(`"x"`, "yes, is it?")))
		no, _ := cached.Complete(t.Context(), deciding(yesNo(`"x"`, "is it?")))
		if calls.Load() != 2 {
			t.Fatalf("the decision provider was asked %d times, want 2", calls.Load())
		}
		if !yes.Answers[0].Yes || no.Answers[0].Yes {
			t.Errorf("the answers were %+v and %+v: the second was given the first's", yes.Answers, no.Answers)
		}
	})

	for _, tc := range []struct {
		name  string
		other *schemaflux.Decision
	}{
		{"the state", yesNo(`"y"`, "is it?")},
		{"the guidance", &schemaflux.Decision{State: `"x"`, Guidance: "be strict", Questions: yesNo(`"x"`, "is it?").Questions}},
		{"the question", yesNo(`"x"`, "is that?")},
		{"the kind of question", &schemaflux.Decision{State: `"x"`, Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionScale, Ask: "is it?", Low: 1, High: 5}}}},
		{"the ends of a scale", &schemaflux.Decision{State: `"x"`, Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionScale, Ask: "is it?", Low: 1, High: 7}}}},
		{"the number of questions", &schemaflux.Decision{State: `"x"`, Questions: append(yesNo(`"x"`, "is it?").Questions, schemaflux.DecisionQuestion{Kind: schemaflux.DecisionYesNo, Ask: "and this?"})}},
		{"a decision against none", nil},
	} {
		t.Run("a decision that differs in "+tc.name+" is a different request", func(t *testing.T) {
			t.Parallel()
			var p schemafluxtest.Provider
			p.Reply("text")
			cached := Cache()(&p)
			scale := &schemaflux.Decision{State: `"x"`, Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionScale, Ask: "is it?", Low: 1, High: 5}}}
			first := yesNo(`"x"`, "is it?")
			if tc.name == "the ends of a scale" {
				first = scale
			}
			for _, d := range []*schemaflux.Decision{first, tc.other, first, tc.other} {
				if _, err := cached.Complete(t.Context(), deciding(d)); err != nil {
					t.Fatal(err)
				}
			}
			if p.Calls() != 2 {
				t.Errorf("the provider was asked %d times, want 2: one for each decision", p.Calls())
			}
		})
	}

	t.Run("choices that differ in their options are different requests, and each is answered with its own label", func(t *testing.T) {
		t.Parallel()
		d, calls := answeringByQuestion()
		cached := Cache()(d)
		a, _ := cached.Complete(t.Context(), deciding(choice("which?", "red", "green")))
		b, _ := cached.Complete(t.Context(), deciding(choice("which?", "red", "blue")))
		if calls.Load() != 2 || a.Answers[0].Label != "green" || b.Answers[0].Label != "blue" {
			t.Errorf("%d requests, labels %q and %q, want 2, green and blue", calls.Load(), a.Answers[0].Label, b.Answers[0].Label)
		}
	})

	t.Run("criteria that are absent differ from criteria that are blank", func(t *testing.T) {
		t.Parallel()
		d, calls := answeringByQuestion()
		cached := Cache()(d)
		absent := choice("which?", "red", "green")
		blank := choice("which?", "red", "green")
		blank.Questions[0].Criteria = []string{"", ""}
		cached.Complete(t.Context(), deciding(absent))
		cached.Complete(t.Context(), deciding(blank))
		if calls.Load() != 2 {
			t.Errorf("the provider was asked %d times, want 2", calls.Load())
		}
	})

	t.Run("a typed answer that does not answer its question is not kept", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Chose("purple"), schemafluxtest.Chose("purple"))
		cached := Cache(CacheOptions{})(d)
		for range 2 {
			cached.Complete(t.Context(), deciding(choice("which?", "red", "green")))
		}
		if d.Calls() != 2 {
			t.Errorf("the provider was asked %d times, want 2: a label nobody offered was stored", d.Calls())
		}
	})

	bubble(t, "an answer is served until its TTL and asked for again after it", func(t *testing.T) {
		p, calls := answering("answer")
		cached := Cache(CacheOptions{TTL: time.Minute})(p)
		cached.Complete(t.Context(), ask("q"))
		time.Sleep(59 * time.Second)
		cached.Complete(t.Context(), ask("q"))
		if calls.Load() != 1 {
			t.Fatalf("the provider was asked %d times inside the TTL, want 1", calls.Load())
		}
		time.Sleep(2 * time.Second)
		cached.Complete(t.Context(), ask("q"))
		if calls.Load() != 2 {
			t.Errorf("the provider was asked %d times after the TTL, want 2", calls.Load())
		}
	})

	t.Run("without a TTL an answer does not expire", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			p, calls := answering("answer")
			cached := Cache()(p)
			cached.Complete(t.Context(), ask("q"))
			time.Sleep(24 * 365 * time.Hour)
			cached.Complete(t.Context(), ask("q"))
			if calls.Load() != 1 {
				t.Errorf("the provider was asked %d times, want 1", calls.Load())
			}
		})
	})

	t.Run("a store of the program's own keeps the answers, typed answers included, and serves a second cache", func(t *testing.T) {
		t.Parallel()
		store := &mapStore{}
		d, calls := answeringByQuestion()
		first := Cache(CacheOptions{Store: store, TTL: time.Hour})(d)
		if _, err := first.Complete(t.Context(), deciding(yesNo(`"x"`, "yes, is it?"))); err != nil {
			t.Fatal(err)
		}
		if len(store.sets) != 1 || store.sets[0] != time.Hour {
			t.Fatalf("the store was set %v, want once with the TTL", store.sets)
		}
		for _, kept := range store.entries {
			if len(kept.Answers) != 1 || !kept.Answers[0].Yes || !kept.Answers[0].ConfidenceReported {
				t.Errorf("the store kept %+v, want the typed answer", kept)
			}
		}
		second := Cache(CacheOptions{Store: store})(d)
		resp, err := second.Complete(t.Context(), deciding(yesNo(`"x"`, "yes, is it?")))
		if err != nil || !resp.Cached || len(resp.Answers) != 1 || !resp.Answers[0].Yes {
			t.Fatalf("the second cache: %+v, %v", resp, err)
		}
		if calls.Load() != 1 {
			t.Errorf("the decision provider was asked %d times, want 1", calls.Load())
		}
	})

	t.Run("what a hit is handed shares nothing with what is stored", func(t *testing.T) {
		t.Parallel()
		d, _ := answeringByQuestion()
		cached := Cache()(d)
		req := deciding(yesNo(`"x"`, "yes, is it?"))
		cached.Complete(t.Context(), req)
		hit, _ := cached.Complete(t.Context(), req)
		hit.Answers[0].Yes = false
		hit.Answers[0].ModelConfidence = 0
		again, _ := cached.Complete(t.Context(), req)
		if !again.Answers[0].Yes || again.Answers[0].ModelConfidence != 0.8 {
			t.Errorf("a caller's edit reached the store: %+v", again.Answers)
		}
	})

	t.Run("OnEvent says a miss and then a hit, with a digest for a key and never the text", func(t *testing.T) {
		t.Parallel()
		var seen events
		p, _ := answering("answer")
		cached := Cache(CacheOptions{OnEvent: seen.add})(p)
		cached.Complete(t.Context(), ask("the secret text "+testEmail))
		cached.Complete(t.Context(), ask("the secret text "+testEmail))
		got := seen.all()
		if len(got) != 2 || got[0].Hit || !got[1].Hit || got[0].Key != got[1].Key {
			t.Fatalf("events = %+v, want a miss then a hit of one key", got)
		}
		if len(got[0].Key) != 64 {
			t.Errorf("the key is %q, want a sha-256 digest", got[0].Key)
		}
		mustNotContain(t, "the key", got[0].Key, "secret", testEmail)
	})

	t.Run("a partition keeps one tenant's answers from another", func(t *testing.T) {
		t.Parallel()
		var seen events
		p, calls := answering("answer")
		cached := Cache(CacheOptions{OnEvent: seen.add})(p)
		a := WithCachePartition(t.Context(), CachePartition{Tenant: "a", DataPolicy: "public"})
		b := WithCachePartition(t.Context(), CachePartition{Tenant: "b", DataPolicy: "public"})
		for _, ctx := range []context.Context{a, b, a, b, t.Context()} {
			cached.Complete(ctx, ask("q"))
		}
		if calls.Load() != 3 {
			t.Errorf("the provider was asked %d times, want 3: a, b and the zero partition", calls.Load())
		}
		got := seen.all()
		if got[0].Tenant != "a" || got[0].DataPolicy != "public" || got[1].Tenant != "b" || got[4].Tenant != "" {
			t.Errorf("events = %+v", got)
		}
	})

	t.Run("a different data policy label is a different partition", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		cached := Cache()(p)
		public := WithCachePartition(t.Context(), CachePartition{Tenant: "a", DataPolicy: "public"})
		private := WithCachePartition(t.Context(), CachePartition{Tenant: "a", DataPolicy: "private"})
		cached.Complete(public, ask("q"))
		cached.Complete(private, ask("q"))
		if calls.Load() != 2 {
			t.Errorf("the provider was asked %d times, want 2", calls.Load())
		}
	})

	t.Run("an answer that was cut off or empty is returned and not kept", func(t *testing.T) {
		t.Parallel()
		for name, resp := range map[string]schemaflux.Response{
			"cut off at the limit":   {Content: "half an ans", FinishReason: "length"},
			"withheld by the filter": {Content: "", FinishReason: "content_filter"},
			"empty":                  {Content: ""},
		} {
			var n atomic.Int32
			var p schemafluxtest.Provider
			p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) { n.Add(1); return resp, nil })
			cached := Cache()(&p)
			cached.Complete(t.Context(), ask("q"))
			cached.Complete(t.Context(), ask("q"))
			if n.Load() != 2 {
				t.Errorf("%s: the provider was asked %d times, want 2", name, n.Load())
			}
		}
	})

	t.Run("a failure is returned each time and a later answer is kept", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.FailThen(2, unavailable(), "answer")
		cached := Cache()(&p)
		for range 2 {
			_, err := cached.Complete(t.Context(), ask("q"))
			wantKind(t, err, schemaflux.ErrUnavailable)
		}
		cached.Complete(t.Context(), ask("q"))
		resp, err := cached.Complete(t.Context(), ask("q"))
		if err != nil || !resp.Cached || p.Calls() != 3 {
			t.Errorf("resp cached %v, err %v, %d provider calls, want a hit after 3", resp.Cached, err, p.Calls())
		}
	})

	bubble(t, "identical requests in flight together share one provider call", func(t *testing.T) {
		h := newHeld()
		var seen events
		cached := Cache(CacheOptions{OnEvent: seen.add})(h)
		var done []<-chan error
		for range 4 {
			done = append(done, started(t.Context(), cached))
			synctest.Wait()
		}
		if h.Calls() != 1 {
			t.Fatalf("the provider was asked %d times while four identical requests waited, want 1", h.Calls())
		}
		h.let()
		for i, d := range done {
			if err := <-d; err != nil {
				t.Errorf("request %d: %v", i+1, err)
			}
		}
		coalesced := 0
		for _, e := range seen.all() {
			if e.Coalesced && e.Hit {
				coalesced++
			}
		}
		if coalesced != 3 {
			t.Errorf("%d events were coalesced hits, want 3: %+v", coalesced, seen.all())
		}
	})

	bubble(t, "a request whose first request was canceled asks again with its own context", func(t *testing.T) {
		release := make(chan struct{})
		var n atomic.Int32
		slow := Func("slow", func(ctx context.Context, _ schemaflux.Request) (schemaflux.Response, error) {
			n.Add(1)
			select {
			case <-release:
				return schemaflux.Response{Content: "answer"}, nil
			case <-ctx.Done():
				return schemaflux.Response{}, ctx.Err()
			}
		})
		cached := Cache()(slow)
		leaderCtx, cancel := context.WithCancel(t.Context())
		leader := started(leaderCtx, cached)
		synctest.Wait()
		follower := started(t.Context(), cached)
		synctest.Wait()
		cancel()
		synctest.Wait()
		close(release)
		if err := <-follower; err != nil {
			t.Errorf("the follower was given the leader's cancellation: %v", err)
		}
		wantKind(t, <-leader, schemaflux.ErrCanceled)
		if n.Load() != 2 {
			t.Errorf("the provider was asked %d times, want 2", n.Load())
		}
	})

	t.Run("a panic in the provider is an internal error and leaves the request free to be asked again", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		p := Func("p", func(context.Context, schemaflux.Request) (schemaflux.Response, error) {
			if calls.Add(1) == 1 {
				panic("provider bug")
			}
			return schemaflux.Response{Content: "answer"}, nil
		})
		cached := Cache()(p)
		_, err := cached.Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrInternal)
		resp, err := cached.Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "answer" {
			t.Errorf("the request after the panic: %q, %v", resp.Content, err)
		}
	})

	t.Run("a client whose policy does not allow caching goes round the cache", func(t *testing.T) {
		t.Parallel()
		var seen events
		p, calls := answering("answer")
		cached := Cache(CacheOptions{OnEvent: seen.add})(p)
		ctx := withPolicy(t.Context(), types.DataPolicy{AllowedProviders: []string{"test"}})
		cached.Complete(ctx, ask("q"))
		cached.Complete(ctx, ask("q"))
		if calls.Load() != 2 {
			t.Errorf("the provider was asked %d times, want 2", calls.Load())
		}
		for _, e := range seen.all() {
			if !e.Bypassed || e.Hit {
				t.Errorf("event %+v, want Bypassed and no hit", e)
			}
		}
	})

	t.Run("a client whose policy allows caching is cached, and apart from one that declared none", func(t *testing.T) {
		t.Parallel()
		p, calls := answering("answer")
		cached := Cache()(p)
		allowing := withPolicy(t.Context(), types.DataPolicy{AllowResultCaching: true})
		cached.Complete(allowing, ask("q"))
		cached.Complete(allowing, ask("q"))
		cached.Complete(t.Context(), ask("q"))
		if calls.Load() != 2 {
			t.Errorf("the provider was asked %d times, want 2: one for the policy, one for no policy", calls.Load())
		}
	})

	t.Run("providers with different names do not read each other's answers", func(t *testing.T) {
		t.Parallel()
		cache := Cache()
		a, ac := answering("from a")
		b, bc := answering("from b")
		a.SetName("a")
		b.SetName("b")
		respA, _ := cache(a).Complete(t.Context(), ask("q"))
		respB, _ := cache(b).Complete(t.Context(), ask("q"))
		if respA.Content != "from a" || respB.Content != "from b" || ac.Load() != 1 || bc.Load() != 1 {
			t.Errorf("got %q and %q after %d and %d calls", respA.Content, respB.Content, ac.Load(), bc.Load())
		}
	})

	t.Run("a negative TTL is a configuration error and nothing is sent", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		_, err := Cache(CacheOptions{TTL: -time.Second})(&p).Complete(t.Context(), ask("q"))
		e := wantKind(t, err, schemaflux.ErrConfiguration)
		if e.Op != "mw.cache" || p.Calls() != 0 {
			t.Errorf("Op %q, %d requests", e.Op, p.Calls())
		}
	})

	t.Run("a callback that panics does not change the answer", func(t *testing.T) {
		t.Parallel()
		p, _ := answering("answer")
		cached := Cache(CacheOptions{OnEvent: func(CacheEvent) { panic("callback bug") }})(p)
		resp, err := cached.Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "answer" {
			t.Errorf("%q, %v", resp.Content, err)
		}
	})

	t.Run("option values merge left to right with a later non-zero field winning", func(t *testing.T) {
		t.Parallel()
		store := &mapStore{}
		var seen events
		p, _ := answering("answer")
		cached := Cache(CacheOptions{TTL: time.Minute, OnEvent: seen.add}, CacheOptions{Store: store}, CacheOptions{TTL: time.Hour})(p)
		cached.Complete(t.Context(), ask("q"))
		if len(store.sets) != 1 || store.sets[0] != time.Hour || len(seen.all()) != 1 {
			t.Errorf("sets %v, events %d, want one set with the last TTL and one event", store.sets, len(seen.all()))
		}
	})
}

// slowStore is a CacheStore that waits on a network for a time, unless the
// context of the request it is for ends first.
type slowStore struct {
	mapStore
	readTime time.Duration
	gets     atomic.Int32
}

func (s *slowStore) Get(ctx context.Context, key string) (schemaflux.Response, bool) {
	s.gets.Add(1)
	select {
	case <-time.After(s.readTime):
	case <-ctx.Done():
		return schemaflux.Response{}, false
	}
	return s.mapStore.Get(ctx, key)
}

// A store behind a network that takes 300 ms to answer, and a request that has 50:
// the request ends with its deadline and is not sent, the store is read once, and
// it was given the request's context to give up on.
func TestCacheGivesUpOnAStoreWhenTheRequestsDeadlinePasses(t *testing.T) {
	t.Parallel()
	bubble(t, "", func(t *testing.T) {
		store := &slowStore{readTime: 300 * time.Millisecond}
		p, calls := answering("answer")
		cached := Cache(CacheOptions{Store: store})(p)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := cached.Complete(ctx, ask("q"))
		wantKind(t, err, schemaflux.ErrTimeout)
		if took := time.Since(start); took != 50*time.Millisecond {
			t.Errorf("the request took %v, want the 50 ms it had", took)
		}
		if calls.Load() != 0 || store.gets.Load() != 1 {
			t.Errorf("the provider was asked %d times and the store read %d, want 0 and 1", calls.Load(), store.gets.Load())
		}
	})
}

// countedStore counts its reads and writes.
type countedStore struct {
	mapStore
	gets, sets atomic.Int32
}

func (s *countedStore) Get(ctx context.Context, key string) (schemaflux.Response, bool) {
	s.gets.Add(1)
	return s.mapStore.Get(ctx, key)
}

func (s *countedStore) Set(ctx context.Context, key string, resp schemaflux.Response, ttl time.Duration) {
	s.sets.Add(1)
	s.mapStore.Set(ctx, key, resp, ttl)
}

func TestCacheReadsAStoreOnceAndWritesItOnceForAMiss(t *testing.T) {
	t.Parallel()
	store := &countedStore{}
	p, calls := answering("answer")
	cached := Cache(CacheOptions{Store: store})(p)
	if _, err := cached.Complete(t.Context(), ask("q")); err != nil {
		t.Fatal(err)
	}
	if store.gets.Load() != 1 || store.sets.Load() != 1 || calls.Load() != 1 {
		t.Errorf("a miss read the store %d times, wrote it %d, and asked the provider %d, want 1, 1 and 1", store.gets.Load(), store.sets.Load(), calls.Load())
	}
	if resp, err := cached.Complete(t.Context(), ask("q")); err != nil || !resp.Cached || store.gets.Load() != 2 || store.sets.Load() != 1 {
		t.Errorf("a hit: cached %v, err %v, reads %d, writes %d, want cached, none, 2 and 1", resp.Cached, err, store.gets.Load(), store.sets.Load())
	}
}

// panickingStore panics in the methods it is told to.
type panickingStore struct {
	mapStore
	onGet, onSet bool
}

func (s *panickingStore) Get(ctx context.Context, key string) (schemaflux.Response, bool) {
	if s.onGet {
		panic("store down")
	}
	return s.mapStore.Get(ctx, key)
}

func (s *panickingStore) Set(ctx context.Context, key string, resp schemaflux.Response, ttl time.Duration) {
	if s.onSet {
		panic("store down")
	}
	s.mapStore.Set(ctx, key, resp, ttl)
}

// A store of the program's own is the program's code. A read that panics is a miss,
// and a write that panics loses the entry and leaves the answer, which was paid for,
// with the request that paid for it.
func TestCacheSurvivesAStoreThatPanics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		onGet, onSet bool
	}{
		{"a read that panics", true, false},
		{"a write that panics", false, true},
		{"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, calls := answering("paid for")
			cached := Cache(CacheOptions{Store: &panickingStore{onGet: tc.onGet, onSet: tc.onSet}})(p)
			for i := range 3 {
				resp, err := cached.Complete(t.Context(), ask("q"))
				if err != nil || resp.Content != "paid for" {
					t.Fatalf("request %d: %q, %v, want the answer that was paid for", i+1, resp.Content, err)
				}
			}
			if calls.Load() != 3 {
				t.Errorf("the provider was asked %d times, want 3: nothing could be kept", calls.Load())
			}
		})
	}
}

// A store a program supplies may hold anything. An answer in it that is not a
// complete one is a miss, and the request is sent.
func TestCacheTreatsAnAnswerThatIsNotCompleteInTheStoreAsAMiss(t *testing.T) {
	t.Parallel()
	for name, stored := range map[string]schemaflux.Response{
		"an empty answer":          {},
		"an answer cut off":        {Content: "partial", FinishReason: "length"},
		"an answer withheld":       {Content: "partial", FinishReason: "content_filter"},
		"empty and cut off":        {FinishReason: "length"},
		"typed answers of nothing": {Answers: []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionChoice, Label: "purple"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &mapStore{}
			p, calls := answering("fresh")
			cached := Cache(CacheOptions{Store: store})(p)
			// The first request writes the key the store will be asked for, and
			// the entry is then replaced by what the store holds that is wrong.
			if _, err := cached.Complete(t.Context(), ask("q")); err != nil {
				t.Fatal(err)
			}
			for key := range store.entries {
				store.entries[key] = stored
			}
			resp, err := cached.Complete(t.Context(), ask("q"))
			if err != nil || resp.Cached || resp.Content != "fresh" || calls.Load() != 2 {
				t.Errorf("%q, cached %v, %v, %d provider calls, want a miss and a fresh answer", resp.Content, resp.Cached, err, calls.Load())
			}
		})
	}
}
