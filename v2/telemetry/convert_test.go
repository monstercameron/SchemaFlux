package telemetry

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/types"
	wrapped "github.com/monstercameron/schemaflux/telemetry"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

func TestBoundKeepsWhatIsAlreadyFitAndCutsAndCleansTheRest(t *testing.T) {
	long := strings.Repeat("x", 20000)
	cases := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{"a generated id", "req_ba09dc8ced0b338b", 128, "req_ba09dc8ced0b338b"},
		{"empty", "", 128, ""},
		{"exactly the limit", strings.Repeat("a", 128), 128, strings.Repeat("a", 128)},
		{"one over the limit", strings.Repeat("a", 129), 128, strings.Repeat("a", 128)},
		{"twenty thousand bytes", long, 64, long[:64]},
		{"a newline", "corr\nforged line", 128, "corr?forged line"},
		{"a NUL", "corr\x00id", 128, "corr?id"},
		{"a tab and a carriage return", "a\tb\rc", 128, "a?b?c"},
		{"an escape sequence", "id\x1b[31mred", 128, "id?[31mred"},
		{"a delete character", "id\x7f", 128, "id?"},
		{"non-ASCII text", "café", 128, "caf??"},
		{"invalid UTF-8", "id\xff\xfe", 128, "id??"},
		{"spaces and punctuation are kept", "user 42 / order #7", 128, "user 42 / order #7"},
		{"long and unprintable", strings.Repeat("\n", 300), 128, strings.Repeat("?", 128)},
		{"a multi-byte character across the limit", strings.Repeat("a", 127) + "é", 128, strings.Repeat("a", 127) + "?"},
		{"a model name as a provider writes it", "accounts/fireworks/models/llama-v3p1-70b-instruct:free", 128, "accounts/fireworks/models/llama-v3p1-70b-instruct:free"},
		{"a limit of zero", "anything", 0, ""},
		{"a negative limit", "anything", -3, ""},
		{"a limit of one", "anything", 1, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bound(tc.value, tc.limit)
			if got != tc.want {
				t.Errorf("bound = %q, want %q", got, tc.want)
			}
			for index := 0; index < len(got); index++ {
				if got[index] < 0x20 || got[index] > 0x7e {
					t.Fatalf("byte %d is %#x, not printable", index, got[index])
				}
			}
		})
	}
}

func TestKindFromNameMapsEveryNameTheLibraryEmitsAndNothingElse(t *testing.T) {
	cases := []struct {
		name string
		want schemaflux.Kind
	}{
		{"", 0},
		{"configuration", schemaflux.ErrConfiguration},
		{"invalid request", schemaflux.ErrInvalidRequest},
		{"unsupported capability", schemaflux.ErrUnsupported},
		{"authentication", schemaflux.ErrUnauthorized},
		{"permission", schemaflux.ErrUnauthorized},
		{"policy violation", schemaflux.ErrPolicy},
		{"budget exceeded", schemaflux.ErrBudgetExceeded},
		{"admission rejected", schemaflux.ErrBusy},
		{"rate limited", schemaflux.ErrRateLimited},
		{"provider unavailable", schemaflux.ErrUnavailable},
		{"circuit open", schemaflux.ErrUnavailable},
		{"timeout", schemaflux.ErrTimeout},
		{"canceled", schemaflux.ErrCanceled},
		{"shutdown", schemaflux.ErrClosed},
		{"context too large", schemaflux.ErrTooLarge},
		{"output truncated", schemaflux.ErrTruncated},
		{"malformed output", schemaflux.ErrInvalidAnswer},
		{"schema violation", schemaflux.ErrInvalidAnswer},
		{"batch protocol violation", schemaflux.ErrInvalidAnswer},
		{"invariant violation", schemaflux.ErrInvalidAnswer},
		{"evidence violation", schemaflux.ErrInvalidAnswer},
		{"repair exhausted", schemaflux.ErrInvalidAnswer},
		{"review required", schemaflux.ErrReviewRequired},
		{wrapped.ErrorKindInternal, schemaflux.ErrInternal},
		{wrapped.ErrorKindRefused, schemaflux.ErrRefused},
		{"refused", schemaflux.ErrRefused},
		{"Refused", schemaflux.ErrInternal},
		{"unknown", schemaflux.ErrInternal},
		{"a kind added after this was written", schemaflux.ErrInternal},
		{"Rate Limited", schemaflux.ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := kindFromName(tc.name); got != tc.want {
				t.Errorf("kindFromName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// Every kind the library classifies a failure as is given the kind the root
// gives the same failure when a call returns it, so a span, an event and the
// *Error a caller holds agree. The root's own conversion is the reference, and a
// kind added to the library without a place here fails this.
func TestEveryInternalKindIsGivenTheKindTheRootGivesTheSameFailure(t *testing.T) {
	seen := 0
	for kind := types.KindConfiguration; kind.String() != types.KindUnknown.String(); kind++ {
		seen++
		converted := bind.ConvertError("probe", types.Newf(kind, "probe", "a message"))
		var root *schemaflux.Error
		if !errors.As(converted, &root) {
			t.Fatalf("the root's conversion of %q is %T, not an *Error", kind, converted)
		}
		if got := kindFromName(kind.String()); got != root.Kind {
			t.Errorf("kind %q: the event says %v, the root's *Error says %v", kind, got, root.Kind)
		}
	}
	if seen < 23 {
		t.Errorf("only %d internal kinds were enumerated; the loop stopped early", seen)
	}
}

// The names that stand for a kind and a mark are given the Kind the root's one
// conversion (bind.KindOf) gives that pair, and an unmarked kind is given the
// Kind it has without a mark: this package holds no table of its own.
func TestTheNamesOfMarkedFailuresAreGivenTheKindTheRootGivesTheMarkedError(t *testing.T) {
	cases := []struct {
		name string
		kind types.ErrorKind
		mark types.Narrowing
	}{
		{wrapped.ErrorKindRefused, types.KindPolicyViolation, types.NarrowRefusal},
		{wrapped.ErrorKindInternal, types.KindInvariantViolation, types.NarrowPanic},
		{types.KindPolicyViolation.String(), types.KindPolicyViolation, types.NarrowNone},
		{types.KindInvariantViolation.String(), types.KindInvariantViolation, types.NarrowNone},
		{types.KindTimeout.String(), types.KindTimeout, types.NarrowNone},
		{types.KindCircuitOpen.String(), types.KindCircuitOpen, types.NarrowNone},
		{types.KindAuthentication.String(), types.KindAuthentication, types.NarrowNone},
		{types.KindPermission.String(), types.KindPermission, types.NarrowNone},
		{types.KindRepairExhausted.String(), types.KindRepairExhausted, types.NarrowNone},
		{types.KindShutdown.String(), types.KindShutdown, types.NarrowNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := schemaflux.Kind(bind.KindOf(tc.kind, tc.mark))
			if want == 0 {
				t.Fatalf("the root gives no kind for %v with mark %d", tc.kind, tc.mark)
			}
			if got := kindFromName(tc.name); got != want {
				t.Errorf("kindFromName(%q) = %v, the root's kind is %v", tc.name, got, want)
			}
		})
	}
}

func TestEveryVersion2KindIsReachableFromAnEventName(t *testing.T) {
	reached := map[schemaflux.Kind]bool{}
	for kind := types.KindConfiguration; kind.String() != types.KindUnknown.String(); kind++ {
		reached[kindFromName(kind.String())] = true
	}
	// A refusal is a policy violation marked as one, and an event names it
	// "refused" and not by its internal kind; a panic is named "internal".
	reached[kindFromName(wrapped.ErrorKindRefused)] = true
	reached[kindFromName(wrapped.ErrorKindInternal)] = true
	for kind := schemaflux.ErrConfiguration; kind.Error() != "unknown"; kind++ {
		if !reached[kind] {
			t.Errorf("no event name maps to %v", kind)
		}
	}
}

func TestModeAndTierAreReadFromTheNamesTheLibraryEmits(t *testing.T) {
	modes := []struct {
		name string
		want schemaflux.Mode
	}{
		{types.Strict.String(), schemaflux.ModeStrict},
		{types.TransformMode.String(), schemaflux.ModeTransform},
		{types.Creative.String(), schemaflux.ModeCreative},
		{types.ModeUnset.String(), 0},
		{"", 0},
		{"unknown", 0},
		{"STRICT", 0},
		{"strict ", 0},
		{"a mode added later", 0},
		{"transform\n", 0},
	}
	for _, tc := range modes {
		if got := modeFromName(tc.name); got != tc.want {
			t.Errorf("modeFromName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	tiers := []struct {
		name string
		want schemaflux.Tier
	}{
		{types.Smart.String(), schemaflux.TierSmart},
		{types.Fast.String(), schemaflux.TierFast},
		{types.Quick.String(), schemaflux.TierQuick},
		{types.TierUnset.String(), 0},
		{"", 0},
		{"unknown", 0},
		{"SMART", 0},
		{" fast", 0},
		{"a tier added later", 0},
		{"quick\x00", 0},
	}
	for _, tc := range tiers {
		if got := tierFromName(tc.name); got != tc.want {
			t.Errorf("tierFromName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUsageIsTheProvidersCountUnderEitherPairOfNames(t *testing.T) {
	cases := []struct {
		name string
		in   types.TokenUsage
		want schemaflux.Usage
	}{
		{"nothing reported", types.TokenUsage{}, schemaflux.Usage{}},
		{"prompt and completion", types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
			schemaflux.Usage{Input: 100, Output: 30, Total: 130}},
		{"input and output only", types.TokenUsage{InputTokens: 9, OutputTokens: 4},
			schemaflux.Usage{Input: 9, Output: 4, Total: 13}},
		{"both pairs, the first wins", types.TokenUsage{PromptTokens: 5, CompletionTokens: 3, InputTokens: 9, OutputTokens: 4, TotalTokens: 8},
			schemaflux.Usage{Input: 5, Output: 3, Total: 8}},
		{"a total the provider gave is kept", types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 99},
			schemaflux.Usage{Input: 10, Output: 5, Total: 99}},
		{"a missing total is the sum", types.TokenUsage{PromptTokens: 10, CompletionTokens: 5},
			schemaflux.Usage{Input: 10, Output: 5, Total: 15}},
		{"cached, written and reasoning", types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130, CachedTokens: 40, CacheWriteTokens: 12, ReasoningTokens: 7},
			schemaflux.Usage{Input: 100, Output: 30, Total: 130, Cached: 40, CacheWrite: 12, Reasoning: 7}},
		{"only a prompt", types.TokenUsage{PromptTokens: 7}, schemaflux.Usage{Input: 7, Total: 7}},
		{"only a completion", types.TokenUsage{CompletionTokens: 7}, schemaflux.Usage{Output: 7, Total: 7}},
		{"cached with nothing else", types.TokenUsage{CachedTokens: 3}, schemaflux.Usage{Cached: 3}},
		{"input names with a total", types.TokenUsage{InputTokens: 2, OutputTokens: 2, TotalTokens: 4},
			schemaflux.Usage{Input: 2, Output: 2, Total: 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := usageFrom(tc.in); got != tc.want {
				t.Errorf("usageFrom = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCostKeepsWhatWasPricedAndSaysWhatWasNot(t *testing.T) {
	cases := []struct {
		name string
		in   types.CostInfo
		want schemaflux.Cost
	}{
		{"never worked out", types.CostInfo{}, schemaflux.Cost{}},
		{"priced exactly", types.CostInfo{TotalCost: 0.0042, PromptCost: 0.003, CompletionCost: 0.0012, Currency: "USD", Priced: true, Quality: types.PricingExact, PricingSource: "gpt-5"},
			schemaflux.Cost{Total: 0.0042, Input: 0.003, Output: 0.0012, Currency: "USD", Priced: true, Source: "gpt-5"}},
		{"unpriced", types.CostInfo{Currency: "USD", Quality: types.PricingUnknown},
			schemaflux.Cost{Currency: "USD"}},
		{"estimated", types.CostInfo{TotalCost: 1, Currency: "USD", Priced: true, Quality: types.PricingEstimated},
			schemaflux.Cost{Total: 1, Currency: "USD", Priced: true, Estimated: true}},
		{"free", types.CostInfo{Currency: "USD", Priced: true, Quality: types.PricingFree},
			schemaflux.Cost{Currency: "USD", Priced: true}},
		{"cached and reasoning", types.CostInfo{TotalCost: 2, CachedCost: 0.5, ReasoningCost: 0.25, Currency: "USD", Priced: true},
			schemaflux.Cost{Total: 2, Cached: 0.5, Reasoning: 0.25, Currency: "USD", Priced: true}},
		{"a price per token is not carried", types.CostInfo{PricePerPromptToken: 1, PricePerCompletionToken: 2},
			schemaflux.Cost{}},
		{"billing identifiers are not carried", types.CostInfo{BillingPeriod: "2026-10", OrganizationID: "org", ProjectID: "proj"},
			schemaflux.Cost{}},
		{"a currency with no price", types.CostInfo{Currency: "EUR"}, schemaflux.Cost{Currency: "EUR"}},
		{"a source with no price", types.CostInfo{PricingSource: "m"}, schemaflux.Cost{Source: "m"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := costFrom(tc.in); got != tc.want {
				t.Errorf("costFrom = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestScopeIsReadFromTheLibrarysScope(t *testing.T) {
	cases := []struct {
		in   wrapped.Scope
		want Scope
	}{
		{wrapped.ScopeRequest, ScopeRequest},
		{wrapped.ScopeOperation, ScopeOperation},
		{wrapped.Scope(2), ScopeRequest},
		{wrapped.Scope(255), ScopeRequest},
	}
	for _, tc := range cases {
		if got := scopeFrom(tc.in); got != tc.want {
			t.Errorf("scopeFrom(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		scope Scope
		want  string
	}{{ScopeRequest, "request"}, {ScopeOperation, "operation"}, {Scope(9), "request"}} {
		if got := tc.scope.String(); got != tc.want {
			t.Errorf("Scope(%d).String() = %q, want %q", tc.scope, got, tc.want)
		}
	}
	if ScopeRequest != 0 {
		t.Error("ScopeRequest is not the zero value")
	}
	for _, scope := range []wrapped.Scope{wrapped.ScopeRequest, wrapped.ScopeOperation} {
		if scopeFrom(scope).String() != scope.String() {
			t.Errorf("scope %v is %q here and %q in the package it wraps", scope, scopeFrom(scope), scope)
		}
	}
}

func TestAnEventCarriesTheRequestsSettingsBoundedAndPrintable(t *testing.T) {
	hostile := "h\n" + strings.Repeat("x", 5000) + "\x00" + strings.Repeat("y", 300)
	full := wrapped.OperationEvent{
		Scope: wrapped.ScopeRequest, Operation: "extract", Provider: "openai", Model: "gpt-5.6-luna",
		RequestID: "req_1", CorrelationID: "corr_1", Mode: "strict", Intelligence: "smart",
		SchemaName: "invoice", ResponseFormat: "json", Repair: true, Stream: true,
	}
	cases := []struct {
		name   string
		mutate func(e *wrapped.OperationEvent)
		check  func(t *testing.T, got Event)
	}{
		{"every field", func(*wrapped.OperationEvent) {}, func(t *testing.T, got Event) {
			want := Event{Scope: ScopeRequest, Operation: "extract", Provider: "openai", Model: "gpt-5.6-luna",
				RequestID: "req_1", CorrelationID: "corr_1", Mode: schemaflux.ModeStrict, Tier: schemaflux.TierSmart,
				SchemaName: "invoice", ResponseFormat: "json", Repair: true, Stream: true}
			if got != want {
				t.Errorf("event = %+v, want %+v", got, want)
			}
		}},
		{"the zero event", func(e *wrapped.OperationEvent) { *e = wrapped.OperationEvent{} }, func(t *testing.T, got Event) {
			if got != (Event{}) {
				t.Errorf("event = %+v, want the zero Event", got)
			}
		}},
		{"an operation's event", func(e *wrapped.OperationEvent) {
			*e = wrapped.OperationEvent{Scope: wrapped.ScopeOperation, Operation: "sort"}
		}, func(t *testing.T, got Event) {
			if got != (Event{Scope: ScopeOperation, Operation: "sort"}) {
				t.Errorf("event = %+v", got)
			}
		}},
		{"a hostile request id", func(e *wrapped.OperationEvent) { e.RequestID = hostile }, func(t *testing.T, got Event) { limited(t, "RequestID", got.RequestID, 128) }},
		{"a hostile correlation id", func(e *wrapped.OperationEvent) { e.CorrelationID = hostile }, func(t *testing.T, got Event) { limited(t, "CorrelationID", got.CorrelationID, 128) }},
		{"a hostile model", func(e *wrapped.OperationEvent) { e.Model = hostile }, func(t *testing.T, got Event) { limited(t, "Model", got.Model, 128) }},
		{"a hostile schema name", func(e *wrapped.OperationEvent) { e.SchemaName = hostile }, func(t *testing.T, got Event) { limited(t, "SchemaName", got.SchemaName, 128) }},
		{"a hostile provider", func(e *wrapped.OperationEvent) { e.Provider = hostile }, func(t *testing.T, got Event) { limited(t, "Provider", got.Provider, 64) }},
		{"a hostile operation", func(e *wrapped.OperationEvent) { e.Operation = hostile }, func(t *testing.T, got Event) { limited(t, "Operation", got.Operation, 64) }},
		{"a hostile response format", func(e *wrapped.OperationEvent) { e.ResponseFormat = hostile }, func(t *testing.T, got Event) { limited(t, "ResponseFormat", got.ResponseFormat, 64) }},
		{"a mode and a tier nobody knows", func(e *wrapped.OperationEvent) { e.Mode, e.Intelligence = "wild", "huge" }, func(t *testing.T, got Event) {
			if got.Mode != 0 || got.Tier != 0 {
				t.Errorf("mode %v and tier %v, want neither", got.Mode, got.Tier)
			}
		}},
		{"the library's own unset words", func(e *wrapped.OperationEvent) { e.Mode, e.Intelligence = "unset", "unset" }, func(t *testing.T, got Event) {
			if got.Mode != 0 || got.Tier != 0 {
				t.Errorf("mode %v and tier %v, want neither", got.Mode, got.Tier)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := full
			tc.mutate(&event)
			tc.check(t, eventFrom(event))
		})
	}
}

// limited checks that value is printable and no longer than limit, and that it
// is not empty, since the hostile inputs are not.
func limited(t *testing.T, field, value string, limit int) {
	t.Helper()
	if value == "" || len(value) > limit {
		t.Errorf("%s is %d bytes, want 1 to %d", field, len(value), limit)
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			t.Errorf("%s holds the unprintable byte %#x at %d", field, value[index], index)
			return
		}
	}
}

func TestAResultCarriesWhatTheUnitDidAndHowItEnded(t *testing.T) {
	hostile := "h\n" + strings.Repeat("x", 5000) + "\x00"
	answered := wrapped.OperationResult{
		Scope: wrapped.ScopeRequest, Operation: "extract", Duration: 80 * time.Millisecond, ProviderDuration: 60 * time.Millisecond,
		Attempts: 2, Provider: "openai", Model: "gpt-5.6-luna-2026-08-01", FinishReason: "stop",
		Usage: types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
		Cost:  types.CostInfo{TotalCost: 0.0042, Currency: "USD", Priced: true},
	}
	cases := []struct {
		name   string
		mutate func(r *wrapped.OperationResult)
		check  func(t *testing.T, got Result)
	}{
		{"an answered request", func(*wrapped.OperationResult) {}, func(t *testing.T, got Result) {
			want := Result{Scope: ScopeRequest, Operation: "extract", Duration: 80 * time.Millisecond, ProviderDuration: 60 * time.Millisecond,
				Attempts: 2, Provider: "openai", Model: "gpt-5.6-luna-2026-08-01", FinishReason: "stop",
				Usage: schemaflux.Usage{Input: 100, Output: 30, Total: 130},
				Cost:  schemaflux.Cost{Total: 0.0042, Currency: "USD", Priced: true}}
			if got != want {
				t.Errorf("result = %+v, want %+v", got, want)
			}
		}},
		{"the zero result", func(r *wrapped.OperationResult) { *r = wrapped.OperationResult{} }, func(t *testing.T, got Result) {
			if got != (Result{}) {
				t.Errorf("result = %+v, want the zero Result", got)
			}
		}},
		{"a call that succeeded has no kind", func(*wrapped.OperationResult) {}, func(t *testing.T, got Result) {
			if got.Kind != 0 {
				t.Errorf("kind = %v on a success", got.Kind)
			}
		}},
		{"a rate limit", func(r *wrapped.OperationResult) { r.ErrorKind = "rate limited" }, func(t *testing.T, got Result) {
			if got.Kind != schemaflux.ErrRateLimited {
				t.Errorf("kind = %v", got.Kind)
			}
		}},
		{"a refused answer", func(r *wrapped.OperationResult) { r.ErrorKind = "repair exhausted" }, func(t *testing.T, got Result) {
			if got.Kind != schemaflux.ErrInvalidAnswer {
				t.Errorf("kind = %v", got.Kind)
			}
		}},
		{"a panic", func(r *wrapped.OperationResult) { r.ErrorKind = wrapped.ErrorKindInternal }, func(t *testing.T, got Result) {
			if got.Kind != schemaflux.ErrInternal {
				t.Errorf("kind = %v", got.Kind)
			}
		}},
		{"an error that carried no kind is still a failure", func(r *wrapped.OperationResult) { r.ErrorKind = "unknown" }, func(t *testing.T, got Result) {
			if got.Kind == 0 {
				t.Error("a failure whose kind nobody knew was reported as a success")
			}
		}},
		{"a kind added later is still a failure", func(r *wrapped.OperationResult) { r.ErrorKind = "something new" }, func(t *testing.T, got Result) {
			if got.Kind == 0 {
				t.Error("a failure with a name this package does not know was reported as a success")
			}
		}},
		{"a hostile model", func(r *wrapped.OperationResult) { r.Model = hostile }, func(t *testing.T, got Result) { limited(t, "Model", got.Model, 128) }},
		{"a hostile provider", func(r *wrapped.OperationResult) { r.Provider = hostile }, func(t *testing.T, got Result) { limited(t, "Provider", got.Provider, 64) }},
		{"a hostile finish reason", func(r *wrapped.OperationResult) { r.FinishReason = hostile }, func(t *testing.T, got Result) { limited(t, "FinishReason", got.FinishReason, 64) }},
		{"a hostile operation", func(r *wrapped.OperationResult) { r.Operation = hostile }, func(t *testing.T, got Result) { limited(t, "Operation", got.Operation, 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := answered
			tc.mutate(&result)
			tc.check(t, resultFrom(result))
		})
	}
}

func TestANoteCarriesTheAttemptTheKindOfTheFailureAndTheWait(t *testing.T) {
	cases := []struct {
		name string
		in   wrapped.Note
		want Note
	}{
		{"a retry after a rate limit", wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1, ErrorKind: "rate limited", Wait: 20 * time.Millisecond},
			Note{Name: NoteRetry, Attempt: 1, Kind: schemaflux.ErrRateLimited, Wait: 20 * time.Millisecond}},
		{"a retry after an unavailable provider", wrapped.Note{Name: wrapped.NoteRetry, Attempt: 2, ErrorKind: "provider unavailable"},
			Note{Name: NoteRetry, Attempt: 2, Kind: schemaflux.ErrUnavailable}},
		{"a retry after a timeout", wrapped.Note{Name: wrapped.NoteRetry, Attempt: 3, ErrorKind: "timeout", Wait: time.Second},
			Note{Name: NoteRetry, Attempt: 3, Kind: schemaflux.ErrTimeout, Wait: time.Second}},
		{"a hedge", wrapped.Note{Name: wrapped.NoteHedge, Attempt: 2}, Note{Name: NoteHedge, Attempt: 2}},
		{"the zero note", wrapped.Note{}, Note{}},
		{"a retry with no failure kind", wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1}, Note{Name: NoteRetry, Attempt: 1}},
		{"a retry whose failure carried no kind", wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1, ErrorKind: "unknown"},
			Note{Name: NoteRetry, Attempt: 1, Kind: schemaflux.ErrInternal}},
		{"a name that is not the library's", wrapped.Note{Name: "n\n" + strings.Repeat("x", 500)}, Note{Name: "n?" + strings.Repeat("x", 62)}},
		{"a negative attempt is passed on", wrapped.Note{Name: wrapped.NoteRetry, Attempt: -1}, Note{Name: NoteRetry, Attempt: -1}},
		{"a retry after a canceled call", wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1, ErrorKind: "canceled"}, Note{Name: NoteRetry, Attempt: 1, Kind: schemaflux.ErrCanceled}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := noteFrom(tc.in); got != tc.want {
				t.Errorf("noteFrom = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The conversion runs on every unit of every observed call. A string that is
// already printable and within its limit, which is every id the library makes
// and every model name in use, is passed through without a copy, so the
// bounding costs a scan and no allocation.
func TestConvertingAnEventAResultAndANoteAllocatesNothingForCleanStrings(t *testing.T) {
	event := wrapped.OperationEvent{
		Operation: "extract", Provider: "openai", Model: "gpt-5.6-luna", RequestID: "req_ba09dc8ced0b338b",
		CorrelationID: "corr_1", Mode: "strict", Intelligence: "smart", SchemaName: "invoice", ResponseFormat: "json",
	}
	result := wrapped.OperationResult{
		Operation: "extract", Attempts: 2, Provider: "openai", Model: "gpt-5.6-luna", FinishReason: "stop", ErrorKind: "rate limited",
		Usage: types.TokenUsage{PromptTokens: 100, CompletionTokens: 30, TotalTokens: 130},
		Cost:  types.CostInfo{TotalCost: 0.01, Currency: "USD", Priced: true},
	}
	note := wrapped.Note{Name: wrapped.NoteRetry, Attempt: 1, ErrorKind: "timeout", Wait: time.Second}

	var sink Event
	if got := testing.AllocsPerRun(200, func() { sink = eventFrom(event) }); got != 0 {
		t.Errorf("converting an event allocates %.0f times, want 0", got)
	}
	var out Result
	if got := testing.AllocsPerRun(200, func() { out = resultFrom(result) }); got != 0 {
		t.Errorf("converting a result allocates %.0f times, want 0", got)
	}
	var n Note
	if got := testing.AllocsPerRun(200, func() { n = noteFrom(note) }); got != 0 {
		t.Errorf("converting a note allocates %.0f times, want 0", got)
	}
	_, _, _ = sink, out, n
}

// What a result carries of a cache, a decision pair that escalated and a run's
// items reaches the observer as it was, and every string of it is bounded as the
// others are.
func TestResultFromCarriesTheCacheEscalationAndRunFields(t *testing.T) {
	long := strings.Repeat("x", 500)
	cases := []struct {
		name string
		in   wrapped.OperationResult
		want func(Result) string // "" when right, otherwise what is wrong
	}{
		{"a cache hit", wrapped.OperationResult{Cached: true}, func(r Result) string {
			if !r.Cached {
				return "Cached is false"
			}
			return ""
		}},
		{"no cache hit", wrapped.OperationResult{}, func(r Result) string {
			if r.Cached {
				return "Cached is true"
			}
			return ""
		}},
		{"an escalation", wrapped.OperationResult{AskedFirst: "jev", AskedFirstModel: "jev-1", Escalated: "below the floor"}, func(r Result) string {
			if r.AskedFirst != "jev" || r.AskedFirstModel != "jev-1" || r.Escalated != "below the floor" {
				return "the escalation was changed"
			}
			return ""
		}},
		{"no escalation", wrapped.OperationResult{}, func(r Result) string {
			if r.AskedFirst != "" || r.AskedFirstModel != "" || r.Escalated != "" {
				return "an escalation was made up"
			}
			return ""
		}},
		{"a provider asked first is bounded to 64", wrapped.OperationResult{AskedFirst: long}, func(r Result) string {
			if len(r.AskedFirst) != 64 {
				return "AskedFirst is " + strconv.Itoa(len(r.AskedFirst)) + " bytes"
			}
			return ""
		}},
		{"a model asked first is bounded to 128", wrapped.OperationResult{AskedFirstModel: long}, func(r Result) string {
			if len(r.AskedFirstModel) != 128 {
				return "AskedFirstModel is " + strconv.Itoa(len(r.AskedFirstModel)) + " bytes"
			}
			return ""
		}},
		{"a reason is bounded to 64", wrapped.OperationResult{Escalated: long}, func(r Result) string {
			if len(r.Escalated) != 64 {
				return "Escalated is " + strconv.Itoa(len(r.Escalated)) + " bytes"
			}
			return ""
		}},
		{"unprintable bytes in a reason", wrapped.OperationResult{Escalated: "below\nthe floor"}, func(r Result) string {
			if r.Escalated != "below?the floor" {
				return "Escalated is " + strconv.Quote(r.Escalated)
			}
			return ""
		}},
		{"a run's items", wrapped.OperationResult{Requests: 9, Items: 6, Succeeded: 3, Failed: 2, Cancelled: 1}, func(r Result) string {
			if r.Requests != 9 || r.Items != 6 || r.Succeeded != 3 || r.Failed != 2 || r.Cancelled != 1 {
				return "the counts were changed"
			}
			return ""
		}},
		{"no run", wrapped.OperationResult{ErrorKind: "timeout"}, func(r Result) string {
			if r.Requests != 0 || r.Items != 0 || r.Succeeded != 0 || r.Failed != 0 || r.Cancelled != 0 {
				return "counts were made up"
			}
			return ""
		}},
		{"the kind of a run in which every item failed", wrapped.OperationResult{Items: 2, Failed: 2, ErrorKind: "refused"}, func(r Result) string {
			if r.Kind != schemaflux.ErrRefused || r.Failed != 2 {
				return "the kind or the count was lost"
			}
			return ""
		}},
		{"an internal failure is internal", wrapped.OperationResult{ErrorKind: wrapped.ErrorKindInternal}, func(r Result) string {
			if r.Kind != schemaflux.ErrInternal {
				return "Kind is " + r.Kind.Error()
			}
			return ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if problem := tc.want(resultFrom(tc.in)); problem != "" {
				t.Error(problem)
			}
		})
	}
}
