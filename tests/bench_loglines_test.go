package tests

import (
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// How many lines one call writes at the library's default logging.
//
// The cost of logging is a number of lines times the cost of a line, and the
// second number is in internal/telemetry's benchmarks. This is the first: one
// successful call of each kind, with the logger at its defaults (level Info,
// history capture on), and the lines the logger kept, by level and message.
// It asserts nothing, since the right number is a decision of the library's
// and not of this file; run it with -v:
//
//	go test ./tests/ -run TestLogLinesPerCall -v
func TestLogLinesPerCall(t *testing.T) {
	ctx := benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
	extractOpts := schemaflux.NewExtractOptions()
	extractOpts.CommonOptions = extractOpts.CommonOptions.WithContext(ctx)
	summaryCtx := benchContext("A summary.")
	summarizeOpts := schemaflux.NewSummarizeOptions()
	summarizeOpts.CommonOptions = summarizeOpts.CommonOptions.WithContext(summaryCtx)
	decisionCtx := benchContext(`{"q1":false}`)

	calls := []struct {
		name string
		run  func() error
	}{
		{"Extract", func() error { _, err := schemaflux.Extract[benchInvoice](benchText, extractOpts); return err }},
		{"ExtractResult", func() error { _, err := schemaflux.ExtractResult[benchInvoice](benchText, extractOpts); return err }},
		{"Summarize", func() error { _, err := schemaflux.Summarize(benchText, summarizeOpts); return err }},
		{"Is", func() error { _, err := schemaflux.Is(decisionCtx, benchText, "Is this message spam?"); return err }},
	}
	for _, call := range calls {
		// Each call is made with its own provider, installed by the context
		// above; the process default is whichever was installed last, so the
		// provider is installed again before the call.
		switch call.name {
		case "Summarize":
			benchContext("A summary.")
		case "Is":
			benchContext(`{"q1":false}`)
		default:
			benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
		}
		benchDefaultLogging(t)
		schemaflux.ResetLogEntries()
		if err := call.run(); err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		entries := schemaflux.GetLogEntries()
		byLevel := map[string]int{}
		for _, entry := range entries {
			byLevel[entry.Level]++
		}
		t.Logf("%s: %d lines kept at the default level (%v)", call.name, len(entries), byLevel)
		for _, entry := range entries {
			t.Logf("    %-5s %s", entry.Level, entry.Message)
		}
	}
}
