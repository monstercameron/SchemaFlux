package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// A-004: every setting declares exactly one scope, precedence is
// deterministic within it, the resolved value and its source are printable,
// and an invocation cannot weaken a locked client/data-policy constraint.
//
// These tests exercise the mechanism from internal/ops/options.go directly,
// the same package the merge and validation live in, rather than through the
// fluent layer -- A-013's tests cover the fluent Run(ctx)/RunResult(ctx)
// surface separately.

// 1. A setting nobody touched reports the operation's own default.
func TestExplainResolution_UntouchedSettingReportsOperationDescriptorScope(t *testing.T) {
	def := NewExtractOptions()
	plan := ExplainResolution(def, def)

	setting, ok := plan.Get("Steering")
	if !ok {
		t.Fatal("Steering missing from the plan")
	}
	if setting.Source != types.ScopeOperationDescriptor {
		t.Errorf("Source = %v, want ScopeOperationDescriptor", setting.Source)
	}
	if setting.Locked {
		t.Error("an untouched setting must not report Locked")
	}
}

// 2. A setting an invocation changed reports invocation scope.
func TestExplainResolution_InvocationChangeReportsInvocationScope(t *testing.T) {
	def := NewExtractOptions()
	invoked := def.WithSteering("prefer explicit evidence")

	plan := ExplainResolution(invoked, def)
	setting, ok := plan.Get("Steering")
	if !ok {
		t.Fatal("Steering missing from the plan")
	}
	if setting.Source != types.ScopeInvocation {
		t.Errorf("Source = %v, want ScopeInvocation", setting.Source)
	}
	if setting.Value != "prefer explicit evidence" {
		t.Errorf("Value = %q", setting.Value)
	}
}

// 3. A context deadline is reported at request-context scope and is present
// in the plan whenever the context carries one -- deterministic within that
// scope: a deadline always shows up, regardless of what else is set.
func TestExplainResolution_ContextDeadlineReportsRequestContextScope(t *testing.T) {
	def := NewExtractOptions()
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	withCtx := def.WithContext(ctx)

	plan := ExplainResolution(withCtx, def)
	setting, ok := plan.Get("Deadline")
	if !ok {
		t.Fatal("Deadline missing from the plan despite a context deadline")
	}
	if setting.Source != types.ScopeRequestContext {
		t.Errorf("Source = %v, want ScopeRequestContext", setting.Source)
	}
}

// 4. A setting equal to a locked value reports client scope and Locked=true,
// distinguishing "the invocation happened to ask for this" from "the lock is
// what is actually binding."
func TestExplainResolution_LockedValueReportsClientScope(t *testing.T) {
	def := NewExtractOptions()
	ctx := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 500})
	invoked := def
	invoked.CommonOptions = invoked.CommonOptions.WithContext(ctx)
	invoked.OpOptions.MaxOutputTokens = 500

	plan := ExplainResolution(invoked, def)
	setting, ok := plan.Get("MaxOutputTokens")
	if !ok {
		t.Fatal("MaxOutputTokens missing from the plan")
	}
	if setting.Source != types.ScopeClient || !setting.Locked {
		t.Errorf("MaxOutputTokens = %+v, want ScopeClient and Locked", setting)
	}
}

// 5. option.Model-equivalent isolation: a per-call value (here,
// MaxOutputTokens, which -- unlike a hypothetical Model field -- is a real
// field CallLLM already reads, see types.OpOptions.MaxOutputTokens) set on
// one ExtractOptions value must not leak into a sibling built from the same
// starting point.
func TestPerCallSetting_DoesNotLeakBetweenSiblings(t *testing.T) {
	base := NewExtractOptions()

	first := base
	first.OpOptions.MaxOutputTokens = 111

	second := base
	second.OpOptions.MaxOutputTokens = 222

	if first.OpOptions.MaxOutputTokens != 111 {
		t.Errorf("first.MaxOutputTokens = %d, want 111", first.OpOptions.MaxOutputTokens)
	}
	if second.OpOptions.MaxOutputTokens != 222 {
		t.Errorf("second.MaxOutputTokens = %d, want 222", second.OpOptions.MaxOutputTokens)
	}
	if base.OpOptions.MaxOutputTokens != 0 {
		t.Errorf("base.MaxOutputTokens = %d, want 0 (unmodified)", base.OpOptions.MaxOutputTokens)
	}
}

// 6. An invocation raising MaxOutputTokens above a locked ceiling is
// rejected, not applied -- Validate() returns an error and the caller never
// reaches toOpOptions() or a provider call.
func TestValidate_RejectsInvocationThatWeakensLockedMaxOutputTokens(t *testing.T) {
	ctx := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 1000})
	opts := NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	opts.OpOptions.MaxOutputTokens = 8000 // above the lock

	err := opts.Validate()
	if err == nil {
		t.Fatal("expected Validate to reject a MaxOutputTokens above the locked ceiling")
	}
	if !strings.Contains(err.Error(), "locked policy") {
		t.Errorf("error = %q, want it to name the locked policy", err.Error())
	}
}

// 7. The same lock accepts an invocation that is stricter (lower) than the
// ceiling -- "an invocation may make a limit stricter."
func TestValidate_AcceptsInvocationThatTightensLockedMaxOutputTokens(t *testing.T) {
	ctx := WithLockedLimits(context.Background(), LockedLimits{MaxOutputTokens: 1000})
	opts := NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)
	opts.OpOptions.MaxOutputTokens = 200 // below the lock: stricter, allowed

	if err := opts.Validate(); err != nil {
		t.Errorf("Validate rejected a stricter MaxOutputTokens: %v", err)
	}
}

// 8. A locked Mode floor rejects an invocation that asks for a weaker mode --
// Creative is weaker than the Strict floor.
func TestValidate_RejectsInvocationThatWeakensLockedMode(t *testing.T) {
	ctx := WithLockedLimits(context.Background(), LockedLimits{MinMode: types.Strict})
	opts := NewChooseOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx).WithMode(types.Creative)

	err := opts.Validate()
	if err == nil {
		t.Fatal("expected Validate to reject a Mode weaker than the locked floor")
	}
	if !strings.Contains(err.Error(), "locked policy") {
		t.Errorf("error = %q, want it to name the locked policy", err.Error())
	}

	// The original lock is unaffected by the rejected attempt: a second,
	// compliant invocation under the same lock still succeeds.
	compliant := NewChooseOptions()
	compliant.CommonOptions = compliant.CommonOptions.WithContext(ctx).WithMode(types.Strict)
	if err := compliant.Validate(); err != nil {
		t.Errorf("a compliant invocation under the same lock failed: %v", err)
	}
}

// 9. With no lock installed on the context, the same options that would have
// been rejected above pass -- the check is opt-in per context, not a global
// ceiling.
func TestValidate_NoLockMeansNoRejection(t *testing.T) {
	opts := NewChooseOptions()
	opts.CommonOptions = opts.CommonOptions.WithMode(types.Creative)
	if err := opts.Validate(); err != nil {
		t.Errorf("Validate rejected Creative with no lock installed: %v", err)
	}
}

// 10. Setting a Timeout after the options were otherwise built takes effect:
// the resulting context carries a deadline that was not there before.
func TestWithTimeout_TakesEffectOnTheResolvedContext(t *testing.T) {
	opts := NewFilterOptions().WithCriteria("keep the relevant ones")
	opts.CommonOptions = opts.CommonOptions.WithTimeout(50 * time.Millisecond)

	resolved := opts.toOpOptions()
	deadline, ok := resolved.Context.Deadline()
	if !ok {
		t.Fatal("resolved context has no deadline; WithTimeout did not take effect")
	}
	if time.Until(deadline) > 50*time.Millisecond {
		t.Errorf("deadline %s away, want <= 50ms", time.Until(deadline))
	}
}

// 11. A deadline the caller's context already carries always wins: a longer
// Timeout set afterwards cannot push it further out.
func TestWithTimeout_NeverExtendsAnExistingEarlierDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	opts := NewFilterOptions().WithCriteria("keep the relevant ones")
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx).WithTimeout(time.Hour)

	resolved := opts.toOpOptions()
	deadline, ok := resolved.Context.Deadline()
	if !ok {
		t.Fatal("resolved context lost its deadline")
	}
	if time.Until(deadline) > 20*time.Millisecond {
		t.Errorf("deadline %s away, want <= 20ms (the caller's own, earlier deadline)", time.Until(deadline))
	}
}

// 12. A model pin (represented here, as in test 5, by MaxOutputTokens -- the
// real field CallLLM reads) set through Configure on one fluent-style call
// does not survive into a second options value built from the same operation
// default, proving isolation at the options level the fluent builders sit on
// top of.
func TestPerCallSetting_IsolatedAcrossTwoCallsFromTheSameDefault(t *testing.T) {
	makeDefault := NewExtractOptions

	callOne := makeDefault()
	callOne.OpOptions.MaxOutputTokens = 4096

	callTwo := makeDefault()
	if callTwo.OpOptions.MaxOutputTokens != 0 {
		t.Errorf("callTwo.MaxOutputTokens = %d, want 0 -- callOne's setting leaked", callTwo.OpOptions.MaxOutputTokens)
	}
}
