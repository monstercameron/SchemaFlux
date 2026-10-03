package ops

// dx12SummaryLength reports how long a summary came back against the length the
// caller asked for.
//
// TargetLength is written into the prompt, and a caller who asked for 50 words
// could be given 400 with nothing to say so. The refusal beyond the tolerance
// was added to SummarizeWithMetadata earlier; what was still missing is the
// other half, the figure itself, for a summary that is accepted. A caller
// deciding whether a summary that came back at 55 words against a target of 50
// is good enough has no way to ask, and a summary inside the tolerance and over
// the target is returned like one under it.
//
// The figures are measured here, in the unit the caller named, by the same
// function that decides the refusal (MeasureLength), so what is reported cannot
// disagree with what was accepted. They are not the model's claims.
//
// It returns nil when no target was set, because there is nothing to be within.
// An unnamed unit is measured in characters, which is what MeasureLength does
// for it and what WithinLength therefore checked.
func dx12SummaryLength(text string, opts SummarizeOptions) map[string]any {
	if opts.TargetLength <= 0 {
		return nil
	}

	unit := LengthUnit(opts.LengthUnit)
	if unit == "" {
		unit = LengthInCharacters
	}
	measured := MeasureLength(text, unit)

	return map[string]any{
		"length_measured":      measured,
		"length_unit":          string(unit),
		"length_target":        opts.TargetLength,
		"length_within_target": measured <= opts.TargetLength,
		"length_tolerance":     summaryLengthTolerance,
	}
}
