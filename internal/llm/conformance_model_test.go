package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// conformance_model.go's checks are gated on a *LiveTarget carrying a real
// Provider, but the Provider is just an interface -- LiveTarget's own doc
// comment says nothing requires it to be a real vendor route. A LocalProvider
// with a scripted WithHandler makes zero network calls (Complete never
// touches the network for LocalProvider -- see provider.go), so these drive
// every checkXxx function end to end without SCHEMAFLUX_LIVE_TESTS.

func fakeLiveTarget(t *testing.T, handler func(context.Context, CompletionRequest) (string, error)) *LiveTarget {
	t.Helper()
	provider, err := NewLocalProvider(ProviderConfig{})
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	provider.WithHandler(handler)
	return &LiveTarget{ProviderName: "local", Model: "conformance-fake-model", Provider: provider}
}

// TestModelCheckPreconditions proves the shared guard refuses a nil
// LiveTarget and a LiveTarget with a nil Provider, and lets a well-formed one
// through (nil result).
func TestModelCheckPreconditions(t *testing.T) {
	if bad := modelCheckPreconditions(nil); bad == nil || bad.Passed {
		t.Errorf("modelCheckPreconditions(nil) = %+v, want a failed outcome", bad)
	}
	if bad := modelCheckPreconditions(&LiveTarget{}); bad == nil || bad.Passed {
		t.Errorf("modelCheckPreconditions(no provider) = %+v, want a failed outcome", bad)
	}

	target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
		return "ok", nil
	})
	if bad := modelCheckPreconditions(target); bad != nil {
		t.Errorf("modelCheckPreconditions(well-formed target) = %+v, want nil", bad)
	}
}

// TestClassifyDetailDescribesOnlyTheKind proves classifyDetail never leaks
// the underlying error's own message -- only Classify's taxonomy label --
// which is the whole point of the "never log the caller's payload" rule
// applied to this suite's own synthetic fixtures.
func TestClassifyDetailDescribesOnlyTheKind(t *testing.T) {
	err := errors.New("a message that must never appear in the detail: secret-marker-9f2c")
	got := classifyDetail(err)
	if strings.Contains(got, "secret-marker-9f2c") {
		t.Errorf("classifyDetail leaked the underlying error message: %q", got)
	}
	if got != types.KindUnknown.String() {
		t.Errorf("classifyDetail(unclassifiable) = %q, want %q", got, types.KindUnknown.String())
	}
}

func TestCheckSchemaKeywordSupport(t *testing.T) {
	t.Run("no live target", func(t *testing.T) {
		outcome := checkSchemaKeywordSupport(context.Background(), nil)
		if outcome.Passed {
			t.Error("passed with no live target")
		}
	})

	t.Run("route honours the schema and answers content", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return `{"greeting":"hello"}`, nil
		})
		outcome := checkSchemaKeywordSupport(context.Background(), target)
		if !outcome.Passed || !outcome.Supported {
			t.Errorf("outcome = %+v, want Passed and Supported", outcome)
		}
	})

	t.Run("route rejects the schema-constrained request", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "", errors.New("schema rejected")
		})
		outcome := checkSchemaKeywordSupport(context.Background(), target)
		if !outcome.Passed || outcome.Supported {
			t.Errorf("outcome = %+v, want Passed=true (measured) but Supported=false", outcome)
		}
	})

	t.Run("route answers empty content", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "", nil
		})
		outcome := checkSchemaKeywordSupport(context.Background(), target)
		if !outcome.Passed || outcome.Supported {
			t.Errorf("outcome = %+v, want Passed=true, Supported=false for empty content", outcome)
		}
	})
}

func TestCheckUsageReporting(t *testing.T) {
	t.Run("no live target", func(t *testing.T) {
		if outcome := checkUsageReporting(context.Background(), nil); outcome.Passed {
			t.Error("passed with no live target")
		}
	})

	t.Run("request fails", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "", errors.New("boom")
		})
		outcome := checkUsageReporting(context.Background(), target)
		if outcome.Passed {
			t.Error("a failed request must not report a passed check")
		}
	})

	t.Run("non-zero usage is supported", func(t *testing.T) {
		// LocalProvider.Complete derives usage from prompt/content length, so
		// any non-empty prompt and content produces non-zero usage.
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "ready", nil
		})
		outcome := checkUsageReporting(context.Background(), target)
		if !outcome.Passed || !outcome.Supported {
			t.Errorf("outcome = %+v, want Passed and Supported for non-zero usage", outcome)
		}
	})
}

func TestCheckUnicodeRoundtrip(t *testing.T) {
	t.Run("no live target", func(t *testing.T) {
		if outcome := checkUnicodeRoundtrip(context.Background(), nil); outcome.Passed {
			t.Error("passed with no live target")
		}
	})

	t.Run("request fails", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "", errors.New("boom")
		})
		if outcome := checkUnicodeRoundtrip(context.Background(), target); outcome.Passed {
			t.Error("a failed request must not report a passed check")
		}
	})

	t.Run("empty response content fails", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "", nil
		})
		if outcome := checkUnicodeRoundtrip(context.Background(), target); outcome.Passed {
			t.Error("an empty response must not pass the round-trip check")
		}
	})

	t.Run("valid utf8 content passes", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "你好 مرحبا 👍🏽", nil
		})
		outcome := checkUnicodeRoundtrip(context.Background(), target)
		if !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed for valid UTF-8 content", outcome)
		}
	})
}

func TestCheckLargePayload(t *testing.T) {
	t.Run("no live target", func(t *testing.T) {
		if outcome := checkLargePayload(context.Background(), nil); outcome.Passed {
			t.Error("passed with no live target")
		}
	})

	t.Run("request fails", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "", errors.New("boom")
		})
		if outcome := checkLargePayload(context.Background(), target); outcome.Passed {
			t.Error("a failed large-payload request must not pass")
		}
	})

	t.Run("marker missing from the answer fails", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "the answer is elsewhere", nil
		})
		if outcome := checkLargePayload(context.Background(), target); outcome.Passed {
			t.Error("a response missing the marker must not pass")
		}
	})

	t.Run("marker recovered passes", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "the secret word is CONFORMANCE-MARKER-7f3a", nil
		})
		outcome := checkLargePayload(context.Background(), target)
		if !outcome.Passed {
			t.Errorf("outcome = %+v, want Passed when the marker survives", outcome)
		}
	})
}

func TestCheckReasoningCachedTokenNormalization(t *testing.T) {
	t.Run("no live target", func(t *testing.T) {
		if outcome := checkReasoningCachedTokenNormalization(context.Background(), nil); outcome.Passed {
			t.Error("passed with no live target")
		}
	})

	t.Run("request fails", func(t *testing.T) {
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "", errors.New("boom")
		})
		if outcome := checkReasoningCachedTokenNormalization(context.Background(), target); outcome.Passed {
			t.Error("a failed request must not report a passed check")
		}
	})

	t.Run("zero reasoning and cached tokens is a passing, unsupported measurement", func(t *testing.T) {
		// LocalProvider never populates ReasoningTokens/CachedTokens, so this
		// exercises the documented "zero is a valid, normalized absence" path.
		target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
			return "ready", nil
		})
		outcome := checkReasoningCachedTokenNormalization(context.Background(), target)
		if !outcome.Passed || outcome.Supported {
			t.Errorf("outcome = %+v, want Passed=true, Supported=false for zero reasoning/cached tokens", outcome)
		}
	})
}

// TestModelConformanceChecksRunEndToEndAgainstAFake runs every check
// ModelConformanceChecks declares against one fake live target, proving the
// registered Run functions actually match the checkXxx functions above (not
// just that the individually-named functions work in isolation).
func TestModelConformanceChecksRunEndToEndAgainstAFake(t *testing.T) {
	checks := ModelConformanceChecks()
	target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
		if req.ResponseFormat == "json" {
			return `{"greeting":"hello"}`, nil
		}
		return "a synthetic answer containing 你好 and CONFORMANCE-MARKER-7f3a", nil
	})

	report := RunConformanceSuite(context.Background(), "local", "conformance-fake-model", checks, target)
	for _, check := range checks {
		outcome, ok := report.Results[check.ID]
		if !ok {
			t.Errorf("check %q produced no result", check.ID)
			continue
		}
		if outcome.Skipped {
			t.Errorf("check %q skipped despite a live target being supplied", check.ID)
		}
	}
}
