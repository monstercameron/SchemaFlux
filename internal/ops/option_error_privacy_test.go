package ops

import (
	"strings"
	"testing"
)

// A refused enum is still caller-controlled text. Keep its value out of the
// error that applications routinely write to their logs.
func TestInvalidStringOptionsNameTheFieldWithoutRepeatingTheValue(t *testing.T) {
	const canary = "PRIVATE-OPTION-CANARY"
	cases := []struct {
		name  string
		field string
		check func() error
	}{
		{"annotation format", "format", func() error { o := NewAnnotateOptions(); o.Format = canary; return o.Validate() }},
		{"cluster naming", "naming strategy", func() error { o := NewClusterOptions(); o.NamingStrategy = canary; return o.Validate() }},
		{"compression strategy", "strategy", func() error { o := NewCompressOptions(); o.Strategy = canary; return o.Validate() }},
		{"compression priority", "priority", func() error { o := NewCompressOptions(); o.Priority = canary; return o.Validate() }},
		{"compression format", "format", func() error { o := NewCompressOptions(); o.OutputFormat = canary; return o.Validate() }},
		{"critique style", "style", func() error {
			o := NewCritiqueOptions()
			o.Criteria = []string{"criterion"}
			o.Style = canary
			return o.Validate()
		}},
		{"decomposition strategy", "strategy", func() error { o := NewDecomposeOptions(); o.Strategy = canary; return o.Validate() }},
		{"enrichment depth", "depth", func() error { o := NewEnrichOptions(); o.Depth = canary; return o.Validate() }},
		{"validation severity", "FailOn", func() error { o := NewValidateOptions(); o.FailOn = canary; return o.Validate() }},
		{"explanation audience", "audience", func() error { o := NewExplainOptions(); o.Audience = canary; return o.Validate() }},
		{"explanation format", "format", func() error { o := NewExplainOptions(); o.Format = canary; return o.Validate() }},
		{"explanation focus", "focus", func() error { o := NewExplainOptions(); o.Focus = canary; return o.Validate() }},
		{"matching strategy", "strategy", func() error { o := NewMatchOptions(); o.Strategy = canary; return o.Validate() }},
		{"normalization case", "case", func() error { o := NewNormalizeOptions(); o.NormalizeCase = canary; return o.Validate() }},
		{"transformation merge", "merge strategy", func() error { o := NewTransformOptions(); o.MergeStrategy = canary; return o.Validate() }},
		{"summary length", "length unit", func() error { o := NewSummarizeOptions(); o.LengthUnit = canary; return o.Validate() }},
		{"comparison format", "format", func() error { o := NewCompareOptions(); o.OutputFormat = canary; return o.Validate() }},
		{"comparison focus", "focus", func() error { o := NewCompareOptions(); o.FocusOn = canary; return o.Validate() }},
		{"choice strategy", "strategy", func() error { o := NewChooseOptions(); o.Strategy = canary; return o.Validate() }},
		{"sort direction", "direction", func() error {
			o := NewSortOptions()
			o.Criteria = "criterion"
			o.Direction = canary
			return o.Validate()
		}},
		{"prediction method", "method", func() error { o := NewPredictOptions(); o.Method = canary; return o.Validate() }},
		{"synthesis strategy", "strategy", func() error { o := NewSynthesizeOptions(); o.Strategy = canary; return o.Validate() }},
		{"synthesis conflict", "conflict resolution", func() error { o := NewSynthesizeOptions(); o.ConflictResolution = canary; return o.Validate() }},
		{"verification strictness", "strictness", func() error { o := NewVerifyOptions(); o.Strictness = canary; return o.Validate() }},
		{"suggestion strategy", "strategy", func() error { o := NewSuggestOptions(); o.Strategy = canary; return o.Validate() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.check()
			if err == nil {
				t.Fatal("the invalid option was accepted")
			}
			if strings.Contains(err.Error(), canary) {
				t.Error("the error repeated the caller's option value")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.field)) {
				t.Errorf("error %q does not identify the refused field %q", err, tc.field)
			}
		})
	}
}
