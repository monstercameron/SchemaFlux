package ops

import (
	"context"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// Fourteen option types embed both CommonOptions and types.OpOptions. The
// conversion used to return only the CommonOptions half, so anything set on the
// OpOptions side was silently discarded -- across the most-used operations in
// the library. Each case below sets a field on the embedded OpOptions only and
// asserts it survives the conversion.
func TestEmbeddedOpOptionsAreNotDiscarded(t *testing.T) {
	const requestID = "embedded-request-id"

	cases := []struct {
		name    string
		convert func() types.OpOptions
	}{
		{"Extract", func() types.OpOptions {
			o := NewExtractOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Transform", func() types.OpOptions {
			o := NewTransformOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Generate", func() types.OpOptions {
			o := NewGenerateOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Summarize", func() types.OpOptions {
			o := NewSummarizeOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Rewrite", func() types.OpOptions {
			o := NewRewriteOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Translate", func() types.OpOptions {
			o := NewTranslateOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Expand", func() types.OpOptions {
			o := NewExpandOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Classify", func() types.OpOptions {
			o := NewClassifyOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Score", func() types.OpOptions {
			o := NewScoreOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Compare", func() types.OpOptions {
			o := NewCompareOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Choose", func() types.OpOptions {
			o := NewChooseOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Filter", func() types.OpOptions {
			o := NewFilterOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Sort", func() types.OpOptions {
			o := NewSortOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
		{"Batch", func() types.OpOptions {
			o := NewBatchOptions()
			o.OpOptions.RequestID = requestID
			return o.toOpOptions()
		}},
	}

	if len(cases) != 14 {
		t.Fatalf("expected all 14 dual-embedded option types, have %d", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.convert().RequestID; got != requestID {
				t.Errorf("RequestID = %q, want %q -- the embedded OpOptions was discarded", got, requestID)
			}
		})
	}
}

// CommonOptions is the documented surface and must win a conflict.
func TestCommonOptionsWinsOverEmbeddedOpOptions(t *testing.T) {
	o := NewExtractOptions()
	o.CommonOptions.RequestID = "from-common"
	o.OpOptions.RequestID = "from-embedded"

	if got := o.toOpOptions().RequestID; got != "from-common" {
		t.Errorf("RequestID = %q, want the CommonOptions value", got)
	}
}

// Correlation IDs follow the same rule on both sides.
func TestCorrelationIDResolvesFromEitherSide(t *testing.T) {
	fromEmbedded := NewExtractOptions()
	fromEmbedded.OpOptions.CorrelationID = "corr-embedded"
	if got := fromEmbedded.toOpOptions().CorrelationID; got != "corr-embedded" {
		t.Errorf("embedded CorrelationID = %q, want corr-embedded", got)
	}

	fromCommon := NewExtractOptions()
	fromCommon.CommonOptions.CorrelationID = "corr-common"
	fromCommon.OpOptions.CorrelationID = "corr-embedded"
	if got := fromCommon.toOpOptions().CorrelationID; got != "corr-common" {
		t.Errorf("CorrelationID = %q, want the CommonOptions value", got)
	}
}

// A context set on either side must reach the operation, because a context that
// is dropped is a cancellation that never arrives.
func TestContextResolvesFromEitherSide(t *testing.T) {
	type key struct{}

	t.Run("embedded_only", func(t *testing.T) {
		o := NewExtractOptions()
		o.OpOptions.Context = context.WithValue(context.Background(), key{}, "embedded")
		if got := o.toOpOptions().Context.Value(key{}); got != "embedded" {
			t.Errorf("context value = %v, want embedded -- the embedded context was dropped", got)
		}
	})

	t.Run("common_wins", func(t *testing.T) {
		o := NewExtractOptions()
		o.CommonOptions.Context = context.WithValue(context.Background(), key{}, "common")
		o.OpOptions.Context = context.WithValue(context.Background(), key{}, "embedded")
		if got := o.toOpOptions().Context.Value(key{}); got != "common" {
			t.Errorf("context value = %v, want common", got)
		}
	})

	t.Run("neither_set_is_never_nil", func(t *testing.T) {
		if ctx := NewExtractOptions().toOpOptions().Context; ctx == nil {
			t.Error("the resolved context must never be nil")
		}
	})
}

// Steering and Threshold merge with CommonOptions taking precedence when set.
func TestSteeringAndThresholdMerge(t *testing.T) {
	embedded := NewExtractOptions()
	embedded.OpOptions.Steering = "from embedded"
	embedded.OpOptions.Threshold = 0.4
	got := embedded.toOpOptions()
	if got.Steering != "from embedded" {
		t.Errorf("Steering = %q, want the embedded value", got.Steering)
	}
	if got.Threshold != 0.4 {
		t.Errorf("Threshold = %v, want 0.4", got.Threshold)
	}

	both := NewExtractOptions()
	both.OpOptions.Steering = "from embedded"
	both.CommonOptions.Steering = "from common"
	if got := both.toOpOptions().Steering; got != "from common" {
		t.Errorf("Steering = %q, want the CommonOptions value", got)
	}
}

// A request ID is always produced, generated when neither side supplies one, so
// every call remains traceable.
func TestRequestIDIsAlwaysPopulated(t *testing.T) {
	if got := NewExtractOptions().toOpOptions().RequestID; got == "" {
		t.Error("a request ID must always be generated")
	}
}
