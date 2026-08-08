package tests

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/schemafluxtest"
)

// TI-004. The exact prompt each operation sends, for a fixed input.
//
// A prompt is behaviour. Editing a line in one changes what every downstream
// caller gets back, and a Go API that did not change is no evidence that
// nothing did -- which is the whole of Gap-13. This makes a prompt edit a diff
// in review rather than a change nobody sees until output quality moves.
//
// It also gives PS-007 something to version: you cannot version an artifact you
// cannot see.
//
// Regenerate deliberately:
//
//	SCHEMAFLUX_UPDATE_PROMPTS=1 go test . -run TestGoldenPrompts
const goldenPromptsPath = "../testdata/golden_prompts.txt"

// The inputs are fixed and boring on purpose: the subject is the prompt, and a
// varying input would make every diff unreadable.
type goldenTicket struct {
	ID      string  `json:"id"`
	Subject string  `json:"subject"`
	Amount  float64 `json:"amount"`
}

func goldenTickets() []goldenTicket {
	return []goldenTicket{
		{ID: "T-1", Subject: "charged twice", Amount: 42.5},
		{ID: "T-2", Subject: "cannot log in", Amount: 0},
	}
}

const goldenText = "The invoice INV-4417 from Northwind Traders totals $1284.50 and is due on the 30th."

func TestGoldenPrompts(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{"Extract", func() error {
			_, err := schemaflux.Extract[goldenTicket](goldenText, schemaflux.NewExtractOptions())
			return err
		}},
		{"Extract_Strict", func() error {
			_, err := schemaflux.Extract[goldenTicket](goldenText,
				schemaflux.NewExtractOptions().WithMode(schemaflux.Strict))
			return err
		}},
		{"Extract_Creative", func() error {
			_, err := schemaflux.Extract[goldenTicket](goldenText,
				schemaflux.NewExtractOptions().WithMode(schemaflux.Creative))
			return err
		}},
		{"Extract_Steered", func() error {
			_, err := schemaflux.Extract[goldenTicket](goldenText,
				schemaflux.NewExtractOptions().WithSteering("Use only facts explicitly present"))
			return err
		}},
		{"Classify", func() error {
			_, err := schemaflux.Classify[string, string]("charged twice",
				schemaflux.NewClassifyOptions().WithCategories([]string{"billing", "technical"}))
			return err
		}},
		{"Summarize", func() error {
			_, err := schemaflux.Summarize(goldenText, schemaflux.NewSummarizeOptions())
			return err
		}},
		{"Choose", func() error {
			_, err := schemaflux.Choose(goldenTickets(),
				schemaflux.NewChooseOptions().WithSteering("the most urgent"))
			return err
		}},
		{"Filter", func() error {
			_, err := schemaflux.Filter(goldenTickets(),
				schemaflux.NewFilterOptions().WithCriteria("billing problems"))
			return err
		}},
		{"Sort", func() error {
			_, err := schemaflux.Sort(goldenTickets(),
				schemaflux.NewSortOptions().WithCriteria("largest amount first"))
			return err
		}},
		{"Validate", func() error {
			_, err := schemaflux.Validate(goldenTickets()[0],
				schemaflux.NewValidateOptions().WithRules("the subject must describe a problem"))
			return err
		}},
		{"Transform", func() error {
			_, err := schemaflux.Transform[goldenTicket, goldenTicket](goldenTickets()[0],
				schemaflux.NewTransformOptions())
			return err
		}},
		{"Generate", func() error {
			_, err := schemaflux.Generate[goldenTicket]("a support ticket about a duplicate charge",
				schemaflux.NewGenerateOptions())
			return err
		}},
		{"Score", func() error {
			_, err := schemaflux.Score(goldenTickets()[0], schemaflux.NewScoreOptions())
			return err
		}},
		{"Compare", func() error {
			_, err := schemaflux.Compare(goldenTickets()[0], goldenTickets()[1],
				schemaflux.NewCompareOptions())
			return err
		}},
	}

	var recorded []string
	for _, tc := range cases {
		provider := schemafluxtest.New().Shaped()
		restore := schemafluxtest.Install(t, provider)

		// The error is not the subject. Several of these operations refuse the
		// shaped answer, which is them working; what is being recorded is the
		// request that went out.
		_ = tc.run()
		restore()

		requests := provider.Requests()
		if len(requests) == 0 {
			t.Errorf("%s sent no request", tc.name)
			continue
		}

		recorded = append(recorded, renderPrompt(tc.name, requests[0]))
	}

	sort.Strings(recorded)
	current := strings.Join(recorded, "\n")

	if os.Getenv("SCHEMAFLUX_UPDATE_PROMPTS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("creating testdata: %v", err)
		}
		if err := os.WriteFile(goldenPromptsPath, []byte(current), 0o644); err != nil {
			t.Fatalf("writing %s: %v", goldenPromptsPath, err)
		}
		t.Logf("golden prompts updated: %s", goldenPromptsPath)
		return
	}

	raw, err := os.ReadFile(goldenPromptsPath)
	if err != nil {
		t.Fatalf("reading %s: %v (set SCHEMAFLUX_UPDATE_PROMPTS=1 to create it)", goldenPromptsPath, err)
	}

	if string(raw) != current {
		t.Errorf("a prompt changed.\n\n%s\n\n"+
			"A prompt is behaviour: this changes what every caller gets back, with no Go API\n"+
			"change to show for it. If it is intended, note it in TODOS.md and regenerate:\n"+
			"    SCHEMAFLUX_UPDATE_PROMPTS=1 go test . -run TestGoldenPrompts",
			firstDifference(string(raw), current))
	}
}

func renderPrompt(name string, req schemaflux.CompletionRequest) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "=== %s\n", name)
	fmt.Fprintf(&builder, "--- response format: %s\n", req.ResponseFormat)
	fmt.Fprintf(&builder, "--- json schema: %t\n", len(req.JSONSchema) > 0)
	fmt.Fprintf(&builder, "--- system\n%s\n", strings.TrimRight(req.SystemPrompt, "\n"))
	fmt.Fprintf(&builder, "--- user\n%s\n", strings.TrimRight(req.UserPrompt, "\n"))

	return builder.String()
}

// firstDifference reports the first differing line with a little context, which
// is what a reader needs; the whole prompt corpus is thousands of lines.
func firstDifference(before, after string) string {
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")

	limit := len(beforeLines)
	if len(afterLines) < limit {
		limit = len(afterLines)
	}

	for i := 0; i < limit; i++ {
		if beforeLines[i] == afterLines[i] {
			continue
		}

		var context strings.Builder
		start := i - 3
		if start < 0 {
			start = 0
		}
		for j := start; j < i; j++ {
			fmt.Fprintf(&context, "  %s\n", beforeLines[j])
		}
		fmt.Fprintf(&context, "- %s\n", beforeLines[i])
		fmt.Fprintf(&context, "+ %s\n", afterLines[i])
		return context.String()
	}

	if len(beforeLines) != len(afterLines) {
		return fmt.Sprintf("the corpus changed length: %d lines recorded, %d now",
			len(beforeLines), len(afterLines))
	}
	return "(no line differs, which should be impossible here)"
}
