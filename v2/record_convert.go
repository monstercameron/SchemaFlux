package schemaflux

import (
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// contractFromInternal is the level for an internal level.
func contractFromInternal(c types.ContractLevel) ContractLevel {
	switch c {
	case types.ContractPromptOnly:
		return ContractPromptOnly
	case types.ContractJSONWellFormed:
		return ContractJSONWellFormed
	case types.ContractSchemaConstrained:
		return ContractSchemaConstrained
	case types.ContractSchemaAndInvariantChecked:
		return ContractSchemaAndInvariantChecked
	case types.ContractEvidenceChecked:
		return ContractEvidenceChecked
	case types.ContractFullyGoverned:
		return ContractFullyGoverned
	}
	return ContractLevel(c)
}

// timingToInternal is the internal timing for a Timing.
func timingToInternal(t Timing) types.Timing {
	return types.Timing{
		Calls:     t.Calls,
		Reused:    t.Reused,
		Queued:    t.Queued,
		Lookup:    t.Lookup,
		Connect:   t.Connect,
		Handshake: t.Handshake,
		Send:      t.Send,
		Wait:      t.Wait,
		Read:      t.Read,
		Network:   t.Network,
	}
}

// timingFromInternal is the Timing for an internal timing.
func timingFromInternal(t types.Timing) Timing {
	return Timing{
		Calls:     t.Calls,
		Reused:    t.Reused,
		Queued:    t.Queued,
		Lookup:    t.Lookup,
		Connect:   t.Connect,
		Handshake: t.Handshake,
		Send:      t.Send,
		Wait:      t.Wait,
		Read:      t.Read,
		Network:   t.Network,
	}
}

// costFromInternal is the Cost for an internal cost. The per-token prices, the
// billing period, the organisation and the project are not carried: a price per
// token is the price table's to state, and the other three are not a call's.
func costFromInternal(c types.CostInfo) Cost {
	// The currency is the one the call path states. A price that was found and
	// stated none is in the one currency the library prices in, "USD", so a priced
	// figure always says what it is in.
	currency := c.Currency
	if c.Priced && currency == "" {
		currency = "USD"
	}
	return Cost{
		Total:     c.TotalCost,
		Input:     c.PromptCost,
		Output:    c.CompletionCost,
		Cached:    c.CachedCost,
		Reasoning: c.ReasoningCost,
		Currency:  currency,
		Priced:    c.Priced,
		Estimated: c.Quality == types.PricingEstimated,
		Source:    c.PricingSource,
	}
}

// costToInternal is the internal cost for a Cost, the inverse of
// costFromInternal for the fields a Cost has: a Cost that is not Priced is no
// price found, and one that is Estimated says so.
func costToInternal(c Cost) types.CostInfo {
	info := types.CostInfo{
		TotalCost:      c.Total,
		PromptCost:     c.Input,
		CompletionCost: c.Output,
		CachedCost:     c.Cached,
		ReasoningCost:  c.Reasoning,
		Currency:       c.Currency,
		Priced:         c.Priced,
		PricingSource:  c.Source,
	}
	switch {
	case !c.Priced:
		info.Quality = types.PricingUnknown
	case c.Estimated:
		info.Quality = types.PricingEstimated
	default:
		info.Quality = types.PricingExact
	}
	return info
}

// checksFromInternal is the Checks for internal checks, nil for none.
func checksFromInternal(checks []types.Check) []Check {
	if len(checks) == 0 {
		return nil
	}
	converted := make([]Check, len(checks))
	for i, c := range checks {
		converted[i] = Check{Name: c.Name, Passed: c.Passed, Detail: c.Detail}
	}
	return converted
}

// provenanceFromInternal is the Provenance for an internal one, nil when the
// internal one is empty: the operation built none.
func provenanceFromInternal(p types.Provenance) *Provenance {
	if p.ResultID == "" && len(p.ParentResultIDs) == 0 && p.InputDigest == "" && p.SchemaDigest == "" &&
		p.OperationVersion == "" && p.PromptVersion == "" && p.ResolvedModel == "" &&
		len(p.NormalizersApplied) == 0 && p.ItemRecoveryPath == "" && !p.CacheUsed &&
		p.LibraryVersion == "" && p.AdapterVersion == "" {
		return nil
	}
	return &Provenance{
		ResultID:           p.ResultID,
		ParentResultIDs:    cloneStrings(p.ParentResultIDs),
		InputDigest:        p.InputDigest,
		SchemaDigest:       p.SchemaDigest,
		OperationVersion:   p.OperationVersion,
		PromptVersion:      p.PromptVersion,
		ResolvedModel:      p.ResolvedModel,
		NormalizersApplied: cloneStrings(p.NormalizersApplied),
		ItemRecoveryPath:   p.ItemRecoveryPath,
		CacheUsed:          p.CacheUsed,
		LibraryVersion:     p.LibraryVersion,
		AdapterVersion:     p.AdapterVersion,
	}
}

// askedFromInternal is the Asked for the questions and answers of a decision,
// nil for none. The options are copied.
func askedFromInternal(asked []ops.AnsweredQuestion) []Asked {
	if len(asked) == 0 {
		return nil
	}
	converted := make([]Asked, len(asked))
	for i, q := range asked {
		converted[i] = Asked{
			Question: q.Question,
			Options:  cloneStrings(q.Options),
			Low:      q.Low,
			High:     q.High,
			Answer:   q.Answer,
		}
	}
	return converted
}

// decisionFacts are who answered a decision and who was asked first, as the
// outcome of the call reports them.
type decisionFacts struct {
	answeredBy           string
	typed                bool
	askedFirst           string
	escalated            string
	askedFirstConfidence float64
	askedFirstReported   bool
}

// decisionFactsOf reads who answered a decision from its outcome. The outcome
// holds them only when the call made decisions and every one of them said the
// same (ops.Outcome.NoteDecision); a probability the decision provider asked
// first did not report is nil there and is not reported here, which is not a
// probability of zero.
func decisionFactsOf(o *ops.Outcome) decisionFacts {
	facts := decisionFacts{
		answeredBy: o.AnsweredBy,
		typed:      o.Typed,
		askedFirst: o.AskedFirst,
		escalated:  o.Escalated,
	}
	if o.AskedFirstModelConfidence != nil {
		facts.askedFirstConfidence = *o.AskedFirstModelConfidence
		facts.askedFirstReported = true
	}
	return facts
}

// apply writes the facts into a Record.
func (f decisionFacts) apply(rec *Record) {
	rec.AnsweredBy = f.answeredBy
	rec.Typed = f.typed
	rec.AskedFirst = f.askedFirst
	rec.Escalated = f.escalated
	rec.AskedFirstConfidence = f.askedFirstConfidence
	rec.AskedFirstReported = f.askedFirstReported
}

// recordFromOutcome makes the Record of one call from its outcome. It carries
// the questions and answers of a decision only when wantDecisions is set, and
// the refused body, cut to maxBody bytes, only when wantBodies is set: what a
// recorder did not ask for is not in the Record it is given.
//
// Requests is the attempts the call made less the answers a cache served, since
// the provider was not called for those. Every field of types.Meta is either
// read here or listed as not carried in the test of this function.
func recordFromOutcome(o *ops.Outcome, wantDecisions, wantBodies bool, maxBody int) Record {
	m := &o.Meta
	rec := Record{
		Op:             m.Operation,
		Start:          o.Start,
		Elapsed:        m.Elapsed,
		Provider:       m.Provider,
		Model:          m.Model,
		RequestedModel: m.RequestedModel,
		ServiceTier:    m.ServiceTier,
		RequestID:      m.RequestID,
		CorrelationID:  m.CorrelationID,
		SchemaID:       m.SchemaID,
		Requests:       max(0, m.Attempts-m.CacheHits),
		Retries:        m.Retries,
		Repairs:        m.Repairs,
		CacheHits:      m.CacheHits,
		Hedged:         m.Hedged,
		Tokens:         usageFromInternal(m.Usage),
		Cost:           costFromInternal(m.Cost),
		Timing:         timingFromInternal(m.Timing),
		Requested:      contractFromInternal(m.RequestedContract),
		Delivered:      contractFromInternal(m.DeliveredContract),
		Checks:         checksFromInternal(m.Checks),
		Strategy:       m.Strategy,
		Provenance:     provenanceFromInternal(m.Provenance),
	}
	if o.Err != nil {
		rec.Err = convertError(rec.Op, o.Err)
	}
	decisionFactsOf(o).apply(&rec)
	if wantDecisions {
		rec.Asked = askedFromInternal(o.Asked)
	}
	if wantBodies {
		rec.Body = bind.CutBody(o.Body, maxBody)
	}
	return rec
}

// sumRecords adds records up as Recorder.Total says.
func sumRecords(records []Record) Record {
	if len(records) == 0 {
		return Record{}
	}
	total := Record{
		Requested: records[0].Requested,
		Delivered: records[0].Delivered,
		Typed:     true,
	}
	// pricedAll and reusedAll are what every record that sent a request, and every
	// record that timed one, said; sent and timed are whether there was any.
	pricedAll, reusedAll, sent, timed := true, true, false, false
	var earliest, latest time.Time
	var unstamped time.Duration
	for _, rec := range records {
		if total.Err == nil {
			total.Err = rec.Err
		}
		if !rec.Start.IsZero() {
			if earliest.IsZero() || rec.Start.Before(earliest) {
				earliest = rec.Start
			}
			if finish := rec.Start.Add(rec.Elapsed); finish.After(latest) {
				latest = finish
			}
		} else {
			unstamped = max(unstamped, rec.Elapsed)
		}
		total.Requests = types.SumInt(total.Requests, rec.Requests)
		total.Retries = types.SumInt(total.Retries, rec.Retries)
		total.Repairs = types.SumInt(total.Repairs, rec.Repairs)
		total.CacheHits = types.SumInt(total.CacheHits, rec.CacheHits)
		total.Hedged = total.Hedged || rec.Hedged
		total.Requested = min(total.Requested, rec.Requested)
		total.Delivered = min(total.Delivered, rec.Delivered)
		total.Typed = total.Typed && rec.Typed

		total.Tokens.Input = types.SumInt(total.Tokens.Input, rec.Tokens.Input)
		total.Tokens.Output = types.SumInt(total.Tokens.Output, rec.Tokens.Output)
		total.Tokens.Total = types.SumInt(total.Tokens.Total, rec.Tokens.Total)
		total.Tokens.Cached = types.SumInt(total.Tokens.Cached, rec.Tokens.Cached)
		total.Tokens.CacheWrite = types.SumInt(total.Tokens.CacheWrite, rec.Tokens.CacheWrite)
		total.Tokens.Reasoning = types.SumInt(total.Tokens.Reasoning, rec.Tokens.Reasoning)

		total.Cost.Total += rec.Cost.Total
		total.Cost.Input += rec.Cost.Input
		total.Cost.Output += rec.Cost.Output
		total.Cost.Cached += rec.Cost.Cached
		total.Cost.Reasoning += rec.Cost.Reasoning
		if rec.Requests > 0 {
			sent = true
			pricedAll = pricedAll && rec.Cost.Priced
		}
		total.Cost.Estimated = total.Cost.Estimated || rec.Cost.Estimated

		total.Timing.Calls = types.SumInt(total.Timing.Calls, rec.Timing.Calls)
		if rec.Timing.Calls > 0 {
			timed = true
			reusedAll = reusedAll && rec.Timing.Reused
		}
		total.Timing.Queued = types.SumDuration(total.Timing.Queued, rec.Timing.Queued)
		total.Timing.Lookup = types.SumDuration(total.Timing.Lookup, rec.Timing.Lookup)
		total.Timing.Connect = types.SumDuration(total.Timing.Connect, rec.Timing.Connect)
		total.Timing.Handshake = types.SumDuration(total.Timing.Handshake, rec.Timing.Handshake)
		total.Timing.Send = types.SumDuration(total.Timing.Send, rec.Timing.Send)
		total.Timing.Wait = types.SumDuration(total.Timing.Wait, rec.Timing.Wait)
		total.Timing.Read = types.SumDuration(total.Timing.Read, rec.Timing.Read)
		total.Timing.Network = types.SumDuration(total.Timing.Network, rec.Timing.Network)
	}
	total.Cost.Priced = sent && pricedAll
	total.Timing.Reused = timed && reusedAll
	total.Start = earliest
	total.Elapsed = max(latest.Sub(earliest), unstamped)

	total.Op = agreed(records, func(r Record) string { return r.Op })
	total.Provider = agreed(records, func(r Record) string { return r.Provider })
	total.Model = agreed(records, func(r Record) string { return r.Model })
	total.RequestedModel = agreed(records, func(r Record) string { return r.RequestedModel })
	total.ServiceTier = agreed(records, func(r Record) string { return r.ServiceTier })
	total.RequestID = agreed(records, func(r Record) string { return r.RequestID })
	total.CorrelationID = agreed(records, func(r Record) string { return r.CorrelationID })
	total.SchemaID = agreed(records, func(r Record) string { return r.SchemaID })
	total.Strategy = agreed(records, func(r Record) string { return r.Strategy })
	total.AnsweredBy = agreed(records, func(r Record) string { return r.AnsweredBy })
	total.AskedFirst = agreed(records, func(r Record) string { return r.AskedFirst })
	total.Escalated = agreed(records, func(r Record) string { return r.Escalated })
	total.Cost.Currency = agreed(records, func(r Record) string { return r.Cost.Currency })
	total.Cost.Source = agreed(records, func(r Record) string { return r.Cost.Source })
	return total
}

// agreed returns the value field gives for every record when it is the same for
// all of them, and the empty string otherwise.
func agreed(records []Record, field func(Record) string) string {
	first := field(records[0])
	for _, rec := range records[1:] {
		if field(rec) != first {
			return ""
		}
	}
	return first
}
