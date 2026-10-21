package mw

import (
	"context"
	"slices"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/types"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// route is a provider named name that answers every request with body, as a
// Route of Fallback.
func route(name, body string) (Route, *schemafluxtest.Provider) {
	p := new(schemafluxtest.Provider)
	p.SetName(name)
	p.Reply(body)
	return Route{Provider: p}, p
}

// failing is a provider named name that fails every request with err.
func failingProvider(name string, err error) *schemafluxtest.Provider {
	p := new(schemafluxtest.Provider)
	p.SetName(name)
	p.Fail(err)
	return p
}

func TestFallback(t *testing.T) {
	t.Parallel()

	t.Run("a primary that answers is the answer and the alternate is not asked", func(t *testing.T) {
		t.Parallel()
		primary := new(schemafluxtest.Provider)
		primary.Reply("primary's")
		alt, altP := route("alt", "alternate's")
		resp, err := Fallback([]Route{alt})(primary).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "primary's" || altP.Calls() != 0 {
			t.Errorf("%q, %v, the alternate was asked %d times", resp.Content, err, altP.Calls())
		}
	})

	t.Run("a primary that fails is replaced by the alternate, which is named in the answer", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", unavailable())
		alt, altP := route("alt", "alternate's")
		resp, err := Fallback([]Route{alt})(primary).Complete(t.Context(), schemaflux.Request{Model: "m", User: "q"})
		if err != nil || resp.Content != "alternate's" {
			t.Fatalf("%q, %v", resp.Content, err)
		}
		if resp.Provider != "alt" || resp.Model != "m" {
			t.Errorf("the answer names %q and %q, want the alternate and the model it was sent", resp.Provider, resp.Model)
		}
		if primary.Calls() != 1 || altP.Calls() != 1 {
			t.Errorf("the primary was asked %d times and the alternate %d, want 1 each", primary.Calls(), altP.Calls())
		}
	})

	for _, tc := range []struct {
		kind   schemaflux.Kind
		failed bool
	}{
		{schemaflux.ErrUnavailable, true},
		{schemaflux.ErrRateLimited, true},
		{schemaflux.ErrTimeout, true},
		{schemaflux.ErrTooLarge, true},
		{schemaflux.ErrTruncated, true},
		{schemaflux.ErrInvalidAnswer, true},
		{schemaflux.ErrUnsupported, true},
		{schemaflux.ErrInternal, false},
		{schemaflux.ErrBusy, false},
		{schemaflux.ErrReviewRequired, false},
		{schemaflux.ErrUnauthorized, false},
		{schemaflux.ErrInvalidRequest, false},
		{schemaflux.ErrConfiguration, false},
		{schemaflux.ErrPolicy, false},
		{schemaflux.ErrRefused, false},
		{schemaflux.ErrBudgetExceeded, false},
		{schemaflux.ErrCanceled, false},
		{schemaflux.ErrClosed, false},
	} {
		name := "by default a failure of kind " + tc.kind.Error() + " is returned"
		if tc.failed {
			name = "by default a failure of kind " + tc.kind.Error() + " goes to the alternate"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			primary := failingProvider("primary", &schemaflux.Error{Kind: tc.kind})
			alt, altP := route("alt", "alternate's")
			resp, err := Fallback([]Route{alt})(primary).Complete(t.Context(), ask("q"))
			if tc.failed {
				if err != nil || resp.Content != "alternate's" {
					t.Errorf("%q, %v, want the alternate's answer", resp.Content, err)
				}
				return
			}
			wantKind(t, err, tc.kind)
			if altP.Calls() != 0 {
				t.Errorf("the alternate was asked %d times, want 0", altP.Calls())
			}
		})
	}

	t.Run("On lists the kinds that go to the alternate and no others", func(t *testing.T) {
		t.Parallel()
		alt, altP := route("alt", "alternate's")
		fallback := Fallback([]Route{alt}, FallbackOptions{On: []schemaflux.Kind{schemaflux.ErrRateLimited}})
		_, err := fallback(failingProvider("primary", unavailable())).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if altP.Calls() != 0 {
			t.Fatalf("the alternate was asked %d times for an outage not in On", altP.Calls())
		}
		resp, err := fallback(failingProvider("primary", rateLimited(0))).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "alternate's" {
			t.Errorf("a rate limit: %q, %v", resp.Content, err)
		}
	})

	t.Run("a kind named in On goes to the alternate even when it stops everything by default", func(t *testing.T) {
		t.Parallel()
		alt, _ := route("alt", "alternate's")
		for _, kind := range []schemaflux.Kind{schemaflux.ErrUnauthorized, schemaflux.ErrInvalidRequest, schemaflux.ErrPolicy, schemaflux.ErrBudgetExceeded, schemaflux.ErrConfiguration} {
			fallback := Fallback([]Route{alt}, FallbackOptions{On: []schemaflux.Kind{kind}})
			resp, err := fallback(failingProvider("primary", &schemaflux.Error{Kind: kind})).Complete(t.Context(), ask("q"))
			if err != nil || resp.Content != "alternate's" {
				t.Errorf("%v: %q, %v", kind, resp.Content, err)
			}
		}
	})

	t.Run("On tells a model's refusal from the data policy's, and a recovered panic from a bad answer", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			on, failure schemaflux.Kind
			goes        bool
		}{
			{schemaflux.ErrRefused, schemaflux.ErrRefused, true},
			{schemaflux.ErrRefused, schemaflux.ErrPolicy, false},
			{schemaflux.ErrPolicy, schemaflux.ErrPolicy, true},
			{schemaflux.ErrPolicy, schemaflux.ErrRefused, false},
			{schemaflux.ErrInternal, schemaflux.ErrInternal, true},
			{schemaflux.ErrInternal, schemaflux.ErrInvalidAnswer, false},
			{schemaflux.ErrInvalidAnswer, schemaflux.ErrInternal, false},
			{schemaflux.ErrInvalidAnswer, schemaflux.ErrInvalidAnswer, true},
			{schemaflux.ErrUnavailable, schemaflux.ErrUnavailable, true},
		} {
			alt, altP := route("alt", "alternate's")
			fallback := Fallback([]Route{alt}, FallbackOptions{On: []schemaflux.Kind{tc.on}})
			_, err := fallback(failingProvider("primary", &schemaflux.Error{Kind: tc.failure})).Complete(t.Context(), ask("q"))
			if tc.goes != (altP.Calls() == 1) {
				t.Errorf("On %v, failure %v: the alternate was asked %d times, want it asked = %v", tc.on, tc.failure, altP.Calls(), tc.goes)
			}
			if !tc.goes {
				wantKind(t, err, tc.failure)
			}
		}
	})

	t.Run("a request is not changed by the kinds, and an error that is returned is the primary's own", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", &schemaflux.Error{Kind: schemaflux.ErrUnauthorized, Message: "the key was refused", Code: "invalid_key"})
		alt, _ := route("alt", "x")
		_, err := Fallback([]Route{alt}, FallbackOptions{On: []schemaflux.Kind{schemaflux.ErrRateLimited}})(primary).Complete(t.Context(), ask("q"))
		e := wantKind(t, err, schemaflux.ErrUnauthorized)
		if e.Message != "the key was refused" || e.Code != "invalid_key" {
			t.Errorf("the error is %+v: the label of the wrapped package leaked or the fields were lost", e)
		}
	})

	t.Run("the alternates are tried in order and the first that answers is the answer", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", unavailable())
		first := failingProvider("first", unavailable())
		second, secondP := route("second", "second's")
		third, thirdP := route("third", "third's")
		resp, err := Fallback([]Route{{Provider: first}, second, third})(primary).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "second's" || resp.Provider != "second" {
			t.Fatalf("%q by %q, %v", resp.Content, resp.Provider, err)
		}
		if first.Calls() != 1 || secondP.Calls() != 1 || thirdP.Calls() != 0 {
			t.Errorf("asked %d, %d and %d times, want 1, 1 and 0", first.Calls(), secondP.Calls(), thirdP.Calls())
		}
	})

	t.Run("when every route fails the error is the last one's", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", rateLimited(0))
		last := failingProvider("last", &schemaflux.Error{Kind: schemaflux.ErrTimeout})
		_, err := Fallback([]Route{{Provider: failingProvider("first", unavailable())}, {Provider: last}})(primary).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrTimeout)
	})

	t.Run("an alternate's failure that is not one to go on from ends the search", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", unavailable())
		stops := failingProvider("stops", &schemaflux.Error{Kind: schemaflux.ErrBudgetExceeded})
		never, neverP := route("never", "x")
		_, err := Fallback([]Route{{Provider: stops}, never})(primary).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrBudgetExceeded)
		if neverP.Calls() != 0 {
			t.Errorf("the route after the one that stopped was asked %d times", neverP.Calls())
		}
	})

	t.Run("a route that cannot enforce a schema is skipped for a request that needs one", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", unavailable())
		alt, altP := route("alt", "x")
		req := schemaflux.Request{User: "q", Format: "json", Schema: map[string]any{"type": "object"}}
		fallback := Fallback([]Route{alt}, FallbackOptions{Primary: Capabilities{NativeJSONSchema: true}})
		_, err := fallback(primary).Complete(t.Context(), req)
		wantKind(t, err, schemaflux.ErrUnavailable)
		if altP.Calls() != 0 {
			t.Errorf("the alternate was asked %d times", altP.Calls())
		}
	})

	t.Run("a route that declares the capability serves it, and so does one the caller allowed to degrade", func(t *testing.T) {
		t.Parallel()
		req := schemaflux.Request{User: "q", Format: "json", Schema: map[string]any{"type": "object"}}
		capable, capableP := route("capable", "x")
		capable.Capabilities = Capabilities{NativeJSONSchema: true}
		if _, err := Fallback([]Route{capable}, FallbackOptions{Primary: Capabilities{NativeJSONSchema: true}})(failingProvider("primary", unavailable())).Complete(t.Context(), req); err != nil || capableP.Calls() != 1 {
			t.Errorf("capable: %v, %d calls", err, capableP.Calls())
		}
		weak, weakP := route("weak", "x")
		if _, err := Fallback([]Route{weak}, FallbackOptions{Primary: Capabilities{NativeJSONSchema: true}, AllowSchemaDegradation: true})(failingProvider("primary", unavailable())).Complete(t.Context(), req); err != nil || weakP.Calls() != 1 {
			t.Errorf("degraded: %v, %d calls", err, weakP.Calls())
		}
	})

	t.Run("a primary that declares nothing leaves every alternate eligible, and a request with no schema needs none", func(t *testing.T) {
		t.Parallel()
		weak, weakP := route("weak", "x")
		req := schemaflux.Request{User: "q", Format: "json", Schema: map[string]any{"type": "object"}}
		if _, err := Fallback([]Route{weak})(failingProvider("primary", unavailable())).Complete(t.Context(), req); err != nil || weakP.Calls() != 1 {
			t.Errorf("no declaration: %v, %d calls", err, weakP.Calls())
		}
		weak2, weak2P := route("weak2", "x")
		if _, err := Fallback([]Route{weak2}, FallbackOptions{Primary: Capabilities{NativeJSONSchema: true}})(failingProvider("primary", unavailable())).Complete(t.Context(), ask("q")); err != nil || weak2P.Calls() != 1 {
			t.Errorf("no schema asked: %v, %d calls", err, weak2P.Calls())
		}
	})

	t.Run("a route's Model is the model it is sent", func(t *testing.T) {
		t.Parallel()
		alt, altP := route("alt", "x")
		alt.Model = "the-alternate-model"
		primary := failingProvider("primary", unavailable())
		primary.SetName("primary")
		resp, err := Fallback([]Route{alt})(primary).Complete(t.Context(), schemaflux.Request{Model: "the-primary-model", User: "q"})
		if err != nil {
			t.Fatal(err)
		}
		if got := altP.Last().Model; got != "the-alternate-model" {
			t.Errorf("the alternate was sent model %q", got)
		}
		if primary.Last().Model != "the-primary-model" || resp.Model != "the-alternate-model" {
			t.Errorf("the primary was sent %q and the answer names %q", primary.Last().Model, resp.Model)
		}
	})

	t.Run("the client's data policy holds for an alternate: one it does not allow is never asked", func(t *testing.T) {
		t.Parallel()
		alt, altP := route("not-allowed-route", "x")
		ctx := withPolicy(t.Context(), types.DataPolicy{AllowedProviders: []string{"primary"}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30})
		_, err := Fallback([]Route{alt})(failingProvider("primary", unavailable())).Complete(ctx, ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
		if altP.Calls() != 0 {
			t.Errorf("the alternate was asked %d times, want 0", altP.Calls())
		}
	})

	t.Run("an alternate the policy allows, on a declared route, is asked", func(t *testing.T) {
		t.Parallel()
		provider.DeclareRoute("mw-fallback-allowed", "route-model", []string{"us"}, 0)
		alt, altP := route("mw-fallback-allowed", "alternate's")
		alt.Model = "route-model"
		ctx := withPolicy(t.Context(), types.DataPolicy{AllowedProviders: []string{"mw-fallback-allowed"}, AllowedRegions: []string{"us"}, MaxRetentionDays: 30})
		resp, err := Fallback([]Route{alt})(failingProvider("primary", unavailable())).Complete(ctx, ask("q"))
		if err != nil || resp.Content != "alternate's" || altP.Calls() != 1 {
			t.Errorf("%q, %v, %d calls", resp.Content, err, altP.Calls())
		}
	})

	t.Run("a context that has ended is the result and the alternates are not asked", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		primary := new(schemafluxtest.Provider)
		primary.SetName("primary")
		primary.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			cancel()
			return schemaflux.Response{}, unavailable()
		})
		alt, altP := route("alt", "x")
		_, err := Fallback([]Route{alt})(primary).Complete(ctx, ask("q"))
		if !isContextError(err) || altP.Calls() != 0 {
			t.Errorf("err = %v, the alternate was asked %d times", err, altP.Calls())
		}
	})

	t.Run("a route with no provider and an unknown kind are configuration errors and nothing is sent", func(t *testing.T) {
		t.Parallel()
		for name, fallback := range map[string]Middleware{
			"no provider":  Fallback([]Route{{}}),
			"unknown kind": Fallback(nil, FallbackOptions{On: []schemaflux.Kind{schemaflux.Kind(0)}}),
			"huge kind":    Fallback(nil, FallbackOptions{On: []schemaflux.Kind{schemaflux.Kind(200)}}),
		} {
			var p schemafluxtest.Provider
			_, err := fallback(&p).Complete(t.Context(), ask("q"))
			e := wantKind(t, err, schemaflux.ErrConfiguration)
			if e.Op != "mw.fallback" || p.Calls() != 0 {
				t.Errorf("%s: Op %q, %d requests", name, e.Op, p.Calls())
			}
		}
	})

	t.Run("a fallback with no alternates is its primary", func(t *testing.T) {
		t.Parallel()
		primary := failingProvider("primary", unavailable())
		_, err := Fallback(nil)(primary).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrUnavailable)
	})

	t.Run("its Unwrap and its Name are the primary's", func(t *testing.T) {
		t.Parallel()
		primary := new(schemafluxtest.Provider)
		primary.SetName("the-primary")
		alt, _ := route("alt", "x")
		wrapped := Fallback([]Route{alt})(primary)
		if wrapped.Name() != "the-primary" || wrapped.(provider.Unwrapper).Unwrap() != schemaflux.Provider(primary) {
			t.Errorf("Name %q, Unwrap %v", wrapped.Name(), wrapped.(provider.Unwrapper).Unwrap())
		}
	})

	t.Run("option values merge left to right with a later non-zero field winning", func(t *testing.T) {
		t.Parallel()
		alt, altP := route("alt", "alternate's")
		fallback := Fallback([]Route{alt},
			FallbackOptions{On: []schemaflux.Kind{schemaflux.ErrTimeout}},
			FallbackOptions{},
			FallbackOptions{On: []schemaflux.Kind{schemaflux.ErrUnavailable}})
		resp, err := fallback(failingProvider("primary", unavailable())).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "alternate's" || altP.Calls() != 1 {
			t.Errorf("%q, %v, %d calls", resp.Content, err, altP.Calls())
		}
	})
}

// isContextError reports whether err is a cancellation or a passed deadline.
func isContextError(err error) bool {
	k, ok := kindOf(err)
	return ok && (k == schemaflux.ErrCanceled || k == schemaflux.ErrTimeout)
}

var _ = time.Second

// The kinds that stop a default Fallback and the kinds that go on to the next
// route are the two lists of the doc comment of FallbackOptions.On, and between
// them every kind of the library: a kind the doc does not list is a kind nobody
// decided.
func TestFallbackByDefaultStopsOrGoesOnForEveryKindAsTheDocSays(t *testing.T) {
	t.Parallel()

	stops := []schemaflux.Kind{
		schemaflux.ErrConfiguration, schemaflux.ErrInvalidRequest, schemaflux.ErrUnauthorized, schemaflux.ErrPolicy,
		schemaflux.ErrRefused, schemaflux.ErrBudgetExceeded, schemaflux.ErrBusy, schemaflux.ErrCanceled,
		schemaflux.ErrClosed, schemaflux.ErrReviewRequired, schemaflux.ErrInternal,
	}
	goesOn := []schemaflux.Kind{
		schemaflux.ErrUnsupported, schemaflux.ErrRateLimited, schemaflux.ErrUnavailable, schemaflux.ErrTimeout,
		schemaflux.ErrTooLarge, schemaflux.ErrTruncated, schemaflux.ErrInvalidAnswer,
	}
	var all []schemaflux.Kind
	for kind := schemaflux.ErrConfiguration; kind <= schemaflux.ErrInternal; kind++ {
		all = append(all, kind)
	}
	if len(stops)+len(goesOn) != len(all) {
		t.Fatalf("the doc lists %d kinds and the library has %d", len(stops)+len(goesOn), len(all))
	}
	for _, kind := range all {
		if slices.Contains(stops, kind) == slices.Contains(goesOn, kind) {
			t.Errorf("%v is in both lists of the doc, or in neither", kind)
		}
	}

	for _, kind := range all {
		t.Run(kind.Error(), func(t *testing.T) {
			t.Parallel()
			primary := failingProvider("primary", &schemaflux.Error{Kind: kind})
			alt, altP := route("alt", "alternate's")
			resp, err := Fallback([]Route{alt})(primary).Complete(t.Context(), ask("q"))
			if slices.Contains(goesOn, kind) {
				if err != nil || resp.Content != "alternate's" {
					t.Errorf("%q, %v, want the alternate's answer", resp.Content, err)
				}
				return
			}
			wantKind(t, err, kind)
			if altP.Calls() != 0 {
				t.Errorf("the alternate was asked %d times, want 0", altP.Calls())
			}
		})
	}

	t.Run("a failure no layer classified goes on", func(t *testing.T) {
		t.Parallel()
		alt, _ := route("alt", "alternate's")
		resp, err := Fallback([]Route{alt})(failingProvider("primary", errPlain)).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "alternate's" {
			t.Errorf("%q, %v", resp.Content, err)
		}
	})

	t.Run("a panic in the primary is an internal error and the alternate does not replace it", func(t *testing.T) {
		t.Parallel()
		primary := Func("primary", func(context.Context, schemaflux.Request) (schemaflux.Response, error) { panic("provider bug") })
		alt, altP := route("alt", "alternate's")
		_, err := Fallback([]Route{alt})(primary).Complete(t.Context(), ask("q"))
		wantKind(t, err, schemaflux.ErrInternal)
		if altP.Calls() != 0 {
			t.Errorf("the alternate was asked %d times, want 0", altP.Calls())
		}
	})
}
