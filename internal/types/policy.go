package types

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CP-002. DataPolicy is the tenant/client-boundary contract that decides
// where a request is allowed to run and what may happen to it afterward --
// classification, allowed providers and regions, retention, training use,
// content logging, result caching, and the minimum contract level an answer
// must carry. TRU-11: a policy locked at the client or tenant boundary may be
// STRENGTHENED by an invocation and never WEAKENED by one, because the
// opposite -- a single call quietly loosening a tenant-wide privacy
// guarantee -- is how a private route's data ends up on a public one. Tighten
// below is the one function that combines two policies, and it is built so
// that property holds regardless of which side (tenant default or per-call
// override) is more restrictive on any given field.

// ContentLoggingLevel orders how much of a call's content a log line may
// carry, least to most permissive. The zero value, LogNoContent, is the
// production default SEC-002 names -- a DataPolicy nobody configured logs
// nothing, not "whatever the logger happened to default to."
type ContentLoggingLevel uint8

const (
	// LogNoContent: no prompt or completion text reaches a log, ever.
	LogNoContent ContentLoggingLevel = iota

	// LogMetadataOnly: shapes, counts, field names, error kinds -- the
	// things types.DescribeValue already produces -- but never a value.
	LogMetadataOnly

	// LogRedactedContent: content may be logged after passing through a
	// redaction pass (mw.RedactEgress or an equivalent). Weaker than
	// LogMetadataOnly in the sense that some caller data reaches the log,
	// stronger than LogFullContent in that credential- and PII-shaped
	// substrings do not.
	LogRedactedContent

	// LogFullContent: content may be logged verbatim. Never the default;
	// an invocation can only reach this level if the tenant-level policy
	// already permits it, because Tighten refuses to raise the level below.
	LogFullContent
)

func (l ContentLoggingLevel) String() string {
	switch l {
	case LogNoContent:
		return "no_content"
	case LogMetadataOnly:
		return "metadata_only"
	case LogRedactedContent:
		return "redacted_content"
	case LogFullContent:
		return "full_content"
	default:
		return "unknown"
	}
}

// RetentionDays is how long, in days, a route or a caller may retain
// request/response content. The sentinel RetentionUnspecified distinguishes
// "no constraint declared" from RetentionDays(0), which is a real and
// stricter constraint: zero-retention, permitted for zero days.
type RetentionDays int

// RetentionUnspecified means the policy declares no retention constraint on
// this axis. It is not the zero value (0) on purpose: 0 is "no retention
// permitted," a real and strict constraint, and conflating "nobody said" with
// "nobody may" would make an unconfigured DataPolicy silently reject every
// route -- which is safe but wrong, and indistinguishable from a deliberate
// zero-retention tenant in any error message this package produces. -1 keeps
// the two apart.
const RetentionUnspecified RetentionDays = -1

// DataPolicy is the tenant/client-boundary contract. The empty DataPolicy{}
// is NOT "no restrictions" -- see IsRestricted and every Allows* method's
// doc comment. It is the policy a caller gets by never setting anything,
// and every field's zero value was chosen to be the SAFE (most restrictive
// or most honestly "unspecified") one, not the permissive one, in keeping
// with AGENTS.md's "never fail open."
type DataPolicy struct {
	// Classification is a caller-defined label ("public", "restricted",
	// "pii", ...). Empty means undeclared. Two policies with different
	// non-empty classifications conflict (see Tighten); this package does
	// not interpret the label's meaning beyond equality, the same
	// convention mw.FallbackDataPolicy already uses for the same reason
	// (nothing here can know what a caller's labels mean).
	Classification string

	// AllowedProviders, when non-empty, is the exhaustive list of provider
	// names a route may use. Empty means undeclared -- NOT unrestricted;
	// see AllowsProvider.
	AllowedProviders []string

	// AllowedRegions, when non-empty, is the exhaustive list of regions a
	// route may execute in. Empty means undeclared, same convention as
	// AllowedProviders.
	AllowedRegions []string

	// MaxRetentionDays bounds how long a route may retain content.
	// RetentionUnspecified (the zero value's replacement -- callers should
	// set this explicitly) means no constraint declared.
	MaxRetentionDays RetentionDays

	// AllowTrainingUse permits a route to use the request/response to train
	// or fine-tune a model. False (the zero value) is the safe default: a
	// DataPolicy nobody configured does not permit training use.
	AllowTrainingUse bool

	// ContentLogging is the maximum ContentLoggingLevel permitted anywhere
	// downstream of this policy. Zero value is LogNoContent, the safe
	// default.
	ContentLogging ContentLoggingLevel

	// AllowResultCaching permits mw.Cache (or an equivalent) to store this
	// call's response for reuse. False (the zero value) is the safe
	// default -- P-008's `store: false` default is CP-002's own note that
	// this is the first instance of the same rule.
	AllowResultCaching bool

	// MinimumContract is the lowest ContractLevel an answer executed under
	// this policy may be delivered at. ContractPromptOnly (the zero value)
	// imposes no floor.
	MinimumContract ContractLevel
}

// ErrDataPolicyConflict reports two DataPolicy values whose Tighten cannot
// be resolved by "pick the more restrictive side" because they assert
// different, mutually exclusive facts on the same axis -- two non-empty,
// unequal Classification labels, most notably. Silently picking one would
// be a guess dressed up as an intersection; this package refuses instead.
var ErrDataPolicyConflict = errors.New("data policy conflict")

// Tighten combines p (the boundary policy, usually the tenant/client
// default) with override (a per-invocation policy) into the policy that
// governs one call. Every field independently takes whichever side is more
// restrictive; a field undeclared on one side defers entirely to the other
// side's value, declared or not. The result is never less restrictive than
// either input on any field -- that is the whole TRU-11 property this
// function exists to guarantee, and every test in policy_test.go that
// matters checks it from the "did this get more permissive" direction, not
// just "did this match an example."
//
// Classification is the one field that can produce ErrDataPolicyConflict:
// two different non-empty labels do not have an ordering to pick the
// stricter of, so this refuses rather than choosing one arbitrarily.
func (p DataPolicy) Tighten(override DataPolicy) (DataPolicy, error) {
	out := DataPolicy{}

	switch {
	case p.Classification == "":
		out.Classification = override.Classification
	case override.Classification == "":
		out.Classification = p.Classification
	case p.Classification == override.Classification:
		out.Classification = p.Classification
	default:
		return DataPolicy{}, fmt.Errorf("%w: classification %q vs %q", ErrDataPolicyConflict, p.Classification, override.Classification)
	}

	out.AllowedProviders = intersectOrDefer(p.AllowedProviders, override.AllowedProviders)
	out.AllowedRegions = intersectOrDefer(p.AllowedRegions, override.AllowedRegions)

	out.MaxRetentionDays = stricterRetention(p.MaxRetentionDays, override.MaxRetentionDays)

	// AllowTrainingUse and AllowResultCaching: permissive only when BOTH
	// sides permit. An override that turns either back on cannot undo a
	// boundary policy that turned it off -- that is the direction TRU-11
	// forbids.
	out.AllowTrainingUse = p.AllowTrainingUse && override.AllowTrainingUse
	out.AllowResultCaching = p.AllowResultCaching && override.AllowResultCaching

	out.ContentLogging = stricterLogging(p.ContentLogging, override.ContentLogging)

	if override.MinimumContract > p.MinimumContract {
		out.MinimumContract = override.MinimumContract
	} else {
		out.MinimumContract = p.MinimumContract
	}

	return out, nil
}

// intersectOrDefer implements the "undeclared defers to the other side,
// both declared means intersect" rule AllowedProviders and AllowedRegions
// share. An intersection that ends up empty (both declared, nothing in
// common) is returned as a non-nil empty slice, not nil -- nil means
// "undeclared" everywhere else in this file, and a real, all-excluding
// intersection must not be misread as "no constraint" by a caller checking
// len(out) == 0 against the wrong sentinel.
func intersectOrDefer(a, b []string) []string {
	if len(a) == 0 {
		return append([]string(nil), b...)
	}
	if len(b) == 0 {
		return append([]string(nil), a...)
	}
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		if set[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func stricterRetention(a, b RetentionDays) RetentionDays {
	if a == RetentionUnspecified {
		return b
	}
	if b == RetentionUnspecified {
		return a
	}
	if a < b {
		return a
	}
	return b
}

func stricterLogging(a, b ContentLoggingLevel) ContentLoggingLevel {
	if a < b {
		return a
	}
	return b
}

// AllowsProvider reports whether name is permitted under p. An UNDECLARED
// AllowedProviders list (nil, or explicitly set empty) makes every provider
// name ineligible -- "no constraint declared" is not "no constraint", it is
// "nothing was ever cleared for this policy," per the never-fail-open rule
// this whole file follows. A policy that genuinely has no provider
// restriction must say so by naming every provider it allows.
func (p DataPolicy) AllowsProvider(name string) bool {
	return containsFold(p.AllowedProviders, name)
}

// AllowsRegion mirrors AllowsProvider for AllowedRegions.
func (p DataPolicy) AllowsRegion(region string) bool {
	return containsFold(p.AllowedRegions, region)
}

// AllowsRetention reports whether days of retention is permitted.
// RetentionUnspecified on the policy means no route may retain anything --
// same never-fail-open rule: an unconfigured ceiling is a ceiling of zero,
// not infinity.
func (p DataPolicy) AllowsRetention(days RetentionDays) bool {
	if p.MaxRetentionDays == RetentionUnspecified {
		return days <= 0
	}
	return days <= p.MaxRetentionDays
}

// AllowsContentLogging reports whether level is permitted under p.
func (p DataPolicy) AllowsContentLogging(level ContentLoggingLevel) bool {
	return level <= p.ContentLogging
}

func containsFold(list []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, v := range list {
		if strings.ToLower(strings.TrimSpace(v)) == want {
			return true
		}
	}
	return false
}
