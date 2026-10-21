package types

import (
	"encoding/json"
	"testing"
	"time"
)

// The response facts and the attempt marker are new fields whose zero value is
// the behaviour before they existed: the encoded record of a call that sets
// none of them is the same bytes, and the one-line summary of a Meta does not
// mention them.

func TestResultMetadataEncodesNoNewKeyWhileTheNewFieldsAreZero(t *testing.T) {
	zero, err := json.Marshal(ResultMetadata{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(zero, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"service_tier", "hedged", "finish_reason", "usage_only"} {
		if _, present := decoded[key]; present {
			t.Errorf("the zero record encodes %q", key)
		}
	}
}

func TestResultMetadataEncodesEachNewFieldWhenItIsSet(t *testing.T) {
	cases := []struct {
		name string
		meta ResultMetadata
		key  string
		want any
	}{
		{"the service tier", ResultMetadata{ServiceTier: "priority"}, "service_tier", "priority"},
		{"a flex service tier", ResultMetadata{ServiceTier: "flex"}, "service_tier", "flex"},
		{"a hedged request", ResultMetadata{Hedged: true}, "hedged", true},
		{"a finish reason of stop", ResultMetadata{FinishReason: "stop"}, "finish_reason", "stop"},
		{"a finish reason of length", ResultMetadata{FinishReason: "length"}, "finish_reason", "length"},
		{"a content filter finish reason", ResultMetadata{FinishReason: "content_filter"}, "finish_reason", "content_filter"},
		{"a usage-only record", ResultMetadata{UsageOnly: true}, "usage_only", true},
		{"a usage-only record that also names a tier", ResultMetadata{UsageOnly: true, ServiceTier: "auto"}, "usage_only", true},
		{"a hedged record that also names a finish reason", ResultMetadata{Hedged: true, FinishReason: "stop"}, "hedged", true},
		{"a record with every new field set, the tier", ResultMetadata{ServiceTier: "t", Hedged: true, FinishReason: "f", UsageOnly: true}, "service_tier", "t"},
		{"a record with every new field set, the reason", ResultMetadata{ServiceTier: "t", Hedged: true, FinishReason: "f", UsageOnly: true}, "finish_reason", "f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.meta)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := decoded[tc.key]; got != tc.want {
				t.Errorf("%s = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestMetaSummaryAndFactsIgnoreTheNewFieldsWhileTheyAreZero(t *testing.T) {
	base := Meta{
		Operation: "extract", Provider: "openai", Model: "m",
		Attempts: 2, Repairs: 1, Elapsed: 1500 * time.Millisecond,
		Usage:             TokenUsage{TotalTokens: 42},
		DeliveredContract: ContractSchemaConstrained, RequestedContract: ContractSchemaConstrained,
	}
	withFacts := base
	withFacts.Retries = 3
	withFacts.CacheHits = 1
	withFacts.Hedged = true
	withFacts.ServiceTier = "priority"

	if base.String() != withFacts.String() {
		t.Errorf("the summary mentions the new fields:\n%s\n%s", base.String(), withFacts.String())
	}
	if base.Degraded() != withFacts.Degraded() {
		t.Errorf("Degraded depends on the new fields")
	}
	if len(withFacts.Failed()) != 0 {
		t.Errorf("Failed reports checks nobody recorded")
	}

	var zero Meta
	if zero.Retries != 0 || zero.CacheHits != 0 || zero.Hedged || zero.ServiceTier != "" {
		t.Errorf("the zero Meta is not zero in the new fields: %+v", zero)
	}
}

func TestMetaFromLeavesTheNewFactsZero(t *testing.T) {
	cases := []struct {
		name string
		meta *ResultMetadata
	}{
		{"a nil record", nil},
		{"an empty record", &ResultMetadata{}},
		{"a record of a clean call", &ResultMetadata{RequestID: "r", Model: "m", Provider: "p", RetryCount: 0}},
		{"a record that was retried", &ResultMetadata{RequestID: "r", RetryCount: 2}},
		{"a record with a service tier", &ResultMetadata{ServiceTier: "priority"}},
		{"a hedged record", &ResultMetadata{Hedged: true}},
		{"a cached record", &ResultMetadata{CacheHit: true}},
		{"a record with a finish reason", &ResultMetadata{FinishReason: "stop"}},
		{"a usage-only record", &ResultMetadata{UsageOnly: true, TokenUsage: &TokenUsage{TotalTokens: 9}}},
		{"a record naming a model and its observed one", &ResultMetadata{RequestedModel: "a", ObservedModel: "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MetaFrom(tc.meta)
			if got.Retries != 0 || got.CacheHits != 0 || got.Hedged || got.ServiceTier != "" {
				t.Errorf("MetaFrom filled the new Meta fields: %+v", got)
			}
		})
	}
}

func TestOperationErrorNarrowIsZeroForEveryConstructor(t *testing.T) {
	cause := Newf(KindTimeout, "call", "slow")
	cases := []struct {
		name string
		err  *OperationError
	}{
		{"Newf", Newf(KindConfiguration, "op", "bad %s", "option")},
		{"Wrap", Wrap(KindTimeout, "op", cause)},
		{"a literal with a kind", &OperationError{Kind: KindPolicyViolation}},
		{"a literal with everything else set", &OperationError{Kind: KindRateLimited, Op: "o", Provider: "p", Model: "m", RequestID: "r", Message: "x", Ambiguous: true}},
		{"a literal with a cause", &OperationError{Kind: KindProviderUnavailable, Cause: cause}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Narrow != NarrowNone {
				t.Errorf("Narrow = %d, want %d", tc.err.Narrow, NarrowNone)
			}
			marked := *tc.err
			marked.Narrow = NarrowPanic
			if marked.Error() != tc.err.Error() {
				t.Errorf("Narrow changed the text: %q vs %q", marked.Error(), tc.err.Error())
			}
			if marked.Retryable() != tc.err.Retryable() || marked.Repairable() != tc.err.Repairable() || marked.Terminal() != tc.err.Terminal() {
				t.Errorf("Narrow changed a disposition")
			}
		})
	}
}
