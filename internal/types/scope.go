package types

import "fmt"

// A-004's Revised line rejects "per-call precedence" as too blunt: every
// setting has to declare exactly one scope, and precedence has to be
// deterministic within that scope rather than "whoever set it last wins."
//
// OptionScope is that declaration. It is deliberately small and ordered by
// where in the call stack a value can originate -- not by how strong the
// value is, which is a separate axis (see ResolvedSetting.Locked).

// OptionScope identifies which layer of the call stack produced a setting's
// effective value.
type OptionScope uint8

const (
	// ScopeUnspecified means nothing in the resolution touched this setting,
	// which is different from every scope below actually applying: a
	// resolver that returns this for a material setting has a bug, not an
	// unset value -- an unset value still has a source, the operation's own
	// default.
	ScopeUnspecified OptionScope = iota

	// ScopeProcess: a value fixed for the whole binary -- an environment
	// variable, a compiled-in default outside any client or call.
	ScopeProcess

	// ScopeClient: a value set once on a *Client and shared by every
	// operation that client runs, until an invocation overrides it within
	// what its lock allows.
	ScopeClient

	// ScopeOperationDescriptor: the operation's own default, applied when
	// nothing narrower said anything -- DefaultPolicy's role for Mode and
	// Speed, and NewXOptions' role for everything else.
	ScopeOperationDescriptor

	// ScopeInvocation: a value set on this specific call through a fluent
	// builder's With* method or Configure.
	ScopeInvocation

	// ScopeRequestContext: a value carried by the context.Context passed to
	// Run -- today, only a deadline. It sits above invocation because a
	// deadline always wins regardless of what an invocation asked for.
	ScopeRequestContext

	// ScopeProviderRequest: the value as it left the library, in the request
	// actually sent. It exists as a distinct scope because a provider's own
	// fallback (rounding a temperature, substituting a deprecated model) can
	// make this differ from ScopeRequestContext, and collapsing the two would
	// hide that.
	ScopeProviderRequest
)

// String renders the scope for a resolution plan and for log lines.
func (s OptionScope) String() string {
	switch s {
	case ScopeProcess:
		return "process"
	case ScopeClient:
		return "client"
	case ScopeOperationDescriptor:
		return "operation default"
	case ScopeInvocation:
		return "invocation"
	case ScopeRequestContext:
		return "request context"
	case ScopeProviderRequest:
		return "provider request"
	default:
		return "unspecified"
	}
}

// ResolvedSetting is one material setting's effective value, where it came
// from, and whether it is locked against being weakened by a narrower scope.
//
// Value is rendered as a string rather than kept as `any` on purpose: this is
// what a caller debugging a plan prints, and the input rule against logging a
// caller's payload (AGENTS.md, "Never log or embed the caller's payload")
// applies here too -- these are configuration values, never the data the
// operation is running on, and rendering them as text keeps that boundary
// visible at the type rather than relying on every caller to remember it.
type ResolvedSetting struct {
	Name   string
	Value  string
	Source OptionScope

	// Locked reports whether this setting carries a constraint a narrower
	// scope may tighten but never weaken. A caller reading a plan where
	// Locked is true and Source is ScopeInvocation knows the invocation's
	// value passed the lock rather than ignored it.
	Locked bool
}

func (r ResolvedSetting) String() string {
	if r.Locked {
		return fmt.Sprintf("%s=%s (%s, locked)", r.Name, r.Value, r.Source)
	}
	return fmt.Sprintf("%s=%s (%s)", r.Name, r.Value, r.Source)
}

// ResolutionPlan is the full set of resolved settings for one call. It is
// what "print effective value and source for every material setting"
// (A-004's added verify line) answers with: a caller debugging why a model
// pin was ignored reads this instead of stepping through five layers of
// option merging.
type ResolutionPlan struct {
	Settings []ResolvedSetting
}

// String renders the plan one setting per line, in the order Settings holds
// them. Callers building a plan are expected to append in a stable order;
// ResolutionPlan does not sort, so a plan built from a map without sorting
// keys first would be non-deterministic -- the same defect sortedKeys exists
// to prevent elsewhere in this codebase.
func (p ResolutionPlan) String() string {
	out := ""
	for i, setting := range p.Settings {
		if i > 0 {
			out += "\n"
		}
		out += setting.String()
	}
	return out
}

// Get returns the resolved setting by name, and whether it was found. A
// caller asking "why was my timeout ignored" wants this rather than scanning
// String's output.
func (p ResolutionPlan) Get(name string) (ResolvedSetting, bool) {
	for _, setting := range p.Settings {
		if setting.Name == name {
			return setting, true
		}
	}
	return ResolvedSetting{}, false
}
