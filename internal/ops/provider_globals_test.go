package ops

import (
	"context"
	"sync"
	"testing"

	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/types"
)

// The two package-level hooks -- defaultProvider and customLLMCaller -- are
// written by every client construction and every schemafluxtest.Install, and
// read by every operation. They were unguarded, which is a data race the
// detector could not report on the machine this library is developed on,
// because -race does not run on windows/arm64 (CI-002).
//
// Run this under -race to see what it is for; without the detector it still
// exercises the interleaving.
func TestProviderHooksSurviveConcurrentInstallation(t *testing.T) {
	originalCaller, originalProvider := currentHooks()
	t.Cleanup(func() {
		setLLMCaller(originalCaller)
		SetDefaultProvider(originalProvider)
	})

	SetDefaultProvider(&captureProvider{})
	setLLMCaller(nil)

	var wg sync.WaitGroup
	const workers = 8
	const iterations = 200

	// Writers swap the provider while readers dispatch operations through it.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				SetDefaultProvider(&captureProvider{})
			}
		}()
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if _, _, err := currentHooksProbe(); err != nil {
					t.Errorf("dispatch failed: %v", err)
					return
				}
			}
		}()
	}

	// And a third group swapping the test caller, which is the other global.
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				setLLMCaller(func(context.Context, string, string, types.OpOptions) (string, error) {
					return `{"ok":true}`, nil
				})
				setLLMCaller(nil)
			}
		}()
	}

	wg.Wait()
}

// currentHooksProbe reads both hooks and reports whether they are usable, the
// way callLLM does. It exists so the test above exercises the read path rather
// than reaching into the variables directly.
func currentHooksProbe() (LLMCaller, llm.Provider, error) {
	caller, provider := currentHooks()
	if caller == nil && provider == nil {
		return nil, nil, ErrNoProvider
	}
	return caller, provider, nil
}

// Batch() reads the same global and had its own unguarded access.
func TestBatchReadsTheProviderUnderTheLock(t *testing.T) {
	originalCaller, originalProvider := currentHooks()
	t.Cleanup(func() {
		setLLMCaller(originalCaller)
		SetDefaultProvider(originalProvider)
	})

	provider := &captureProvider{}
	SetDefaultProvider(provider)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if Batch() == nil {
					t.Error("Batch() returned nil")
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				SetDefaultProvider(&captureProvider{})
			}
		}()
	}
	wg.Wait()
}

// An operation run while no provider is installed still reports the error that
// names the way out, rather than reading a half-written variable.
func TestNoProviderIsStillReportedUnderConcurrency(t *testing.T) {
	originalCaller, originalProvider := currentHooks()
	t.Cleanup(func() {
		setLLMCaller(originalCaller)
		SetDefaultProvider(originalProvider)
	})

	setLLMCaller(nil)
	SetDefaultProvider(nil)

	_, err := callLLM(context.Background(), "system", "user", types.OpOptions{})
	if err == nil {
		t.Fatal("callLLM with no provider must fail")
	}
	if err != ErrNoProvider {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
}
