package exp

import (
	"context"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// SynthesizeOptions configures Synthesize. Its zero value uses the operation's
// defaults: integrate, llm-decide, citations and insights included.
type SynthesizeOptions struct {
	schemaflux.Options

	// Strategy is merge, compare, integrate or reconcile. Empty means integrate.
	Strategy string
	// ConflictResolution is latest-wins, source-priority, majority or llm-decide.
	// Empty means llm-decide.
	ConflictResolution string
	// SourcePriorities orders source indexes for source-priority resolution. Nil
	// means none; when used, it must list valid source indexes without repetition.
	SourcePriorities []int
	// OmitCitations leaves source references out of the request. False asks for
	// citations and requires every reported fact to cite a supplied source.
	OmitCitations bool
	// OmitInsights leaves insights out of the request. False asks for them.
	OmitInsights bool
	// OutputStructure describes how to organise the result. Empty means unstated.
	OutputStructure string
	// FocusAreas are subjects to concentrate on. Nil means none.
	FocusAreas []string
	// ExcludeAspects are subjects to leave out. Nil means none.
	ExcludeAspects []string
}

// SynthesizeResult combines the synthesized value with the facts, insights and
// conflicts the model reported.
type SynthesizeResult[T any] struct {
	// Synthesized is the combined value of type T.
	Synthesized T `json:"synthesized"`
	// Facts are the facts the model reported from the sources; nil means it reported none.
	Facts []SynthesisFact `json:"facts,omitempty"`
	// Insights are what the model reported; OmitInsights does not forbid an insight.
	Insights []SynthesisInsight `json:"insights,omitempty"`
	// Conflicts are the disagreements and resolutions the model reported.
	Conflicts []SynthesisConflict `json:"conflicts,omitempty"`
	// SourceCoverage is the model's own claim about how much of each source it used.
	SourceCoverage map[int]float64 `json:"source_coverage"`
}

// SynthesisFact is one fact reported from the sources.
type SynthesisFact struct {
	// Fact is the reported fact.
	Fact string `json:"fact"`
	// Sources lists source indexes that support the fact.
	Sources []int `json:"sources"`
	// Conflicts contains conflicts mentioned with the fact.
	Conflicts []string `json:"conflicts,omitempty"`
}

// SynthesisInsight is an insight reported across sources.
type SynthesisInsight struct {
	// Insight is the reported pattern or observation.
	Insight string `json:"insight"`
	// Supporting lists source indexes that support the insight.
	Supporting []int `json:"supporting_sources"`
	// Type is pattern, gap, contradiction or trend.
	Type string `json:"type"`
}

// SynthesisConflict is one disagreement the model reported and its resolution.
type SynthesisConflict struct {
	// Topic names the disagreement.
	Topic string `json:"topic"`
	// Positions maps source indexes to the positions they take.
	Positions map[int]string `json:"positions"`
	// Resolution describes how the disagreement was resolved.
	Resolution string `json:"resolution"`
	// Chosen is the source index whose position was followed.
	Chosen int `json:"chosen_source"`
}

func mergeSynthesizeOptions(values []SynthesizeOptions) SynthesizeOptions {
	return mergeExpValues(values, func(out, later SynthesizeOptions) SynthesizeOptions {
		out.Options = mergeCommonOptions([]schemaflux.Options{out.Options, later.Options})
		if later.Strategy != "" {
			out.Strategy = later.Strategy
		}
		if later.ConflictResolution != "" {
			out.ConflictResolution = later.ConflictResolution
		}
		if later.SourcePriorities != nil {
			out.SourcePriorities = later.SourcePriorities
		}
		if later.OmitCitations {
			out.OmitCitations = true
		}
		if later.OmitInsights {
			out.OmitInsights = true
		}
		if later.OutputStructure != "" {
			out.OutputStructure = later.OutputStructure
		}
		if later.FocusAreas != nil {
			out.FocusAreas = later.FocusAreas
		}
		if later.ExcludeAspects != nil {
			out.ExcludeAspects = later.ExcludeAspects
		}
		return out
	})
}

// synthesizeCommon checks and converts common settings for a Synthesize call.
func synthesizeCommon(o SynthesizeOptions) (bind.Common, error) {
	c, err := commonSettings(o.Options)
	if err != nil {
		return bind.Common{}, err
	}
	if err := c.Check("exp.synthesize"); err != nil {
		return bind.Common{}, err
	}
	return c, nil
}

// synthesizeInternal builds internal options on the context used by this
// branch. Constructor defaults preserve included citations and insights.
func synthesizeInternal(ctx context.Context, o SynthesizeOptions, c bind.Common) ops.SynthesizeOptions {
	in := ops.NewSynthesizeOptions()
	if o.Strategy != "" {
		in.Strategy = o.Strategy
	}
	if o.ConflictResolution != "" {
		in.ConflictResolution = o.ConflictResolution
	}
	if o.SourcePriorities != nil {
		in.SourcePriorities = o.SourcePriorities
	}
	in.CiteSources = !o.OmitCitations
	in.GenerateInsights = !o.OmitInsights
	if o.OutputStructure != "" {
		in.OutputStructure = o.OutputStructure
	}
	if o.FocusAreas != nil {
		in.FocusAreas = o.FocusAreas
	}
	if o.ExcludeAspects != nil {
		in.ExcludeAspects = o.ExcludeAspects
	}
	applyFlatCommon(ctx, c, &in.CommonOptions, &in.OpOptions)
	return in
}

// Synthesize combines several sources into one T and reports the facts,
// insights and conflicts it found. Default tier Fast, mode Transform.
//
//	result, err := exp.Synthesize[Briefing](ctx, []any{reportA, reportB, notes})
//	if err == nil { fmt.Println(result.Synthesized, len(result.Conflicts)) }
//
// At least one source is required. The Record is "exp.synthesize" and is
// delivered at ContractJSONWellFormed: the answer is parsed and source
// references are checked, but the request declares no schema.
func Synthesize[T any](ctx context.Context, sources []any, options ...SynthesizeOptions) (SynthesizeResult[T], error) {
	var zero SynthesizeResult[T]
	call, err := bind.Begin(ctx, "exp.synthesize")
	if err != nil {
		return zero, err
	}
	o := mergeSynthesizeOptions(options)
	if err := refuseExpEmpty(&call, "sources", len(sources)); err != nil {
		return zero, err
	}
	c, err := synthesizeCommon(o)
	if err != nil {
		return zero, call.End(nil, err)
	}
	if call.Plain() {
		res, err := ops.Synthesize[T](sources, synthesizeInternal(call.Context(), o, c))
		return synthesizeResult(res), call.End(nil, err)
	}
	res, err := bind.Recorded(&call, types.ContractJSONWellFormed, func(ctx context.Context) (ops.SynthesizeResult[T], error) {
		return ops.Synthesize[T](sources, synthesizeInternal(ctx, o, c))
	})
	return synthesizeResult(res), err
}

func synthesizeResult[T any](res ops.SynthesizeResult[T]) SynthesizeResult[T] {
	if len(res.Facts) == 0 && len(res.Insights) == 0 && len(res.Conflicts) == 0 {
		return SynthesizeResult[T]{Synthesized: res.Synthesized, SourceCoverage: res.SourceCoverage}
	}
	out := SynthesizeResult[T]{Synthesized: res.Synthesized, SourceCoverage: res.SourceCoverage}
	if len(res.Facts) != 0 {
		out.Facts = make([]SynthesisFact, len(res.Facts))
		for i, f := range res.Facts {
			out.Facts[i] = SynthesisFact{Fact: f.Fact, Sources: f.Sources, Conflicts: f.Conflicts}
		}
	}
	if len(res.Insights) != 0 {
		out.Insights = make([]SynthesisInsight, len(res.Insights))
		for i, v := range res.Insights {
			out.Insights[i] = SynthesisInsight{Insight: v.Insight, Supporting: v.Supporting, Type: v.Type}
		}
	}
	if len(res.Conflicts) != 0 {
		out.Conflicts = make([]SynthesisConflict, len(res.Conflicts))
		for i, v := range res.Conflicts {
			out.Conflicts[i] = SynthesisConflict{Topic: v.Topic, Positions: v.Positions, Resolution: v.Resolution, Chosen: v.Chosen}
		}
	}
	return out
}
