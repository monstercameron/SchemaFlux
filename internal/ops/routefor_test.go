package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// routeSetup is one state of the five places a call's provider can come from.
type routeSetup struct {
	snapshot *ExecConfig // on the context
	ctxProv  llm.Provider
	dflt     *ExecConfig // the default snapshot
	hook     LLMCaller
	process  llm.Provider // the process default provider
}

func routeName(p llm.Provider) string {
	if p == nil {
		return ""
	}
	return p.Name()
}

func TestRouteForOrder(t *testing.T) {
	snapshotProvider := &namedProvider{name: "snapshot"}
	ctxProvider := &namedProvider{name: "context"}
	defaultSnapshotProvider := &namedProvider{name: "default-snapshot"}
	processProvider := &namedProvider{name: "process"}
	hook := func(context.Context, string, string, types.OpOptions) (string, error) { return "hook", nil }
	snapshotErr := errors.New("the client cannot make a call")

	cases := []struct {
		name         string
		setup        routeSetup
		wantProvider string
		wantHook     bool
		wantErr      error // matched with errors.Is; nil means none
		wantNoProv   bool
	}{
		{"the snapshot's provider beats everything", routeSetup{
			snapshot: &ExecConfig{Provider: snapshotProvider}, ctxProv: ctxProvider,
			dflt: &ExecConfig{Provider: defaultSnapshotProvider}, hook: hook, process: processProvider,
		}, "snapshot", false, nil, false},
		{"the default snapshot's provider beats the context's", routeSetup{
			ctxProv: ctxProvider, dflt: &ExecConfig{Provider: defaultSnapshotProvider}, hook: hook, process: processProvider,
		}, "default-snapshot", false, nil, false},
		{"the context's snapshot hides the default snapshot", routeSetup{
			snapshot: &ExecConfig{Provider: snapshotProvider}, dflt: &ExecConfig{Provider: defaultSnapshotProvider},
		}, "snapshot", false, nil, false},
		{"the context's provider beats the hook and the process default", routeSetup{
			ctxProv: ctxProvider, hook: hook, process: processProvider,
		}, "context", false, nil, false},
		{"a snapshot with no provider falls through to the context's", routeSetup{
			snapshot: &ExecConfig{Model: "m"}, ctxProv: ctxProvider, process: processProvider,
		}, "context", false, nil, false},
		{"the hook answers and the process default is still named for planning", routeSetup{
			hook: hook, process: processProvider,
		}, "process", true, nil, false},
		{"the hook alone", routeSetup{hook: hook}, "", true, nil, false},
		{"the process default alone", routeSetup{process: processProvider}, "process", false, nil, false},
		{"a snapshot's error when nothing else is there", routeSetup{
			snapshot: &ExecConfig{Err: snapshotErr},
		}, "", false, snapshotErr, false},
		{"a snapshot's error beats the process default", routeSetup{
			snapshot: &ExecConfig{Err: snapshotErr}, process: processProvider,
		}, "", false, snapshotErr, false},
		{"a snapshot's error beats the context's provider", routeSetup{
			snapshot: &ExecConfig{Err: snapshotErr}, ctxProv: ctxProvider,
		}, "", false, snapshotErr, false},
		{"a snapshot's error beats the hook", routeSetup{
			snapshot: &ExecConfig{Err: snapshotErr}, hook: hook,
		}, "", false, snapshotErr, false},
		{"a snapshot's error beats the context's provider, the hook and the process default", routeSetup{
			snapshot: &ExecConfig{Err: snapshotErr}, ctxProv: ctxProvider, hook: hook, process: processProvider,
		}, "", false, snapshotErr, false},
		{"the default snapshot's error beats the context's provider", routeSetup{
			dflt: &ExecConfig{Err: snapshotErr}, ctxProv: ctxProvider,
		}, "", false, snapshotErr, false},
		{"the default snapshot's error beats the hook and the process default", routeSetup{
			dflt: &ExecConfig{Err: snapshotErr}, hook: hook, process: processProvider,
		}, "", false, snapshotErr, false},
		{"the context's snapshot hides the default snapshot's error", routeSetup{
			snapshot: &ExecConfig{Provider: snapshotProvider}, dflt: &ExecConfig{Err: snapshotErr},
		}, "snapshot", false, nil, false},
		{"a snapshot that has both a provider and an error is read as a provider", routeSetup{
			snapshot: &ExecConfig{Provider: snapshotProvider, Err: snapshotErr}, ctxProv: ctxProvider,
		}, "snapshot", false, nil, false},
		{"a snapshot with no provider and no error gives way to the context's provider", routeSetup{
			snapshot: &ExecConfig{}, ctxProv: ctxProvider,
		}, "context", false, nil, false},
		{"the default snapshot's error is read too", routeSetup{
			dflt: &ExecConfig{Err: snapshotErr},
		}, "", false, snapshotErr, false},
		{"nothing at all", routeSetup{}, "", false, ErrNoProvider, true},
		{"a snapshot with neither provider nor error", routeSetup{
			snapshot: &ExecConfig{Model: "m"},
		}, "", false, ErrNoProvider, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			swapNoProvider(t)
			withDefaultExec(t, tc.setup.dflt)
			if tc.setup.hook != nil {
				setLLMCaller(tc.setup.hook)
			}
			if tc.setup.process != nil {
				SetDefaultProvider(tc.setup.process)
			}
			ctx := context.Background()
			if tc.setup.snapshot != nil {
				ctx = WithSharedExecConfig(ctx, tc.setup.snapshot)
			}
			if tc.setup.ctxProv != nil {
				ctx = WithProvider(ctx, tc.setup.ctxProv)
			}

			provider, caller, err := routeFor(ctx)
			if got := routeName(provider); got != tc.wantProvider {
				t.Errorf("provider = %q, want %q", got, tc.wantProvider)
			}
			if (caller != nil) != tc.wantHook {
				t.Errorf("caller present = %v, want %v", caller != nil, tc.wantHook)
			}
			switch {
			case tc.wantErr == nil && err != nil:
				t.Errorf("err = %v, want none", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Errorf("err = %v, want it to match %v", err, tc.wantErr)
			}
			if tc.wantNoProv {
				if err != noProvider {
					t.Errorf("err = %v, want the one noProvider value", err)
				}
				if types.KindOf(err) != types.KindConfiguration {
					t.Errorf("kind = %v, want configuration", types.KindOf(err))
				}
				if err.Error() != "no LLM provider configured: call schemaflux.Init(key) or schemaflux.InitWithEnv(path) "+
					"and check the error, or set one of SCHEMAFLUX_API_KEY, SCHEMAFLUX_OPENAI_API_KEY, "+
					"OPENAI_API_KEY, or OPENAI in the environment" {
					t.Errorf("the text of the error changed: %q", err.Error())
				}
			}
		})
	}
}

// The one error of a call with nowhere to go has the text it always had, a
// kind, and the sentinel.
func TestNoProviderIsClassifiedAndKeepsItsText(t *testing.T) {
	if noProvider.Error() != ErrNoProvider.Error() {
		t.Errorf("text %q differs from the sentinel's %q", noProvider.Error(), ErrNoProvider.Error())
	}
	if !errors.Is(noProvider, ErrNoProvider) {
		t.Error("errors.Is(noProvider, ErrNoProvider) is false")
	}
	if got := types.KindOf(noProvider); got != types.KindConfiguration {
		t.Errorf("kind = %v, want configuration", got)
	}
}

// The three entries to a call with a context that reaches no provider answer
// with the same error: the buffered call, the stream, and the planned route.
func TestSnapshotErrIsTheNoProviderError(t *testing.T) {
	failure := types.Newf(types.KindConfiguration, "client", "the client has no provider")
	swapNoProvider(t)
	ctx := WithSharedExecConfig(context.Background(), &ExecConfig{Err: failure})

	t.Run("buffered", func(t *testing.T) {
		_, err := callLLM(ctx, "system", "user", types.OpOptions{})
		if !errors.Is(err, failure) {
			t.Errorf("callLLM err = %v, want the snapshot's", err)
		}
	})
	t.Run("streamed", func(t *testing.T) {
		options := NewSummarizeOptions()
		options.CommonOptions.Context = ctx
		_, err := StreamSummarize("text to summarise", options)
		if !errors.Is(err, failure) {
			t.Errorf("StreamSummarize err = %v, want the snapshot's", err)
		}
	})
	t.Run("planned", func(t *testing.T) {
		if _, _, known := plannedRoute(ctx, types.OpOptions{}); known {
			t.Error("a route was known for a client that cannot make a call")
		}
	})
	t.Run("a provider on the context does not mask the client's error", func(t *testing.T) {
		provider := &namedProvider{name: "context"}
		masked := WithProvider(ctx, provider)
		if _, err := callLLM(masked, "system", "user", types.OpOptions{Model: "m"}); !errors.Is(err, failure) {
			t.Errorf("callLLM err = %v, want the snapshot's", err)
		}
		if provider.calls != 0 {
			t.Errorf("the context's provider was called %d times, want none", provider.calls)
		}
	})
	t.Run("a process hook does not mask the client's error", func(t *testing.T) {
		called := false
		setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
			called = true
			return "hook", nil
		})
		if _, err := callLLM(ctx, "system", "user", types.OpOptions{Model: "m"}); !errors.Is(err, failure) || called {
			t.Errorf("callLLM err = %v, hook called = %v; want the snapshot's error and no hook", err, called)
		}
	})
	t.Run("with a provider the error is not read", func(t *testing.T) {
		withProvider := WithSharedExecConfig(context.Background(), &ExecConfig{Err: failure, Provider: &namedProvider{name: "ok"}})
		if _, err := callLLM(withProvider, "system", "user", types.OpOptions{Model: "m"}); err != nil {
			t.Errorf("callLLM err = %v", err)
		}
	})
}
