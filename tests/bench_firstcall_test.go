package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/llm"
	"github.com/monstercameron/schemaflux/internal/telemetry"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                                    allocs/op       B/op
//	FirstCallInFreshProcess/stub_default_logging       412      55916  433.5 first-allocs  1.145e+04 first-us  100.7 later-us
//	FirstCallInFreshProcess/stub_quiet                 412      55908  414 first-allocs  1.088e+04 first-us  74.43 later-us
//	FirstCallInFreshProcess/openai_local_http          412      55884  1253 first-allocs  2.649e+04 first-us  258.6 later-us
//	FirstCallInFreshProcess/openai_local_tls           416      57504  2091 first-allocs  4.902e+04 first-us  273.8 later-us

// What the first call of a process costs beyond the hundredth.
//
// The first Extract in a process does work that later ones do not: encoding/json
// builds its encoder and decoder for each type it meets, the first schema walks
// run cold, the first write to the logger's handler, the first connection of
// the HTTP transport (and, for TLS, its handshake). None of that can be
// measured inside a process that has already made a call, so each sample here
// is a fresh process: this test binary is run again, as a child, with
// -test.run set to TestFirstCallChild and an environment variable that tells
// the child which provider to use. The child makes its calls one after another
// and prints, on one line, how long the first took, the second, the hundredth,
// the median of calls 21 to 100, and the allocations of the first and of the
// hundredth. The parent runs the child 20 times per kind and reports medians.
//
// The kinds:
//
//	stub_default_logging   the stub provider that answers at once, logging at its default
//	stub_quiet             the same with the logging off: the library's own cold cost
//	openai_local_http      the OpenAI Responses provider against an httptest server on loopback
//	openai_local_tls       the same over TLS: adds the handshake and the certificate check
//
// What it shows: the cost of the first call that the library and the standard
// library's per-type and per-connection caches add, in a process that has
// finished its start-up. What it does not show: package initialisation (the
// child's clock starts after the test binary has initialised and built its
// client; GODEBUG=inittrace=1 reports init), DNS, a connection to a distant
// host, or the platform's certificate store (the TLS kind trusts the test
// server's own certificate, so the system roots are not loaded, which is the
// largest part of a first TLS call to a real host on some platforms). The
// time of the first call to a real API therefore is larger than any figure
// here by the network and by those; this is the floor, not the total.
//
// Each call is timed with benchClockNanos, a counter that resolves tens of
// nanoseconds; Go's own clock on Windows advances about once a millisecond and
// would measure a 60-microsecond call as zero.
//
// Measured figures are in bench_baseline_summary.txt; the test logs them with
// -v. It starts 80 processes, so it takes about fifteen seconds, and is skipped unless
// SCHEMAFLUX_MEASURE=1 is set. To run it:
//
//	SCHEMAFLUX_MEASURE=1 go test ./tests/ -run TestFirstCallCost -v
//
// bench_firstcall_pieces_test.go takes the first call apart into the pieces it
// is made of.
//
// BenchmarkFirstCallInFreshProcess runs one child per iteration and reports the
// first and the hundredth call as metrics, for use with -count=20 and benchstat.

const (
	firstCallChildEnv = "SCHEMAFLUX_FIRST_CALL_CHILD"
	firstCallCalls    = 100
	firstCallSamples  = 20
)

type firstCallSample struct {
	FirstNS        int64  `json:"first_ns"`
	SecondNS       int64  `json:"second_ns"`
	HundredthNS    int64  `json:"hundredth_ns"`
	MedianLateNS   int64  `json:"median_21_to_100_ns"`
	FirstAllocs    uint64 `json:"first_allocs"`
	HundredthAlloc uint64 `json:"hundredth_allocs"`
	FirstBytes     uint64 `json:"first_bytes"`
}

var firstCallKinds = []string{"stub_default_logging", "stub_quiet", "openai_local_http", "openai_local_tls"}

// TestFirstCallChild is the process under measurement. Run on its own it does
// nothing; the parent sets the environment variable.
func TestFirstCallChild(t *testing.T) {
	kind := os.Getenv(firstCallChildEnv)
	if kind == "" {
		t.Skip("run by TestFirstCallCost, which sets " + firstCallChildEnv)
	}
	const answer = `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`

	var provider llm.Provider
	switch kind {
	case "stub_default_logging", "stub_quiet":
		provider = benchProvider{answer}
	case "openai_local_http", "openai_local_tls":
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body bytes.Buffer
			_, _ = body.ReadFrom(r.Body)
			w.Header().Set("Content-Type", "application/json")
			encoded, _ := json.Marshal(answer)
			_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","model":"gpt-6-luna","output":[{"type":"message","content":[{"type":"output_text","text":` +
				string(encoded) + `}]}],"usage":{"input_tokens":120,"output_tokens":24,"total_tokens":144}}`))
		})
		config := llm.ProviderConfig{APIKey: "bench-key", Timeout: 10 * time.Second}
		if kind == "openai_local_tls" {
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			config.BaseURL = server.URL
			config.HTTPClient = server.Client()
		} else {
			server := httptest.NewServer(handler)
			defer server.Close()
			config.BaseURL = server.URL
		}
		openai, err := llm.NewOpenAIProvider(config)
		if err != nil {
			t.Fatal(err)
		}
		provider = openai
	default:
		t.Fatalf("unknown kind %q", kind)
	}

	if kind == "stub_quiet" {
		schemaflux.ConfigureLogging(telemetry.LoggerConfig{Level: telemetry.ErrorLevel, DisableStderr: true})
	} else {
		schemaflux.ConfigureLogging(telemetry.DefaultLoggerConfig())
	}
	ctx := schemaflux.NewClient("").WithProviderInstance(provider).Context(context.Background())
	opts := schemaflux.NewExtractOptions()
	opts.CommonOptions = opts.CommonOptions.WithContext(ctx)

	durations := make([]int64, firstCallCalls)
	var firstAllocs, hundredthAllocs, firstBytes uint64
	for i := 0; i < firstCallCalls; i++ {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := benchClockNanos()
		if _, err := schemaflux.Extract[benchInvoice](benchText, opts); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		durations[i] = benchClockNanos() - start
		runtime.ReadMemStats(&after)
		switch i {
		case 0:
			firstAllocs, firstBytes = after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc
		case firstCallCalls - 1:
			hundredthAllocs = after.Mallocs - before.Mallocs
		}
	}
	late := append([]int64(nil), durations[20:]...)
	sort.Slice(late, func(i, j int) bool { return late[i] < late[j] })

	line, _ := json.Marshal(firstCallSample{
		FirstNS: durations[0], SecondNS: durations[1], HundredthNS: durations[firstCallCalls-1],
		MedianLateNS: late[len(late)/2], FirstAllocs: firstAllocs, HundredthAlloc: hundredthAllocs, FirstBytes: firstBytes,
	})
	os.Stdout.WriteString("FIRSTCALL " + string(line) + "\n")
}

// runFirstCallChild runs the child once and returns what it printed.
func runFirstCallChild(tb testing.TB, kind string) firstCallSample {
	tb.Helper()
	executable, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestFirstCallChild$", "-test.count=1")
	command.Env = append(os.Environ(), firstCallChildEnv+"="+kind)
	// The child's standard error is not read: at its default settings the
	// library writes its log lines there.
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		tb.Fatalf("the child process for %s failed: %v\n%s", kind, err, stdout.String())
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, "FIRSTCALL "); ok {
			var sample firstCallSample
			if err := json.Unmarshal([]byte(rest), &sample); err != nil {
				tb.Fatal(err)
			}
			return sample
		}
	}
	tb.Fatalf("the child process for %s printed no result:\n%s", kind, stdout.String())
	return firstCallSample{}
}

func medianOf(values []int64) int64 {
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// measureProcesses reports whether the tests that start many processes were
// asked for. They are measurements and assert nothing, so a plain `go test`
// (which CI runs repeatedly, shuffled) skips them.
func measureProcesses(t *testing.T) {
	t.Helper()
	if os.Getenv("SCHEMAFLUX_MEASURE") != "1" {
		t.Skip("starts many processes and asserts nothing; set SCHEMAFLUX_MEASURE=1 to run it")
	}
}

func TestFirstCallCost(t *testing.T) {
	measureProcesses(t)
	for _, kind := range firstCallKinds {
		t.Run(kind, func(t *testing.T) {
			var first, second, hundredth, late, allocsFirst, allocsLate []int64
			for i := 0; i < firstCallSamples; i++ {
				sample := runFirstCallChild(t, kind)
				first = append(first, sample.FirstNS)
				second = append(second, sample.SecondNS)
				hundredth = append(hundredth, sample.HundredthNS)
				late = append(late, sample.MedianLateNS)
				allocsFirst = append(allocsFirst, int64(sample.FirstAllocs))
				allocsLate = append(allocsLate, int64(sample.HundredthAlloc))
			}
			t.Logf("%s over %d fresh processes (medians): first call %.0f us, second %.0f us, hundredth %.0f us, calls 21-100 %.0f us; allocations first %d, hundredth %d",
				kind, firstCallSamples,
				float64(medianOf(first))/1e3, float64(medianOf(second))/1e3, float64(medianOf(hundredth))/1e3, float64(medianOf(late))/1e3,
				medianOf(allocsFirst), medianOf(allocsLate))
		})
	}
}

// BenchmarkFirstCallInFreshProcess is one child per iteration, reporting the
// first call and the later calls as metrics (in microseconds) for benchstat.
func BenchmarkFirstCallInFreshProcess(b *testing.B) {
	for _, kind := range firstCallKinds {
		b.Run(kind, func(b *testing.B) {
			var first, late, allocs int64
			for i := 0; i < b.N; i++ {
				sample := runFirstCallChild(b, kind)
				first += sample.FirstNS
				late += sample.MedianLateNS
				allocs += int64(sample.FirstAllocs)
			}
			n := int64(b.N)
			b.ReportMetric(float64(first/n)/1e3, "first-us")
			b.ReportMetric(float64(late/n)/1e3, "later-us")
			b.ReportMetric(float64(allocs/n), "first-allocs")
		})
	}
}
