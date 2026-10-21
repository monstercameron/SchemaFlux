package schemaflux

import (
	"slices"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// DataPolicy restricts where a client's calls may be sent. It does not fail
// open: a list left empty allows nothing, so the zero DataPolicy refuses every
// call, and a client with no restriction has a nil Policy. A call that the policy
// does not allow fails with ErrPolicy before anything is sent. Where a route runs
// and what it retains are the caller's own statements, made with
// provider.DeclareRoute; a route that has declared nothing is not allowed.
//
// WithClient and SetDefault copy the policy; changing it afterwards has no
// effect.
type DataPolicy struct {
	// Providers are the providers calls may go to. Empty allows none.
	Providers []string

	// Regions are the regions a route may run in. Every region a route may run in
	// has to be listed. Empty allows none.
	Regions []string

	// MaxRetentionDays is the longest a route may keep what it is sent. Zero
	// allows only a route that keeps nothing. A negative value is a configuration
	// error.
	MaxRetentionDays int

	// AllowCaching lets a response cache (mw.Cache) store and serve this client's
	// answers. False passes them through.
	AllowCaching bool

	// MinContract is the weakest guarantee an answer may be delivered with. The
	// zero value sets no floor. A policy that sets only a floor still allows no
	// provider: list the providers and regions and declare each route
	// (provider.DeclareRoute), as in
	//
	//	&DataPolicy{Providers: []string{"openai"}, Regions: []string{"us"}, MaxRetentionDays: 30,
	//		MinContract: ContractSchemaConstrained}
	//
	// A floor above ContractPromptOnly is held on every call of every operation
	// of the client. Summarize, Rewrite, Translate, Expand, Generate into a
	// string, Complete, CompleteField, Diff, Format, Match, Session.Send,
	// Session.SendResults, StreamSummarize, StreamRewrite, StreamTranslate and
	// StreamExpand deliver at that level and are refused with ErrPolicy before
	// anything is sent. A value that is not one of the contract levels is a
	// configuration error.
	MinContract ContractLevel
}

// clone returns a copy of p whose lists are copies, or nil for nil. A copy of
// the empty lists stays what they were, nil or empty, which a policy reads
// alike.
func (p *DataPolicy) clone() *DataPolicy {
	if p == nil {
		return nil
	}
	copied := *p
	copied.Providers = slices.Clone(p.Providers)
	copied.Regions = slices.Clone(p.Regions)
	return &copied
}

// check returns the configuration error of a policy no call can be held to.
func (p *DataPolicy) check() error {
	switch {
	case p == nil:
		return nil
	case p.MaxRetentionDays < 0:
		return types.Newf(types.KindConfiguration, "client", "DataPolicy.MaxRetentionDays must not be negative")
	case p.MinContract > ContractFullyGoverned:
		return types.Newf(types.KindConfiguration, "client", "DataPolicy.MinContract is not one of the contract levels")
	}
	return nil
}

// internal is the policy as the call path holds it, with lists of its own.
func (p *DataPolicy) internal() types.DataPolicy {
	return types.DataPolicy{
		AllowedProviders:   slices.Clone(p.Providers),
		AllowedRegions:     slices.Clone(p.Regions),
		MaxRetentionDays:   types.RetentionDays(p.MaxRetentionDays),
		AllowResultCaching: p.AllowCaching,
		MinimumContract:    contractToInternal(p.MinContract),
	}
}

// contractToInternal is the internal level for a level.
func contractToInternal(c ContractLevel) types.ContractLevel {
	switch c {
	case ContractPromptOnly:
		return types.ContractPromptOnly
	case ContractJSONWellFormed:
		return types.ContractJSONWellFormed
	case ContractSchemaConstrained:
		return types.ContractSchemaConstrained
	case ContractSchemaAndInvariantChecked:
		return types.ContractSchemaAndInvariantChecked
	case ContractEvidenceChecked:
		return types.ContractEvidenceChecked
	case ContractFullyGoverned:
		return types.ContractFullyGoverned
	}
	return types.ContractLevel(c)
}
