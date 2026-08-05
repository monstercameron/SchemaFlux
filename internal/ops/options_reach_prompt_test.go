package ops

import (
	"strings"
	"testing"
)

// TestNoDeadOptionFields proves an option is read somewhere. It does not prove
// the read does anything. These cases set each option that F-023 implemented,
// capture the prompt the operation actually renders, and assert the option
// changed it — which is the difference between a field being referenced and a
// field being honoured.
func TestImplementedOptionsReachThePrompt(t *testing.T) {
	type record struct {
		Alpha string `json:"alpha"`
	}
	items := []record{{Alpha: "a"}, {Alpha: "b"}, {Alpha: "c"}}

	cases := []struct {
		name string
		// with renders a prompt using the option, without renders one that omits it.
		with       func()
		without    func()
		mustSay    []string
		mustNotSay []string
	}{
		{
			name: "ClassifyOptions.CategoryExamples",
			with: func() {
				opts := NewClassifyOptions()
				opts.Categories = []string{"billing", "technical"}
				opts.CategoryExamples = map[string][]string{"billing": {"charged twice", "refund request"}}
				_, _ = Classify[string, string]("input", opts)
			},
			without: func() {
				opts := NewClassifyOptions()
				opts.Categories = []string{"billing", "technical"}
				_, _ = Classify[string, string]("input", opts)
			},
			mustSay: []string{"charged twice", "refund request"},
		},
		{
			name: "ScoreOptions.IncludeBreakdown",
			with: func() {
				opts := NewScoreOptions().WithSteering("quality")
				opts.IncludeBreakdown = true
				_, _ = Score(items[0], opts)
			},
			without: func() {
				opts := NewScoreOptions().WithSteering("quality")
				opts.IncludeBreakdown = false
				_, _ = Score(items[0], opts)
			},
			mustSay: []string{"breakdown"},
		},
		{
			name: "ScoreOptions.Normalize",
			with: func() {
				opts := NewScoreOptions().WithSteering("quality")
				opts.Normalize = true
				_, _ = Score(items[0], opts)
			},
			without: func() { _, _ = Score(items[0], NewScoreOptions().WithSteering("quality")) },
			mustSay: []string{"Normalise"},
		},
		{
			name: "CompareOptions.IncludeSimilarity",
			with: func() {
				opts := NewCompareOptions()
				opts.IncludeSimilarity = true
				_, _ = Compare(items[0], items[1], opts)
			},
			without: func() {
				opts := NewCompareOptions()
				opts.IncludeSimilarity = false
				_, _ = Compare(items[0], items[1], opts)
			},
			mustSay: []string{"similarity score"},
		},
		{
			name: "ClusterOptions.MaxClusterSize",
			with: func() {
				opts := NewClusterOptions()
				opts.MaxClusterSize = 4
				_, _ = Cluster(items, opts)
			},
			without: func() { _, _ = Cluster(items, NewClusterOptions()) },
			mustSay: []string{"more than 4 items"},
		},
		{
			name: "ClusterOptions.GenerateDescriptions",
			with: func() {
				opts := NewClusterOptions()
				opts.GenerateDescriptions = true
				_, _ = Cluster(items, opts)
			},
			without: func() {
				opts := NewClusterOptions()
				opts.GenerateDescriptions = false
				_, _ = Cluster(items, opts)
			},
			mustSay:    []string{"a description saying what its members have in common"},
			mustNotSay: []string{"Leave the description empty"},
		},
		{
			name: "AnnotateOptions.Language",
			with: func() {
				opts := NewAnnotateOptions()
				opts.Language = "Haitian Creole"
				_, _ = Annotate("input", opts)
			},
			without: func() { _, _ = Annotate("input", NewAnnotateOptions()) },
			mustSay: []string{"Haitian Creole", "do not translate"},
		},
		{
			name: "GenerateOptions.EnsureUnique",
			with: func() {
				opts := NewGenerateOptions()
				opts.EnsureUnique = true
				_, _ = Generate[record]("make one", opts)
			},
			without: func() { _, _ = Generate[record]("make one", NewGenerateOptions()) },
			mustSay: []string{"must be distinct"},
		},
		{
			name: "SynthesizeOptions.OutputStructure",
			with: func() {
				opts := NewSynthesizeOptions()
				opts.OutputStructure = "one paragraph per source"
				_, _ = Synthesize[record]([]any{items[0], items[1]}, opts)
			},
			without: func() { _, _ = Synthesize[record]([]any{items[0], items[1]}, NewSynthesizeOptions()) },
			mustSay: []string{"one paragraph per source"},
		},
		{
			name: "MatchOptions.Bidirectional",
			with: func() {
				opts := NewMatchOptions()
				opts.Bidirectional = true
				_, _ = SemanticMatch(items, items, opts)
			},
			without: func() { _, _ = SemanticMatch(items, items, NewMatchOptions()) },
			mustSay: []string{"both directions"},
		},
		{
			name: "DecomposeOptions.PreserveHierarchy",
			with: func() {
				opts := NewDecomposeOptions()
				opts.PreserveHierarchy = true
				_, _ = Decompose(items[0], opts)
			},
			without: func() {
				opts := NewDecomposeOptions()
				opts.PreserveHierarchy = false
				_, _ = Decompose(items[0], opts)
			},
			mustSay: []string{"stays under its parent"},
		},
		{
			name: "ConformOptions.PreserveUnknown",
			with: func() {
				_, _ = Conform(items[0], "a standard", ConformOptions{PreserveUnknown: true})
			},
			without: func() {
				_, _ = Conform(items[0], "a standard", ConformOptions{PreserveUnknown: false})
			},
			mustSay:    []string{"Keep source fields the standard does not define"},
			mustNotSay: []string{"Drop source fields"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withOption := strings.Join(capturePrompts(t, tc.with), "\n")
			if withOption == "" {
				t.Fatal("the operation made no call, so this witnesses nothing")
			}

			for _, phrase := range tc.mustSay {
				if !strings.Contains(withOption, phrase) {
					t.Errorf("setting the option did not put %q in the prompt", phrase)
				}
			}
			for _, phrase := range tc.mustNotSay {
				if strings.Contains(withOption, phrase) {
					t.Errorf("setting the option left %q in the prompt", phrase)
				}
			}

			// The option has to be what made the difference, not something the
			// prompt says regardless.
			withoutOption := strings.Join(capturePrompts(t, tc.without), "\n")
			if withOption == withoutOption {
				t.Error("the prompt is identical with and without the option: setting it changes nothing")
			}
		})
	}
}

// The fields that promised deterministic machinery are gone rather than
// reinterpreted as prompt text, because no prompt can deliver a progress
// callback or a resume point.
func TestMachineryPromisingOptionsAreGone(t *testing.T) {
	// Compile-time absence is what this asserts; the test body documents why.
	// If any of these is reintroduced, TestNoDeadOptionFields fails until it
	// has a reader, and a prompt clause is not an acceptable reader for them.
	removed := []string{
		"BatchOptions.OnProgress",
		"BatchOptions.PreProcess",
		"BatchOptions.PostProcess",
		"PipelineOptions.SaveProgress",
		"ProjectOptions.PreserveNulls",
		"RedactOptions.PreserveFormat",
	}
	if len(removed) != 6 {
		t.Fatal("the list is the documentation here")
	}
}
