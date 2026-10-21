package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// What a caller wrote is not written to a log: the language Translate is asked
// to translate into is the caller's value, and the lines a call writes say how
// long its input was and which request it is, and nothing the caller chose. A
// client's logger is given the lines of its own calls, so it is the one searched.
func TestTranslateLogsNothingTheCallerWrote(t *testing.T) {
	const canary = "CANARY-LANGUAGE-7b3e"
	swapLLMCaller(t, func(context.Context, string, string, types.OpOptions) (string, error) {
		return "", errors.New("provider down")
	})
	cases := []struct {
		name string
		call func(ctx context.Context, language string) error
	}{
		{"Translate", func(ctx context.Context, language string) error {
			_, err := Translate("Good morning", TranslateOptions{CommonOptions: CommonOptions{Context: ctx}, TargetLanguage: language})
			return err
		}},
		{"TranslateWithMetadata", func(ctx context.Context, language string) error {
			_, err := TranslateWithMetadata("Good morning", TranslateOptions{CommonOptions: CommonOptions{Context: ctx}, TargetLanguage: language})
			return err
		}},
		{"TranslateResult", func(ctx context.Context, language string) error {
			_, err := TranslateResult("Good morning", TranslateOptions{CommonOptions: CommonOptions{Context: ctx}, TargetLanguage: language})
			return err
		}},
	}
	languages := map[string]string{
		"a plain language":           "Spanish-" + canary,
		"a language with a newline":  canary + "\nlevel=ERROR forged",
		"a language that is a quote": `"` + canary + `"`,
	}
	for _, tc := range cases {
		for how, language := range languages {
			t.Run(tc.name+" with "+how, func(t *testing.T) {
				client, written := capturedLogger()
				process, processWritten := capturedLogger()
				asProcessLogger(t, process)
				ctx := WithExecConfig(context.Background(), &ExecConfig{Logger: client})

				if err := tc.call(ctx, language); err == nil {
					t.Fatal("the call did not fail, so the lines of its failure were not written")
				}
				if !strings.Contains(written.String(), "Starting translate") {
					t.Fatalf("the client's logger was given no start line:\n%s", written)
				}
				if strings.Contains(written.String(), canary) || strings.Contains(processWritten.String(), canary) {
					t.Errorf("a log line carries the caller's language:\n%s%s", written, processWritten)
				}
			})
		}
	}
}

// The warning that a model has no price is a line of the call that priced it: it
// goes to the logger of the client the call was made on and not to the
// process's, and it names the model as a bounded value. Each run uses a model
// nothing has priced before, because the warning is written once for each model
// in a process.
func TestTheUnpricedModelWarningGoesToTheClientsLoggerOfTheCall(t *testing.T) {
	process, processWritten := capturedLogger()
	asProcessLogger(t, process)
	client, clientWritten := capturedLogger()
	ctx := WithExecConfig(context.Background(), &ExecConfig{Logger: client})

	unique := fmt.Sprintf("unpriced-route-model-%d", time.Now().UnixNano())
	model := unique + "\nlevel=ERROR forged " + strings.Repeat("x", 100)
	provider := &callerSuppliedProvider{name: "acme-unpriced", finishReason: "stop"}
	opts := types.OpOptions{Mode: types.Strict, Intelligence: types.Fast, Model: model}
	if _, err := CallLLM(ctx, provider, "Return JSON.", "input", opts); err != nil {
		t.Fatalf("CallLLM: %v", err)
	}

	const warning = "No pricing information available"
	if !strings.Contains(clientWritten.String(), warning) {
		t.Fatalf("the client's logger was not given the warning:\n%s", clientWritten)
	}
	if strings.Contains(processWritten.String(), warning) {
		t.Errorf("the process logger was given the warning of a call made on a client:\n%s", processWritten)
	}
	var line string
	for _, entry := range strings.Split(clientWritten.String(), "\n") {
		if strings.Contains(entry, warning) {
			line = entry
		}
	}
	// The model is written with its newline replaced by a question mark and cut
	// to the bound.
	if !strings.Contains(line, unique+"?level=ERROR forged x") {
		t.Errorf("the warning does not carry the model bounded:\n%s", line)
	}
	if strings.Contains(line, strings.Repeat("x", 100)) {
		t.Errorf("the model name was written whole in the warning:\n%s", line)
	}
}
