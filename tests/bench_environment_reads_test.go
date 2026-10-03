package tests

import (
	"flag"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
)

// How many times one call reads the environment.
//
// os.Getenv cannot be counted from inside a process by any hook the standard
// library offers (the one it has, internal/testlog, records each name once and
// is not importable), so this is counted by the compiler instead. Built with
// coverage counters on the standard library's own environment function
// (-coverpkg=syscall,os, -covermode=atomic), the test binary records how many
// times the function's body was entered, which is the number of reads, from
// every caller, exactly: no sampling, and nothing the library does can avoid
// being counted, including a read made through os.LookupEnv or by a package
// this library imports.
//
// A coverage profile has the counts for the whole process, including the
// test binary's start-up and the reads the testing package makes, so the
// number per call is the difference of two runs divided by the calls that
// differ between them:
//
//	go test ./tests/ -run TestEnvironmentReadsPerExtract -coverpkg=syscall,os \
//	    -covermode=atomic -coverprofile=reads0.out  -args -env-extract-calls=0
//	go test ./tests/ -run TestEnvironmentReadsPerExtract -coverpkg=syscall,os \
//	    -covermode=atomic -coverprofile=reads1000.out -args -env-extract-calls=1000
//
// then, in each profile, the count on the line of syscall.Getenv's body
// (syscall/env_windows.go on Windows; syscall/env_unix.go elsewhere), and
// (reads1000 - reads0) / 1000 is the number of reads per Extract. The test's
// own part is only the calls: it makes -env-extract-calls Extracts through the
// stub provider with the logging off (so the logger's configuration reads, which
// happen once, are not in the count) and fails if any returns an error.
//
// -env-op=extractresult, summarize or is counts another operation (extract is
// the default).
//
// Without coverage on, the test is skipped: it has nothing to assert, only a
// count to produce. The figure it produced when the baseline was recorded is in
// bench_baseline_summary.txt, with the date of the run, since the number changes
// as the library does.

var environmentExtractCalls = flag.Int("env-extract-calls", 0, "number of calls TestEnvironmentReadsPerExtract makes")

// environmentOperation is which call is counted: extract (the default),
// extractresult, is or summarize.
var environmentOperation = flag.String("env-op", "extract", "the operation TestEnvironmentReadsPerExtract calls")

func TestEnvironmentReadsPerExtract(t *testing.T) {
	if testing.CoverMode() == "" {
		t.Skip("counts reads through coverage counters on the standard library; see the comment at the top of this file")
	}
	benchSilenceLogging(t)
	ctx := benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
	extractOpts := schemaflux.NewExtractOptions()
	extractOpts.CommonOptions = extractOpts.CommonOptions.WithContext(ctx)
	summaryCtx := benchContext("A summary.")
	summarizeOpts := schemaflux.NewSummarizeOptions()
	summarizeOpts.CommonOptions = summarizeOpts.CommonOptions.WithContext(summaryCtx)
	decisionCtx := benchContext(`{"q1":false}`)

	var call func() error
	switch *environmentOperation {
	case "extract":
		benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
		call = func() error { _, err := schemaflux.Extract[benchInvoice](benchText, extractOpts); return err }
	case "extractresult":
		benchContext(`{"order_id":"A-2207","product":"Team plan","amount":"$49"}`)
		call = func() error { _, err := schemaflux.ExtractResult[benchInvoice](benchText, extractOpts); return err }
	case "summarize":
		benchContext("A summary.")
		call = func() error { _, err := schemaflux.Summarize(benchText, summarizeOpts); return err }
	case "is":
		benchContext(`{"q1":false}`)
		call = func() error { _, err := schemaflux.Is(decisionCtx, benchText, "Is this message spam?"); return err }
	default:
		t.Fatalf("unknown -env-op %q", *environmentOperation)
	}
	// One call first, outside the count: whatever a process reads once (the
	// logger's configuration, the first pricing lookup) is not a per-call read.
	if err := call(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < *environmentExtractCalls; i++ {
		if err := call(); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
}
