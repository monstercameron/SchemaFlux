package llm

import (
	"context"
	"errors"
	"net"
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
			return "", &APIError{Provider: "p", StatusCode: 400, Message: "schema rejected"}
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

// Supported is the route having been held to the schema, not the route having
// answered. Any non-empty reply counted, so a route that ignored the schema and
// answered in prose was published as one that enforces it.
func TestCheckSchemaKeywordSupportRequiresAConformingAnswer(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"the one shape the schema allows", `{"greeting":"hello"}`, true},
		{"with surrounding whitespace", "  {\"greeting\": \"hello\"}\n", true},
		{"prose", "hello world", false},
		{"a different value", `{"greeting":"goodbye"}`, false},
		{"the right value in the wrong case", `{"greeting":"Hello"}`, false},
		{"an extra property", `{"greeting":"hello","extra":1}`, false},
		{"the wrong type", `{"greeting":1}`, false},
		{"an empty object", `{}`, false},
		{"an array", `["hello"]`, false},
		{"null", `null`, false},
		{"a bare string", `"hello"`, false},
		{"a fenced block", "```json\n{\"greeting\":\"hello\"}\n```", false},
		{"truncated", `{"greeting":"hel`, false},
		{"the value in another key", `{"salutation":"hello"}`, false},
		{"two objects", `{"greeting":"hello"}{"greeting":"hello"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) { return tc.content, nil })
			outcome := checkSchemaKeywordSupport(context.Background(), target)
			if !outcome.Passed {
				t.Fatalf("outcome = %+v; a route that answered was measured", outcome)
			}
			if outcome.Supported != tc.want {
				t.Errorf("Supported = %v, want %v", outcome.Supported, tc.want)
			}
		})
	}
}

// Any error from the live request was published as "does not support native JSON
// Schema". A throttle, an outage, a dropped connection, an expired key or a
// cancelled context is a request that was not answered, and says nothing of what
// the route does with a schema. Only the route saying the request was wrong does.
func TestCheckSchemaKeywordSupportOnlyMeasuresAnInvalidRequest(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantMeasured bool
	}{
		{"400", &APIError{StatusCode: 400}, true},
		{"422", &APIError{StatusCode: 422}, true},
		{"a typed invalid request", &types.OperationError{Kind: types.KindInvalidRequest}, true},
		{"429", &RateLimitError{APIError: &APIError{StatusCode: 429}}, false},
		{"503", &RateLimitError{APIError: &APIError{StatusCode: 503}}, false},
		{"500", &APIError{StatusCode: 500}, false},
		{"401", &APIError{StatusCode: 401}, false},
		{"403", &APIError{StatusCode: 403}, false},
		{"a cancelled context", context.Canceled, false},
		{"a deadline", context.DeadlineExceeded, false},
		{"a dropped connection", &net.OpError{Op: "read", Err: errors.New("connection reset")}, false},
		{"an unclassified error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) { return "", tc.err })
			outcome := checkSchemaKeywordSupport(context.Background(), target)
			if tc.wantMeasured {
				if !outcome.Passed || outcome.Supported {
					t.Errorf("outcome = %+v, want a measurement of no support", outcome)
				}
				return
			}
			if outcome.Passed || outcome.Supported || outcome.Skipped {
				t.Errorf("outcome = %+v, want a failed check", outcome)
			}
			if !strings.Contains(outcome.Detail, "could not be determined") {
				t.Errorf("Detail = %q, want it to say the answer could not be determined", outcome.Detail)
			}
		})
	}
}

// A check that failed to run publishes nothing: no "does not support" for a route
// that was never found to lack the capability.
func TestAFailedModelCheckPublishesNothing(t *testing.T) {
	ResetCapabilityRegistryForTest()
	t.Cleanup(ResetCapabilityRegistryForTest)

	target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
		return "", &RateLimitError{APIError: &APIError{StatusCode: 429}}
	})
	target.ProviderName = "unreached"
	report := RunConformanceSuite(context.Background(), "unreached", "model-1", ModelConformanceChecks(), target)
	if report.Publish() {
		t.Fatalf("a report whose requests all failed published capabilities: %+v", report.Capabilities().Supports)
	}
	if _, known := CapabilitiesFor("unreached", "model-1"); known {
		t.Error("the registry learned something about a route nothing was measured on")
	}
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

	// Any non-empty answer is valid UTF-8, so the check is that the probe came
	// back, not that something did.
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"the probe alone", "你好 مرحبا 👍🏽", true},
		{"the probe with words around it", "Sure: 你好 مرحبا 👍🏽 done", true},
		{"the probe on its own line", "\n你好 مرحبا 👍🏽\n", true},
		{"prose that does not repeat it", "Hello! How can I help you today?", false},
		{"part of it", "你好 مرحبا", false},
		{"the emoji without its modifier", "你好 مرحبا 👍", false},
		{"in another order", "👍🏽 مرحبا 你好", false},
		{"with the spaces lost", "你好مرحبا👍🏽", false},
		{"a refusal", "I cannot repeat that.", false},
		{"mojibake", "ä½ å¥½", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := fakeLiveTarget(t, func(ctx context.Context, req CompletionRequest) (string, error) {
				return tc.content, nil
			})
			outcome := checkUnicodeRoundtrip(context.Background(), target)
			if outcome.Passed != tc.want {
				t.Errorf("outcome = %+v, want Passed = %v", outcome, tc.want)
			}
		})
	}
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
		return "a synthetic answer repeating 你好 مرحبا 👍🏽 and CONFORMANCE-MARKER-7f3a", nil
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
		// The fake answers each check the way a conforming route does, so every
		// one passes. Not being skipped is no evidence the check ran to a verdict.
		if !outcome.Passed {
			t.Errorf("check %q did not pass against a conforming fake: %+v", check.ID, outcome)
		}
	}
}
