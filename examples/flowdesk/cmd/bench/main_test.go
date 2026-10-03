package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/flowdesk/desk"
	"github.com/monstercameron/schemaflux/examples/flowdesk/sim"
)

// Nothing here reaches a model or the network. The offline mode is the
// simulator, and the live report is run through the simulator in a model's
// place.

func TestMain(m *testing.M) {
	schemaflux.ConfigureLogging(schemaflux.LoggerConfig{Level: schemaflux.LogError, DisableStderr: true})
	// The command makes its context with signal.NotifyContext. The first use of
	// the os/signal package starts a goroutine that waits in a system call for
	// signals, and one started inside a synctest bubble is never idle, so the
	// bubble would never move its clock. It is started here, outside any bubble,
	// and the interrupt is given back to the system.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	signal.Stop(interrupts)
	os.Exit(m.Run())
}

func TestTheFlagsAreChecked(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		refused string
		check   func(settings) bool
	}{
		{"nothing given", nil, "", func(s settings) bool {
			return s.mode == "offline" && len(s.flows) == 2 && s.delay == 200*time.Millisecond && s.model == "gpt-6-luna" && s.reps == 1 && s.limit == 0
		}},
		{"the fused flow alone", []string{"-flow", "fused"}, "", func(s settings) bool { return len(s.flows) == 1 && s.flows[0].name == "fused" }},
		{"the one-at-a-time flow alone", []string{"-flow", "oneatatime"}, "", func(s settings) bool { return len(s.flows) == 1 && s.flows[0].name == "oneatatime" }},
		{"a delay", []string{"-delay", "50ms"}, "", func(s settings) bool { return s.delay == 50*time.Millisecond }},
		{"the shortest delay offline", []string{"-delay", "5ms"}, "", func(s settings) bool { return s.delay == 5*time.Millisecond }},
		{"a delay a little under the shortest, offline", []string{"-delay", "4999us"}, "-delay", nil},
		{"a delay of a millisecond, offline", []string{"-delay", "1ms"}, "-delay", nil},
		{"no delay at all, offline", []string{"-delay", "0"}, "scheduler", nil},
		{"no delay at all is no concern of the live mode", []string{"-mode", "live", "-delay", "0"}, "", func(s settings) bool { return s.mode == "live" }},
		{"live, with a model, a limit and repeats", []string{"-mode", "live", "-model", "gpt-6-sol", "-n", "3", "-reps", "2"}, "",
			func(s settings) bool {
				return s.mode == "live" && s.model == "gpt-6-sol" && s.limit == 3 && s.reps == 2
			}},
		{"a file to write to", []string{"-out", "runs.jsonl"}, "", func(s settings) bool { return s.out == "runs.jsonl" }},
		{"a mode that does not exist", []string{"-mode", "dry"}, "-mode", nil},
		{"a flow that does not exist", []string{"-flow", "sideways"}, "-flow", nil},
		{"the fast flow", []string{"-flow", "fast"}, "", func(s settings) bool { return len(s.flows) == 1 && s.flows[0].name == "fast" }},
		{"every flow", []string{"-flow", "all"}, "", func(s settings) bool { return len(s.flows) == 3 }},
		{"a negative delay", []string{"-delay", "-1s"}, "-delay", nil},
		{"no repeats", []string{"-reps", "0"}, "-reps", nil},
		{"a negative limit", []string{"-n", "-1"}, "-n", nil},
		{"live with no ceiling", []string{"-mode", "live", "-ceiling", "0"}, "-ceiling", nil},
		{"live with no model", []string{"-mode", "live", "-model", " "}, "-model", nil},
		{"a flag that does not exist", []string{"-key", "sk-something"}, "not defined", nil},
		{"offline with no ceiling is not live's concern", []string{"-ceiling", "0"}, "", func(s settings) bool { return s.mode == "offline" }},
		{"the effort, the triage effort, the tier and the hedge", []string{"-mode", "live", "-effort", "low", "-triage-effort", "high", "-tier", "priority", "-hedge", "2s"}, "",
			func(s settings) bool {
				return s.effort == "low" && s.triage == "high" && s.tier == "priority" && s.hedge == 2*time.Second
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			asked, err := parse(tc.args, &stderr)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("err = %v, want one about %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(asked) {
				t.Errorf("settings: %+v", asked)
			}
		})
	}

	t.Run("a delay below the shortest says why", func(t *testing.T) {
		_, err := parse([]string{"-delay", "1ms"}, &bytes.Buffer{})
		for _, why := range []string{"at least 5ms", "overlap", "count"} {
			if err == nil || !strings.Contains(err.Error(), why) {
				t.Errorf("err = %v, want one that says %q", err, why)
			}
		}
	})
	t.Run("the help says the library's log is not filtered", func(t *testing.T) {
		var stderr bytes.Buffer
		if _, err := parse([]string{"-h"}, &stderr); !errors.Is(err, flagHelp(t)) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(stderr.String(), "not filtered") {
			t.Errorf("the help for -logs does not say that the library's log is not filtered:\n%s", stderr.String())
		}
	})
}

// flagHelp is the error the flag package returns for -h.
func flagHelp(t *testing.T) error {
	t.Helper()
	_, err := parse([]string{"-h"}, &bytes.Buffer{})
	return err
}

// The columns that are a time, which are the only part of the offline report
// that may differ between two runs.
var (
	rowTimes     = regexp.MustCompile(`(?m)^(M-\d+ +\S+ +.{18} +\d+ +\d+) +\d+ +\d+ +(\d+)$`)
	summaryTimes = regexp.MustCompile(`(?m)^((?:fused|oneatatime|fast) +\d+ +\d+ +\d+ +\d+ +\d+) +\d+ +\d+ +([\d.]+)$`)
)

func withoutTimes(report string) string {
	report = rowTimes.ReplaceAllString(report, "$1 $2")
	return summaryTimes.ReplaceAllString(report, "$1 $2")
}

func TestTheOfflineReportIsTheSameOnEveryRunApartFromTheTimes(t *testing.T) {
	// The runs are on a synctest bubble's clock, so the waits are counted from
	// calls that overlap exactly, at any delay, and the times are exact too: a
	// call takes its delay and nothing else takes any time. The comparison
	// still sets the times aside, as it is the report's own claim, and holds the
	// times to what the delay makes of them in the rows checked below.
	var first, second, stderr bytes.Buffer
	synctest.Test(t, func(t *testing.T) {
		args := []string{"-delay", "40ms", "-n", "4"}
		if code := run(args, &first, &stderr); code != 0 {
			t.Fatalf("exit %d: %s%s", code, first.String(), stderr.String())
		}
		if code := run(args, &second, &stderr); code != 0 {
			t.Fatalf("exit %d: %s%s", code, second.String(), stderr.String())
		}
	})
	if stderr.Len() != 0 {
		t.Errorf("written to standard error: %s", stderr.String())
	}

	stripped := withoutTimes(first.String())
	if stripped == first.String() {
		t.Fatalf("the times were not found in the report, so the comparison below would compare nothing:\n%s", first.String())
	}
	if other := withoutTimes(second.String()); stripped != other {
		t.Errorf("two runs differ in more than their times:\n%s\n%s", stripped, other)
	}
	if first.String() != second.String() {
		t.Errorf("two runs on the same clock differ in their times:\n%s\n%s", first.String(), second.String())
	}
	// The wall time of a row is its waits times the delay, and the time outside
	// the provider is none.
	for _, line := range []string{
		"M-01  fused       discarded              1         1        40          0     1",
		"M-02  fused       answered               9         4       160          0     4",
		"M-02  oneatatime  answered              15         1       600          0    15",
		"M-03  fused       answered              11         4       240          0     6",
		"M-04  fused       answered              10         4       200          0     5",
	} {
		if !strings.Contains(first.String(), line) {
			t.Errorf("the report has no line %q:\n%s", line, first.String())
		}
	}

	// The first four messages are spam, a plain one, one translated in and
	// back, and one redacted: 1+9+11+10 calls in 1+4+6+5 waits fused, and
	// 1+15+17+16 of each one at a time.
	summary := func(name string, messages, matched, errors, calls, waits int, meanWaits string) string {
		return fmt.Sprintf("%-11s %8d %7d %6d %6d %6d", name, messages, matched, errors, calls, waits) + " " + meanWaits
	}
	for _, line := range []string{
		"M-01  fused       discarded              1         1 1",
		"M-02  fused       answered               9         4 4",
		"M-02  oneatatime  answered              15         1 15",
		"M-03  fused       answered              11         4 6",
		"M-04  oneatatime  answered              16         1 16",
		summary("fused", 4, 4, 0, 31, 16, "4.00"),
		summary("oneatatime", 4, 4, 0, 49, 49, "12.25"),
	} {
		if !strings.Contains(stripped, line) {
			t.Errorf("the report has no line %q:\n%s", line, stripped)
		}
	}
	if !strings.Contains(stripped, "errors") {
		t.Errorf("the summary has no errors column:\n%s", stripped)
	}
}

func TestEachRunIsAppendedToTheOutFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "runs.jsonl")
	args := []string{"-delay", "5ms", "-n", "2", "-flow", "fused", "-out", out}
	var stdout, stderr bytes.Buffer
	// On a synctest bubble's clock the times of each record are exact.
	synctest.Test(t, func(t *testing.T) {
		for i := 0; i < 2; i++ {
			if code := run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
		}
	})

	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if len(lines) != 4 {
		t.Fatalf("%d lines after two runs of two messages, want 4", len(lines))
	}
	for i, line := range lines {
		var made record
		if err := json.Unmarshal([]byte(line), &made); err != nil {
			t.Fatalf("line %d is not a record: %v", i+1, err)
		}
		wantID := []string{"M-01", "M-02"}[i%2]
		if made.ID != wantID || made.Mode != "offline" || made.Flow != "fused" || !made.Match || made.Rep != 1 {
			t.Errorf("line %d: %+v", i+1, made)
		}
		if len(made.Operations) != made.Calls || made.Calls == 0 {
			t.Errorf("line %d lists %d operations for %d calls", i+1, len(made.Operations), made.Calls)
		}
		// Nothing but the provider takes any of the bubble's time, so the run is
		// all provider time.
		if made.WallMS != made.ProviderMS || made.OutsideMS != 0 || made.ProviderShare != 1 {
			t.Errorf("line %d: wall %v ms, provider %v ms, outside %v ms, share %v", i+1, made.WallMS, made.ProviderMS, made.OutsideMS, made.ProviderShare)
		}
		if made.CostUSD != nil || made.InputTokens != 0 {
			t.Errorf("line %d reports a cost or tokens, and the simulator has neither", i+1)
		}
		if _, err := time.Parse(time.RFC3339, made.Time); err != nil {
			t.Errorf("line %d: time %q", i+1, made.Time)
		}
		// The run's parameters, and the critical path beside the count of waits.
		if made.DelayMS != 5 || made.Effort != "" || made.Tier != "" || made.HedgeMS != 0 {
			t.Errorf("line %d: delay %v ms, effort %q, tier %q, hedge %v", i+1, made.DelayMS, made.Effort, made.Tier, made.HedgeMS)
		}
		if made.CriticalPathMS != 5*float64(made.Waits) || made.CriticalPathMS != made.WallMS {
			t.Errorf("line %d: %d waits of 5 ms, a critical path of %v ms, a wall time of %v ms", i+1, made.Waits, made.CriticalPathMS, made.WallMS)
		}
		for _, one := range made.Operations {
			if one.StartMS < 0 || one.StartMS+one.MS > made.WallMS+1e-6 {
				t.Errorf("line %d: a %s call from %v ms for %v ms is outside a run of %v ms", i+1, one.Operation, one.StartMS, one.MS, made.WallMS)
			}
			if one.Said == "" {
				t.Errorf("line %d: a %s call said nothing", i+1, one.Operation)
			}
		}
	}
}

// A report generator reads these names, and they are not to be renamed: only
// added to.
func TestTheRecordKeepsTheFieldNamesAReportGeneratorReads(t *testing.T) {
	spent := 0.25
	line, err := json.Marshal(record{
		ID: "M-02", Kind: "a kind", Status: "answered", Match: true, Mismatches: []string{"team"},
		WallMS: 1, Calls: 2, Waits: 1, CostUSD: &spent, ReasoningTokens: 3,
		Operations: []call{{Operation: "assess", StartMS: 1, MS: 2, Said: "spam: no", InputTokens: 4, OutputTokens: 5, ReasoningTokens: 6, prompt: "kept apart", reply: "kept apart"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(line, &generic); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id", "kind", "status", "match", "mismatches", "wall_ms", "calls", "waits", "cost_usd", "reasoning_tokens", "operations", "critical_path_ms"} {
		if _, found := generic[name]; !found {
			t.Errorf("the record has no %q: %s", name, line)
		}
	}
	operation := generic["operations"].([]any)[0].(map[string]any)
	for _, name := range []string{"operation", "start_ms", "ms", "said", "input_tokens", "output_tokens", "reasoning_tokens"} {
		if _, found := operation[name]; !found {
			t.Errorf("an operation has no %q: %s", name, line)
		}
	}
	if strings.Contains(string(line), "kept apart") {
		t.Errorf("the request and reply a call keeps for later are written out: %s", line)
	}
}

func TestLiveModeWithNoKeyDoesNothing(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	out := filepath.Join(t.TempDir(), "runs.jsonl")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-mode", "live", "-n", "1", "-out", out}, &stdout, &stderr); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("a report was started with no key: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "OPENAI_API_KEY") {
		t.Errorf("the refusal does not say where the key is read from: %s", stderr.String())
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the -out file was created by a run that could not start: %v", err)
	}
}

// quoting is a provider that fails every summary with an error that quotes
// the key, as a provider's own message about a bad key does.
type quoting struct {
	schemaflux.Provider
	key string
}

func (q quoting) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	if sim.Operation(req) == sim.OpSummarize {
		return schemaflux.CompletionResponse{}, errors.New("incorrect API key provided: " + q.key)
	}
	return q.Provider.Complete(ctx, req)
}

// The live report, with the simulator where the model would be: the rows, the
// summary and the records are made the way a live run makes them, and nothing
// is sent anywhere.
func TestTheLiveReportThroughAProviderThatIsNotAModel(t *testing.T) {
	const key = "sk-test-this-is-not-a-key"
	asked, err := parse([]string{"-mode", "live", "-n", "3"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	cases := sim.Cases()[:3]

	t.Run("every run matches its label", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var stdout, sink bytes.Buffer
			failed, err := liveThrough(context.Background(), asked, cases, sim.New(5*time.Millisecond), nil, key, &stdout, &sink)
			if err != nil || failed {
				t.Fatalf("failed: %t, err: %v\n%s", failed, err, stdout.String())
			}
			lines := strings.Split(strings.TrimSpace(sink.String()), "\n")
			if len(lines) != 6 {
				t.Fatalf("%d records for three messages through two flows", len(lines))
			}
			wantCalls := map[string]int{"M-01 fused": 1, "M-01 oneatatime": 1, "M-02 fused": 9, "M-02 oneatatime": 15, "M-03 fused": 11, "M-03 oneatatime": 17}
			for _, line := range lines {
				var made record
				if err := json.Unmarshal([]byte(line), &made); err != nil {
					t.Fatal(err)
				}
				if !made.Match || made.Error != "" || made.Mode != "live" || made.Model != "gpt-6-luna" {
					t.Errorf("%s %s: %+v", made.ID, made.Flow, made)
				}
				if made.Calls != wantCalls[made.ID+" "+made.Flow] || len(made.Operations) != made.Calls {
					t.Errorf("%s %s: %d calls, %d operations listed, want %d", made.ID, made.Flow, made.Calls, len(made.Operations), wantCalls[made.ID+" "+made.Flow])
				}
				if made.CostUSD == nil {
					t.Errorf("%s %s: no cost from the client's ledger", made.ID, made.Flow)
				}
				// On a synctest bubble's clock a run is its waits and nothing
				// else, and each wait is one call's time.
				if made.CriticalPathMS != 5*float64(made.Waits) || made.CriticalPathMS != made.WallMS {
					t.Errorf("%s %s: a critical path of %v ms and a run of %v ms, for %d waits of 5 ms", made.ID, made.Flow, made.CriticalPathMS, made.WallMS, made.Waits)
				}
			}
			for _, heading := range []string{"model ms", "model%", "in tok", "cost USD", "mean outside ms", "errors"} {
				if !strings.Contains(stdout.String(), heading) {
					t.Errorf("the report has no %q column:\n%s", heading, stdout.String())
				}
			}
			if strings.Contains(stdout.String()+sink.String(), key) {
				t.Error("the key is in the report")
			}
		})
	})

	t.Run("the header says what the run was run with, and a note says what the effort is when none is named", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			header := func(args ...string) (string, record) {
				asked, err := parse(append([]string{"-mode", "live", "-n", "1", "-flow", "fused"}, args...), &bytes.Buffer{})
				if err != nil {
					t.Fatal(err)
				}
				var stdout, sink bytes.Buffer
				if _, err := liveThrough(context.Background(), asked, cases[:1], sim.New(5*time.Millisecond), nil, key, &stdout, &sink); err != nil {
					t.Fatal(err)
				}
				var made record
				if err := json.Unmarshal([]byte(strings.TrimSpace(sink.String())), &made); err != nil {
					t.Fatal(err)
				}
				return strings.SplitN(stdout.String(), "\n\n", 2)[0], made
			}

			plain, made := header()
			for _, absent := range []string{"triage effort", "tier", "hedge"} {
				if strings.Contains(plain, absent) {
					t.Errorf("the header names a %s that was not set:\n%s", absent, plain)
				}
			}
			if !strings.Contains(plain, "effort the library's") || !strings.Contains(plain, "note: with no -effort") {
				t.Errorf("the header does not say that the library chose the effort:\n%s", plain)
			}
			if made.Effort != "" || made.TriageEffort != "" || made.Tier != "" || made.HedgeMS != 0 {
				t.Errorf("a record names parameters that were not set: %+v", made)
			}

			named, made := header("-effort", "low", "-triage-effort", "high", "-tier", "priority", "-hedge", "2s")
			for _, present := range []string{"effort low", "triage effort high", "tier priority", "hedge after 2s"} {
				if !strings.Contains(named, present) {
					t.Errorf("the header does not say %q:\n%s", present, named)
				}
			}
			if strings.Contains(named, "note: with no -effort") {
				t.Errorf("the note about an effort nobody named is printed beside one that was named:\n%s", named)
			}
			if made.Effort != "low" || made.TriageEffort != "high" || made.Tier != "priority" || made.HedgeMS != 2000 {
				t.Errorf("the record does not carry the run's parameters: %+v", made)
			}
		})
	})

	t.Run("a run that fails is reported as one, without the key", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var stdout, sink bytes.Buffer
			failing := quoting{Provider: sim.New(5 * time.Millisecond), key: key}
			failed, err := liveThrough(context.Background(), asked, cases, failing, nil, key, &stdout, &sink)
			if err != nil {
				t.Fatal(err)
			}
			if !failed {
				t.Error("a run failed and the report says none did")
			}
			if strings.Contains(stdout.String()+sink.String(), key) {
				t.Errorf("the key is in the report:\n%s", stdout.String())
			}
			if !strings.Contains(stdout.String(), "[key]") || !strings.Contains(sink.String(), "[key]") {
				t.Errorf("the error was not reported:\n%s", stdout.String())
			}
			// Spam is discarded before anything is summarised, so its two runs
			// still succeed; the other four fail.
			if got := strings.Count(sink.String(), `"status":"error"`); got != 4 {
				t.Errorf("%d runs reported as errors, want 4", got)
			}
		})
	})

	// The ceiling is what stands between a mistake in this command and a
	// bill. With a provider that reports tokens and a model the library has a
	// price for, a run stops at the first call after the ceiling is reached.
	t.Run("a run stops at its ceiling", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			priced := func(ceiling float64) (record, float64) {
				provider := measure(reporting{sim.New(time.Millisecond)})
				client := schemaflux.NewClient("").WithProviderInstance(provider).WithBudget(ceiling)
				support := desk.New(sim.OpenTickets())
				support.Options.Model = "gpt-6-luna"
				_, made := handle(context.Background(), flows[1], cases[1], provider, client, support)
				spent, _ := client.Spent()
				return made, spent
			}

			under, spent := priced(1)
			if under.Error != "" || under.Calls != 15 || spent <= 0 {
				t.Fatalf("under its ceiling the run made %d calls and spent %v: %s", under.Calls, spent, under.Error)
			}
			// A ceiling of what the first call costs: the first call is let
			// through, and it is the last.
			over, _ := priced(spent / 15)
			if over.Error == "" || over.Status != "error" {
				t.Fatalf("a run past its ceiling was not stopped: %d calls", over.Calls)
			}
			if over.Calls >= under.Calls {
				t.Errorf("a run past its ceiling made %d calls, as many as one under it", over.Calls)
			}
		})
	})
}

func TestMeteredCountsWhatPassesThroughIt(t *testing.T) {
	summary := schemaflux.CompletionRequest{SystemPrompt: "You are a text summarization expert.", UserPrompt: "Summarize this text:\n[ref M-02]"}
	unknown := schemaflux.CompletionRequest{SystemPrompt: "You are a poet.", UserPrompt: "[ref M-02]"}

	// On a synctest bubble's clock each call of 5 ms takes exactly that.
	synctest.Test(t, func(t *testing.T) {
		provider := measure(reporting{sim.New(5 * time.Millisecond)})
		if _, err := provider.Complete(context.Background(), summary); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Complete(context.Background(), summary); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Complete(context.Background(), unknown); err == nil {
			t.Fatal("the simulator answered a request from no operation")
		}

		// What it kept while the calls were made is strings it already held: the
		// reading of them waits for the end of the run.
		for _, kept := range provider.calls {
			if kept.Said != "" {
				t.Errorf("a call's answer was read while the run was going: %q", kept.Said)
			}
		}
		if provider.calls[0].prompt != summary.UserPrompt || provider.calls[0].reply == "" || provider.calls[2].reply != "" {
			t.Errorf("what a call keeps: %+v", provider.calls)
		}

		calls, usage := provider.made()
		if len(calls) != 3 || provider.Count() != 3 || provider.Waits() != 3 || provider.MaxInFlight() != 1 {
			t.Fatalf("%d calls listed, %d counted, %d waits, %d in flight", len(calls), provider.Count(), provider.Waits(), provider.MaxInFlight())
		}
		if calls[0].Operation != sim.OpSummarize || calls[0].Failed || calls[0].InputTokens != 100 || calls[0].OutputTokens != 20 || calls[0].MS != 5 {
			t.Errorf("first call: %+v", calls[0])
		}
		if calls[0].Said == "" || calls[2].Said != "" {
			t.Errorf("what the calls said: %q and %q, want the first's text and nothing for the failed one", calls[0].Said, calls[2].Said)
		}
		if calls[2].Operation != "other" || !calls[2].Failed || calls[2].InputTokens != 0 {
			t.Errorf("failed call: %+v", calls[2])
		}
		if usage.PromptTokens != 200 || usage.CompletionTokens != 40 || usage.CachedTokens != 60 || usage.ReasoningTokens != 10 {
			t.Errorf("usage: %+v", usage)
		}
		if provider.Busy() != 15*time.Millisecond {
			t.Errorf("busy for %v over three calls of 5ms", provider.Busy())
		}
		if path := provider.CriticalPath(); path != 15*time.Millisecond || path != provider.Busy() {
			t.Errorf("a critical path of %v over three calls one after another, busy for %v", path, provider.Busy())
		}
		if provider.Name() != "local" {
			t.Errorf("the provider's own name is hidden: %q", provider.Name())
		}
	})

	t.Run("calls start from the instant the run was told it began", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			provider := measure(sim.New(time.Millisecond))
			provider.start(time.Now().Add(-time.Second))
			if _, err := provider.Complete(context.Background(), summary); err != nil {
				t.Fatal(err)
			}
			if calls, _ := provider.made(); calls[0].StartMS != 1000 {
				t.Errorf("a call that began a second after the run began is at %v ms, want 1000", calls[0].StartMS)
			}
		})
	})
}

// reporting adds token counts to a provider's answers, as a real one does.
type reporting struct{ schemaflux.Provider }

func (r reporting) Complete(ctx context.Context, req schemaflux.CompletionRequest) (schemaflux.CompletionResponse, error) {
	resp, err := r.Provider.Complete(ctx, req)
	if err == nil {
		resp.Usage = schemaflux.TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CachedTokens: 30, ReasoningTokens: 5}
	}
	return resp, err
}

func TestWhatALiveRunIsHeldTo(t *testing.T) {
	want := desk.Outcome{
		MessageID: "M-02", Status: desk.StatusAnswered, Language: "English",
		Team: "billing", Intent: "asking a question", Urgency: 3, TicketID: "T-M-02", New: true,
		Reply: "the label's reply", Handoff: "the label's note", NextAction: "close the ticket",
	}
	change := func(edit func(*desk.Outcome)) desk.Outcome {
		got := want
		edit(&got)
		return got
	}
	cases := []struct {
		name string
		got  desk.Outcome
		want string
	}{
		{"the same outcome", want, ""},
		{"a reply in other words", change(func(o *desk.Outcome) { o.Reply = "another reply" }), ""},
		{"another hand-off note and next action", change(func(o *desk.Outcome) { o.Handoff, o.NextAction, o.WillWriteAgain = "x", "follow up tomorrow", true }), ""},
		{"a redraft the label does not have", change(func(o *desk.Outcome) { o.Redrafted = true }), ""},
		{"other details", change(func(o *desk.Outcome) { o.Details.OrderID = "Z-1" }), ""},
		{"urgency one higher", change(func(o *desk.Outcome) { o.Urgency = 4 }), ""},
		{"urgency one lower", change(func(o *desk.Outcome) { o.Urgency = 2 }), ""},
		{"urgency two higher", change(func(o *desk.Outcome) { o.Urgency = 5 }), "urgency"},
		{"urgency two lower", change(func(o *desk.Outcome) { o.Urgency = 1 }), "urgency"},
		{"another team", change(func(o *desk.Outcome) { o.Team = "account" }), "team"},
		{"another intent", change(func(o *desk.Outcome) { o.Intent = "making a complaint" }), "intent"},
		{"another language", change(func(o *desk.Outcome) { o.Language = "Spanish" }), "language"},
		{"attached to an open ticket", change(func(o *desk.Outcome) { o.TicketID, o.New = "T-1001", false }), "ticket"},
		{"held for a person", change(func(o *desk.Outcome) { o.Status = desk.StatusHeld }), "status"},
		{"the team and the status both", change(func(o *desk.Outcome) { o.Status, o.Team = desk.StatusAsked, "shipping" }), "status,team"},
		{"discarded, which leaves nothing else to compare", desk.Outcome{MessageID: "M-02", Status: desk.StatusDiscarded}, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(decisionDifferences(tc.got, want), ","); got != tc.want {
				t.Errorf("differences: %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("spam that was worked differs in its status alone", func(t *testing.T) {
		spam := desk.Outcome{MessageID: "M-01", Status: desk.StatusDiscarded}
		if got := strings.Join(decisionDifferences(want, spam), ","); got != "status" {
			t.Errorf("differences: %q", got)
		}
	})
}

func TestEveryFieldOfTheOutcomeCountsOffline(t *testing.T) {
	want := desk.Outcome{
		MessageID: "M-02", Status: desk.StatusAnswered, Language: "English",
		Team: "billing", Intent: "asking a question", Urgency: 3, TicketID: "T-M-02", New: true,
		Details: desk.Details{OrderID: "A-1"}, Reply: "reply", Handoff: "note", NextAction: "close the ticket",
	}
	cases := []struct {
		name  string
		edit  func(*desk.Outcome)
		found string
	}{
		{"the same", func(*desk.Outcome) {}, ""},
		{"the status", func(o *desk.Outcome) { o.Status = desk.StatusHeld }, "status"},
		{"the language", func(o *desk.Outcome) { o.Language = "French" }, "language"},
		{"the team", func(o *desk.Outcome) { o.Team = "account" }, "team"},
		{"the intent", func(o *desk.Outcome) { o.Intent = "giving feedback" }, "intent"},
		{"the urgency by one", func(o *desk.Outcome) { o.Urgency = 4 }, "urgency"},
		{"the ticket's ID", func(o *desk.Outcome) { o.TicketID = "T-1001" }, "ticket"},
		{"whether the ticket is new", func(o *desk.Outcome) { o.New = false }, "ticket"},
		{"the details", func(o *desk.Outcome) { o.Details.Amount = "$1" }, "details"},
		{"the reply", func(o *desk.Outcome) { o.Reply = "other" }, "reply"},
		{"the redraft", func(o *desk.Outcome) { o.Redrafted = true }, "redrafted"},
		{"the hand-off note", func(o *desk.Outcome) { o.Handoff = "other" }, "handoff"},
		{"the next action", func(o *desk.Outcome) { o.NextAction = "wait for the customer" }, "next action"},
		{"the prediction", func(o *desk.Outcome) { o.WillWriteAgain = true }, "writes again"},
		{"the ID of the message, which no run changes", func(o *desk.Outcome) { o.MessageID = "M-03" }, ""},
		{"two fields", func(o *desk.Outcome) { o.Team, o.Reply = "account", "other" }, "team,reply"},
		{"every field", func(o *desk.Outcome) {
			*o = desk.Outcome{MessageID: "x"}
		}, "status,language,team,intent,urgency,ticket,details,reply,handoff,next action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := want
			tc.edit(&got)
			if found := strings.Join(differences(got, want), ","); found != tc.found {
				t.Errorf("differences: %q, want %q", found, tc.found)
			}
		})
	}
}

func TestTheSummaryOfAFlow(t *testing.T) {
	cost := func(usd float64) *float64 { return &usd }
	done := func(flow string, wall, provider float64, calls, waits int) record {
		return record{Flow: flow, Match: true, Calls: calls, Waits: waits, WallMS: wall, ProviderMS: provider, OutsideMS: wall - provider}
	}
	failed := func(flow string, wall float64, calls int) record {
		return record{Flow: flow, Error: "it failed", Calls: calls, Waits: calls, WallMS: wall, ProviderMS: wall}
	}

	cases := []struct {
		name  string
		all   []record
		flow  string
		check func(totals) string
	}{
		{"no runs", nil, "fused", func(s totals) string {
			if s != (totals{costComplete: true}) {
				return fmt.Sprintf("%+v", s)
			}
			return ""
		}},
		{"a flow with no runs among others", []record{done("fused", 10, 5, 1, 1)}, "fast", func(s totals) string {
			if s.runs != 0 || s.meanWall != 0 || s.medianWall != 0 {
				return fmt.Sprintf("%+v", s)
			}
			return ""
		}},
		{"one run", []record{done("fused", 100, 90, 9, 4)}, "fused", func(s totals) string {
			if s.runs != 1 || s.completed != 1 || s.matched != 1 || s.errors != 0 || s.calls != 9 || s.waits != 4 ||
				s.meanWall != 100 || s.medianWall != 100 || s.meanProvider != 90 || s.meanOutside != 10 || s.meanWaits != 4 || s.share != 0.9 {
				return fmt.Sprintf("%+v", s)
			}
			return ""
		}},
		{"the means are of the runs that completed", []record{done("fused", 100, 80, 9, 4), done("fused", 300, 100, 11, 6), failed("fused", 10, 1)}, "fused", func(s totals) string {
			if s.runs != 3 || s.completed != 2 || s.errors != 1 || s.meanWall != 200 || s.meanProvider != 90 || s.meanOutside != 110 || s.meanWaits != 5 {
				return fmt.Sprintf("%+v", s)
			}
			return ""
		}},
		{"a run that failed is not in the median", []record{done("fused", 100, 1, 1, 1), done("fused", 300, 1, 1, 1), failed("fused", 1, 1)}, "fused", func(s totals) string {
			if s.medianWall != 200 {
				return fmt.Sprintf("median %v", s.medianWall)
			}
			return ""
		}},
		{"calls and waits are those of the runs that completed", []record{done("fused", 1, 1, 9, 4), failed("fused", 1, 3)}, "fused", func(s totals) string {
			if s.calls != 9 || s.waits != 4 {
				return fmt.Sprintf("calls %d, waits %d", s.calls, s.waits)
			}
			return ""
		}},
		{"every run failed", []record{failed("fused", 10, 1), failed("fused", 20, 2)}, "fused", func(s totals) string {
			if s.runs != 2 || s.errors != 2 || s.completed != 0 || s.matched != 0 || s.meanWall != 0 || s.medianWall != 0 || s.meanWaits != 0 || s.share != 0 {
				return fmt.Sprintf("%+v", s)
			}
			return ""
		}},
		{"the share is total provider time over total wall time, and not a mean of ratios",
			[]record{done("fused", 100, 90, 1, 1), done("fused", 300, 150, 1, 1)}, "fused", func(s totals) string {
				// 240 of 400, where the mean of 90% and 50% is 70%.
				if s.share != 0.6 {
					return fmt.Sprintf("share %v", s.share)
				}
				return ""
			}},
		{"the share leaves out the runs that failed", []record{done("fused", 100, 50, 1, 1), failed("fused", 1000, 1)}, "fused", func(s totals) string {
			if s.share != 0.5 {
				return fmt.Sprintf("share %v", s.share)
			}
			return ""
		}},
		{"the median of an even number", []record{done("fused", 10, 1, 1, 1), done("fused", 20, 1, 1, 1), done("fused", 40, 1, 1, 1), done("fused", 100, 1, 1, 1)}, "fused", func(s totals) string {
			if s.medianWall != 30 {
				return fmt.Sprintf("median %v", s.medianWall)
			}
			return ""
		}},
		{"the median of an odd number", []record{done("fused", 50, 1, 1, 1), done("fused", 10, 1, 1, 1), done("fused", 20, 1, 1, 1)}, "fused", func(s totals) string {
			if s.medianWall != 20 {
				return fmt.Sprintf("median %v", s.medianWall)
			}
			return ""
		}},
		{"a run that did not match is counted and not matched", []record{done("fused", 1, 1, 1, 1), {Flow: "fused", Calls: 1, Waits: 1, WallMS: 1}}, "fused", func(s totals) string {
			if s.runs != 2 || s.completed != 2 || s.matched != 1 || s.errors != 0 {
				return fmt.Sprintf("%+v", s)
			}
			return ""
		}},
		{"tokens and cost are all the runs', since a run that failed spent them",
			[]record{
				{Flow: "fused", Match: true, WallMS: 1, InputTokens: 10, OutputTokens: 1, CostUSD: cost(0.25), CostComplete: true},
				{Flow: "fused", Error: "x", InputTokens: 20, OutputTokens: 2, CostUSD: cost(0.5), CostComplete: true},
			}, "fused", func(s totals) string {
				if s.input != 30 || s.output != 3 || s.cost != 0.75 || !s.costComplete {
					return fmt.Sprintf("%+v", s)
				}
				return ""
			}},
		{"a cost that is a floor makes the total one", []record{{Flow: "fused", CostUSD: cost(1), CostComplete: true}, {Flow: "fused", CostUSD: cost(1)}}, "fused", func(s totals) string {
			if s.cost != 2 || s.costComplete {
				return fmt.Sprintf("cost %v complete %t", s.cost, s.costComplete)
			}
			return ""
		}},
		{"a record with no cost adds none, and is not a floor unless it says so", []record{{Flow: "fused", CostComplete: true}, {Flow: "fused", CostUSD: cost(1), CostComplete: true}}, "fused", func(s totals) string {
			if s.cost != 1 || !s.costComplete {
				return fmt.Sprintf("cost %v complete %t", s.cost, s.costComplete)
			}
			return ""
		}},
		{"other flows are left out", []record{done("fused", 10, 1, 1, 1), done("oneatatime", 1000, 1, 18, 18), done("fast", 500, 1, 6, 3)}, "oneatatime", func(s totals) string {
			if s.runs != 1 || s.meanWall != 1000 || s.calls != 18 {
				return fmt.Sprintf("%+v", s)
			}
			return ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if problem := tc.check(summarise(tc.all, tc.flow)); problem != "" {
				t.Errorf("summary: %s", problem)
			}
		})
	}
}

func TestDollarsAreWrittenAsAFloorWhenTheyAreOne(t *testing.T) {
	cases := []struct {
		usd      float64
		complete bool
		want     string
	}{
		{0.5, true, "0.500000"},
		{0.5, false, ">=0.500000"},
		{0, true, "0.000000"},
		{0, false, ">=0.000000"},
		{12.3456789, true, "12.345679"},
		{0.0000004, true, "0.000000"},
		{0.0000006, true, "0.000001"},
		{1234567.1, true, "1234567.100000"},
		{0.05, false, ">=0.050000"},
		{-1, true, "-1.000000"},
		{1, true, "1.000000"},
		{0.000123, false, ">=0.000123"},
	}
	for _, tc := range cases {
		if got := dollars(tc.usd, tc.complete); got != tc.want {
			t.Errorf("dollars(%v, %t) = %q, want %q", tc.usd, tc.complete, got, tc.want)
		}
	}
}

func TestANoteSaysWhyARowDidNotMatch(t *testing.T) {
	const key = "sk-test-this-is-not-a-key"
	cases := []struct {
		name string
		made record
		key  string
		want string
	}{
		{"a run that matched", record{Match: true}, "", ""},
		{"an error", record{Error: "it failed"}, "", "  error: it failed"},
		{"an error that quotes the key", record{Error: "bad key " + key}, key, "  error: bad key [key]"},
		{"an error that quotes the key, with no key to look for", record{Error: "bad key " + key}, "", "  error: bad key [key]"},
		{"a difference", record{Mismatches: []string{"team"}}, "", "  differs: team"},
		{"two differences", record{Mismatches: []string{"team", "urgency"}}, "", "  differs: team, urgency"},
		{"an error wins over differences", record{Error: "it failed", Mismatches: []string{"team"}}, "", "  error: it failed"},
		{"no differences", record{Mismatches: []string{}}, "", ""},
		{"a record with nothing in it", record{}, "", ""},
		{"an error with a masked key in it", record{Error: "bad key sk-test-********-key"}, key, "  error: bad key [key]"},
		{"a difference is not looked at for a key", record{Mismatches: []string{"reply"}}, key, "  differs: reply"},
		{"an empty error is none", record{Error: "", Match: true}, key, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := note(tc.made, tc.key); got != tc.want {
				t.Errorf("note = %q, want %q", got, tc.want)
			}
		})
	}
}

// What a provider's error can quote of a key, and what is taken out of it.
func TestTheKeyIsTakenOutOfWhatIsPrintedHoweverItIsQuoted(t *testing.T) {
	const key = "sk-proj-abcdefgh1234567890wxyz"
	cases := []struct {
		name string
		text string
		key  string
		want string
	}{
		{"the whole key", "incorrect API key provided: " + key, key, "incorrect API key provided: [key]"},
		{"the whole key twice", key + " and " + key, key, "[key] and [key]"},
		{"a masked key, as a provider shows one", "Incorrect API key provided: sk-proj-********wxyz.", key, "Incorrect API key provided: [key]"},
		{"a masked key with dots", "key sk-proj-........wxyz was refused", key, "key [key] was refused"},
		{"a key masked in the middle by asterisks", "key sk-proj-abcd****7890wxyz was refused", key, "key [key] was refused"},
		{"the first eight characters and asterisks", "key sk-proj-*** was refused", key, "key [key] was refused"},
		{"asterisks and the last four characters", "key ************wxyz was refused", key, "key [key] was refused"},
		{"a key cut short", "key sk-proj-abcdefgh1234 was cut", key, "key [key] was cut"},
		{"a key cut short at the end of the text", "the key was sk-proj-abcdefgh12", key, "the key was [key]"},
		{"a key in JSON", `{"error":{"message":"Incorrect API key provided: ` + key + `","type":"invalid_request_error"}}`, key,
			`{"error":{"message":"Incorrect API key provided: [key]","type":"invalid_request_error"}}`},
		{"a masked key in JSON", `{"error":{"message":"Incorrect API key provided: sk-proj-********wxyz."}}`, key,
			`{"error":{"message":"Incorrect API key provided: [key]"}}`},
		{"a truncated key in JSON", `{"message":"bad key sk-proj-abcdefgh1234..."}`, key, `{"message":"bad key [key]"}`},
		{"another key, which is not this one", "bad key sk-another-key-12345678", key, "bad key [key]"},
		{"a key that is not one of the library's, masked", "bad key abcd1234****wxyz", "abcd1234efghwxyz", "bad key [key]"},
		{"a key that is not one of the library's, whole", "bad key abcd1234efghwxyz", "abcd1234efghwxyz", "bad key [key]"},
		{"no key to look for, and nothing that looks like one", "the model is unavailable", "", "the model is unavailable"},
		{"no key to look for, and one that looks like one", "bad key sk-another-key-12345678", "", "bad key [key]"},
		{"too short to be a key", "the sk-abc was fine", key, "the sk-abc was fine"},
		{"exactly eight characters after sk-", "key sk-12345678 ok", key, "key [key] ok"},
		{"seven characters after sk-", "key sk-1234567 ok", key, "key sk-1234567 ok"},
		{"a key with characters a regular expression reads", "bad key sk+(a.b)*", "sk+(a.b)*", "bad key [key]"},
		{"text with no key in it", "ordinary text with *** and ... in it", key, "ordinary text with *** and ... in it"},
		{"asterisks that are not next to the key", "a ** b **** c", key, "a ** b **** c"},
		{"an empty text", "", key, ""},
		{"a short key", "the key sk-1 was refused", "sk-1", "the key [key] was refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withoutKey(tc.text, tc.key); got != tc.want {
				t.Errorf("withoutKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheMiddleAndTheNinetiethOfSomeFigures(t *testing.T) {
	cases := []struct {
		name    string
		figures []float64
		middle  float64
		ninth   float64
	}{
		{"none", nil, 0, 0},
		{"one", []float64{7}, 7, 7},
		{"two", []float64{10, 4}, 7, 10},
		{"three, out of order", []float64{9, 1, 5}, 5, 9},
		{"four", []float64{1, 2, 3, 4}, 2.5, 4},
		{"five", []float64{5, 4, 3, 2, 1}, 3, 5},
		{"nine", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9}, 5, 9},
		{"ten, where the ninetieth is the ninth", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 5.5, 9},
		{"eleven, where it is the tenth", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 6, 10},
		{"twenty, where it is the eighteenth", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}, 10.5, 18},
		{"equal figures", []float64{4, 4, 4, 4}, 4, 4},
		{"negative figures", []float64{-3, -1, -2}, -2, -1},
		{"a figure far out", []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1000}, 1, 1},
		{"zeros", []float64{0, 0, 0}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]float64(nil), tc.figures...)
			if got := middle(tc.figures); got != tc.middle {
				t.Errorf("middle = %v, want %v", got, tc.middle)
			}
			if got := ninetieth(tc.figures); got != tc.ninth {
				t.Errorf("ninetieth = %v, want %v", got, tc.ninth)
			}
			for i := range before {
				if tc.figures[i] != before[i] {
					t.Fatalf("the figures were sorted in place: %v", tc.figures)
				}
			}
		})
	}
}

func TestTheBreakdownOfTheCallsOfAFlow(t *testing.T) {
	all := []record{
		{Flow: "fused", Operations: []call{
			{Operation: "assess", MS: 100, WaitMS: 80, ReadMS: 5, InputTokens: 10, OutputTokens: 10, ReasoningTokens: 5, SetupMS: 30},
			{Operation: "assess", MS: 300, WaitMS: 250, ReadMS: 7, InputTokens: 30, OutputTokens: 10, ReasoningTokens: 5, Reused: true},
			{Operation: "audit", MS: 50, WaitMS: 40, ReadMS: 2},
		}},
		{Flow: "oneatatime", Operations: []call{{Operation: "extract", MS: 9}}},
	}
	var out bytes.Buffer
	breakdown(&out, all, flows[:1])
	report := out.String()
	for _, want := range []string{"fused: where the calls' time went (3 calls, 1 on a new connection, 30 ms setting connections up)", "assess", "audit"} {
		if !strings.Contains(report, want) {
			t.Errorf("the breakdown does not say %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "extract") {
		t.Errorf("another flow's calls are in the breakdown:\n%s", report)
	}
	if strings.Index(report, "assess") > strings.Index(report, "audit") {
		t.Errorf("the operations are not in the order of the time they took:\n%s", report)
	}
	if total(all[0].Operations) != 450 {
		t.Errorf("the calls took %v ms between them", total(all[0].Operations))
	}

	out.Reset()
	breakdown(&out, all, flows[2:])
	if out.Len() != 0 {
		t.Errorf("a flow with no calls has a breakdown: %s", out.String())
	}
}

func TestMillisecondsAreWrittenToTheMicrosecond(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want float64
	}{
		{0, 0},
		{time.Millisecond, 1},
		{1500 * time.Microsecond, 1.5},
		{time.Second, 1000},
		{999 * time.Nanosecond, 0},
		{1999 * time.Nanosecond, 0.001},
		{-time.Millisecond, -1},
		{time.Minute, 60000},
	}
	for _, tc := range cases {
		if got := milliseconds(tc.d); got != tc.want {
			t.Errorf("milliseconds(%v) = %v, want %v", tc.d, got, tc.want)
		}
	}
}
