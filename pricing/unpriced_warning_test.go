package pricing

import (
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/logger"
	"github.com/monstercameron/schemaflux/internal/telemetry"
)

// freshUnpricedWarnings gives a test its own set of models already warned about
// and puts the previous one back when it ends.
func freshUnpricedWarnings(t *testing.T) {
	t.Helper()
	unpricedWarnMu.Lock()
	saved := unpricedWarned
	unpricedWarned = map[string]bool{}
	unpricedWarnMu.Unlock()
	t.Cleanup(func() {
		unpricedWarnMu.Lock()
		unpricedWarned = saved
		unpricedWarnMu.Unlock()
	})
}

// clientLog is a logger that is not the process's, holding what it was given.
func clientLog() (*logger.Logger, *lockedLog) {
	buffer := &lockedLog{}
	return telemetry.NewLoggerWithConfig(telemetry.LoggerConfig{Level: telemetry.DebugLevel, Format: "text", Output: buffer}), buffer
}

// The warning that a model has no price is written to the logger of the call
// that priced it, and not to the process's, so that a client which keeps its
// lines to itself keeps this one too.
func TestTheUnpricedWarningGoesToTheLoggerOfTheCallThatPricedIt(t *testing.T) {
	freshUnpricedWarnings(t)
	process := captureLog(t)
	client, written := clientLog()

	cost := CalculateCostLogged(client, usage(), "routed-model", "acme")
	if cost == nil || cost.Priced {
		t.Fatalf("an unpriced model was priced: %+v", cost)
	}
	if !strings.Contains(written.String(), "No pricing information available") {
		t.Errorf("the client's logger was not given the warning:\n%s", written.String())
	}
	if process() != "" {
		t.Errorf("the process logger was given a line that belongs to the client's call:\n%s", process())
	}
}

// With no logger of its own, a call writes to the process's, as CalculateCost
// always did, and a nil logger is that case and not a panic.
func TestTheUnpricedWarningOfACallWithNoLoggerGoesToTheProcessLogger(t *testing.T) {
	freshUnpricedWarnings(t)
	process := captureLog(t)

	CalculateCostLogged(nil, usage(), "no-logger-model", "acme")
	CalculateCost(usage(), "plain-model", "acme")
	got := process()
	for _, model := range []string{"no-logger-model", "plain-model"} {
		if !strings.Contains(got, model) {
			t.Errorf("the process logger was not given the warning for %s:\n%s", model, got)
		}
	}
}

// One warning for each model, whichever logger the first call that priced it
// had: the second call of the same (provider, model) writes nothing anywhere.
func TestTheUnpricedWarningIsWrittenOncePerModelWhateverTheLogger(t *testing.T) {
	freshUnpricedWarnings(t)
	process := captureLog(t)
	first, firstWritten := clientLog()
	second, secondWritten := clientLog()

	for range 20 {
		CalculateCostLogged(first, usage(), "once-model", "acme")
		CalculateCostLogged(second, usage(), "once-model", "acme")
		CalculateCost(usage(), "once-model", "acme")
	}
	if got := strings.Count(firstWritten.String(), "No pricing information"); got != 1 {
		t.Errorf("the first logger was given %d warnings, want 1", got)
	}
	if secondWritten.String() != "" || process() != "" {
		t.Errorf("a later call of the same model wrote a warning again:\n%s%s", secondWritten.String(), process())
	}
	CalculateCostLogged(second, usage(), "once-model", "other-acme")
	if got := strings.Count(secondWritten.String(), "No pricing information"); got != 1 {
		t.Errorf("a second provider of the same model was given %d warnings, want 1", got)
	}
}

// The model and the provider are names a caller or a provider chose. They are
// written bounded the way telemetry bounds a tag: at most 64 printable ASCII
// characters, so a name cannot forge a line or fill one.
func TestTheUnpricedWarningNamesTheModelBounded(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		want     string
		mustNot  string
		provider string
	}{
		{"a model that is fine", "ordinary-model", "ordinary-model", "", "acme"},
		{"a long model", strings.Repeat("m", 500), strings.Repeat("m", 64), strings.Repeat("m", 65), "acme"},
		{"a newline in a model", "model\nlevel=ERROR msg=forged", "model?level=ERROR msg=forged", "\nlevel=ERROR", "acme"},
		{"a control character", "model\x00\x1b[31m", "model??[31m", "\x1b", "acme"},
		{"a model that is not ASCII", "modèle-é", "mod??le-??", "è", "acme"},
		{"a long provider", "provider-model", "provider-model", strings.Repeat("p", 65), strings.Repeat("p", 500)},
		{"a newline in a provider", "newline-provider-model", "newline-provider-model", "\nlevel=ERROR", "acme\nlevel=ERROR msg=forged"},
		{"a model of exactly the bound", strings.Repeat("e", 64), strings.Repeat("e", 64), strings.Repeat("e", 65), "acme"},
		{"a model one past the bound", strings.Repeat("f", 65), strings.Repeat("f", 64), strings.Repeat("f", 65), "acme"},
		{"an empty model", "", "model=", "", "acme"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			freshUnpricedWarnings(t)
			client, written := clientLog()
			CalculateCostLogged(client, usage(), tc.model, tc.provider)
			got := written.String()
			if !strings.Contains(got, tc.want) {
				t.Errorf("case %d: the warning does not carry %q:\n%s", index, tc.want, got)
			}
			if tc.mustNot != "" && strings.Contains(got, tc.mustNot) {
				t.Errorf("case %d: the warning carries %q:\n%s", index, tc.mustNot, got)
			}
			if lines := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1; lines != 1 {
				t.Errorf("case %d: the warning is %d lines:\n%s", index, lines, got)
			}
		})
	}
}
