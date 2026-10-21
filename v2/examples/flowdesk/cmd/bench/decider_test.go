package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/examples/flowdesk/sim"
	"github.com/monstercameron/schemaflux/v2/mw"
)

// The -decider and -floor flags. Nothing here reaches a model or the network:
// offline the decision provider is the simulator's, and the live report is run
// with the simulator's pair where the model and Jev would be.

func TestTheDeciderFlagsAreChecked(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		refused string
		check   func(settings) bool
	}{
		{"no decider", nil, "", func(s settings) bool { return s.decider == "" && s.floor == 0 }},
		{"a decider, offline", []string{"-decider", "sim"}, "", func(s settings) bool { return s.decider == "sim" && s.floor == 0 }},
		{"any name plays the simulator's, offline", []string{"-decider", "jev"}, "", func(s settings) bool { return s.decider == "jev" }},
		{"a decider and a floor", []string{"-decider", "sim", "-floor", "0.7"}, "", func(s settings) bool { return s.decider == "sim" && s.floor == 0.7 }},
		{"a floor of one", []string{"-decider", "sim", "-floor", "1"}, "", func(s settings) bool { return s.floor == 1 }},
		{"the name is trimmed and in lower case", []string{"-decider", " Jev "}, "", func(s settings) bool { return s.decider == "jev" }},
		{"a floor with no decider", []string{"-floor", "0.7"}, "-decider", nil},
		{"a floor with a decider that is only space", []string{"-decider", "  ", "-floor", "0.7"}, "-decider", nil},
		{"a floor above one", []string{"-decider", "sim", "-floor", "1.5"}, "-floor", nil},
		{"a floor below zero", []string{"-decider", "sim", "-floor", "-0.1"}, "-floor", nil},
		{"a floor that is not a number", []string{"-decider", "sim", "-floor", "NaN"}, "-floor", nil},
		{"live, Jev direct", []string{"-mode", "live", "-decider", "jev", "-floor", "0.7"}, "", func(s settings) bool { return s.mode == "live" && s.decider == "jev" }},
		{"live, through OpenRouter", []string{"-mode", "live", "-decider", "jev-openrouter"}, "", func(s settings) bool { return s.decider == "jev-openrouter" }},
		{"live, through LiteLLM", []string{"-mode", "live", "-decider", "jev-litellm"}, "", func(s settings) bool { return s.decider == "jev-litellm" }},
		{"live, a decider the command has no key variable for", []string{"-mode", "live", "-decider", "sim"}, "jev, jev-openrouter or jev-litellm", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked, err := parse(tc.args, &bytes.Buffer{})
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("error %v, want one that names %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !tc.check(asked) {
				t.Errorf("the settings are %+v", asked)
			}
		})
	}
}

func TestTheOfflineReportWithTheDecisionsOnTheDecider(t *testing.T) {
	read := func(t *testing.T, path string) []record {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var all []record
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var made record
			if err := json.Unmarshal([]byte(line), &made); err != nil {
				t.Fatal(err)
			}
			all = append(all, made)
		}
		return all
	}

	t.Run("every decision is the decider's and every run matches its label", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "runs.jsonl")
		var stdout, stderr bytes.Buffer
		synctest.Test(t, func(t *testing.T) {
			if code := run([]string{"-delay", "50ms", "-flow", "every", "-decider", "sim", "-out", out}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
			}
		})
		if !strings.Contains(stdout.String(), "offline, 50ms per call, decisions to sim,") {
			t.Errorf("the first line does not say where the decisions went:\n%s", strings.SplitN(stdout.String(), "\n", 2)[0])
		}
		decisions, decided := 0, 0
		for _, made := range read(t, out) {
			if !made.Match || made.Decider != "sim" || made.Floor != 0 || made.Escalations != 0 {
				t.Errorf("%s %s: %+v", made.ID, made.Flow, made)
			}
			for _, one := range made.Operations {
				if one.Operation != sim.OpAssess {
					if one.Decider != "" {
						t.Errorf("%s %s: a %s was answered by the decider", made.ID, made.Flow, one.Operation)
					}
					continue
				}
				decisions++
				// A decision took the decider's time, a tenth of the simulator's,
				// and the trace still says what it answered.
				if one.Decider != "sim-decider" || one.MS != 5 || one.Said == "" {
					t.Errorf("%s %s: a decision answered by %q in %v ms, saying %q", made.ID, made.Flow, one.Decider, one.MS, one.Said)
				}
			}
			decided += made.DecidedCalls
		}
		if decisions == 0 || decided != decisions {
			t.Errorf("%d decisions, of which the decider answered %d", decisions, decided)
		}
		if !strings.Contains(stdout.String(), "sim answered") || !strings.Contains(stdout.String(), "and 0 it was asked first went on") {
			t.Errorf("the report does not say how the decisions were answered:\n%s", stdout.String())
		}
	})

	t.Run("a floor above what the decider reports sends every decision on", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "runs.jsonl")
		var stdout, stderr bytes.Buffer
		synctest.Test(t, func(t *testing.T) {
			if code := run([]string{"-delay", "50ms", "-flow", "fused", "-n", "4", "-decider", "sim", "-floor", "0.995", "-out", out}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
			}
		})
		if !strings.Contains(stdout.String(), "decisions to sim with a floor of 0.995") {
			t.Errorf("the first line does not name the floor:\n%s", strings.SplitN(stdout.String(), "\n", 2)[0])
		}
		for _, made := range read(t, out) {
			if !made.Match || made.DecidedCalls != 0 || made.Escalations == 0 || made.Floor != 0.995 {
				t.Errorf("%s %s: %d decided, %d sent on, match %v", made.ID, made.Flow, made.DecidedCalls, made.Escalations, made.Match)
			}
			for _, one := range made.Operations {
				// The decider's five milliseconds and then the simulator's fifty.
				if one.Operation == sim.OpAssess && (one.Escalated != "below the floor" || one.MS != 55) {
					t.Errorf("%s: a decision that took %v ms and was sent on for %q", made.ID, one.MS, one.Escalated)
				}
			}
		}
	})

	t.Run("with no decider the report and the records say nothing of one", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "runs.jsonl")
		var stdout, stderr bytes.Buffer
		synctest.Test(t, func(t *testing.T) {
			if code := run([]string{"-delay", "50ms", "-flow", "fused", "-n", "2", "-out", out}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d: %s%s", code, stdout.String(), stderr.String())
			}
		})
		raw, _ := os.ReadFile(out)
		for _, word := range []string{"decider", "decided_calls", "escalat", "floor"} {
			if strings.Contains(string(raw), word) || strings.Contains(stdout.String(), word) {
				t.Errorf("a run with no decider mentions %q", word)
			}
		}
	})
}

func TestLiveModeWithADeciderAndNoKeyForItDoesNothing(t *testing.T) {
	for name, variable := range deciderKeys {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "sk-test-this-is-not-a-key") // secret-scan: allow
			t.Setenv(variable, "")
			out := filepath.Join(t.TempDir(), "runs.jsonl")
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-mode", "live", "-n", "1", "-decider", name, "-out", out}, &stdout, &stderr); code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("a report was started with no key for the decider: %s", stdout.String())
			}
			if !strings.Contains(stderr.String(), variable) || strings.Contains(stderr.String(), "sk-test") {
				t.Errorf("the refusal does not say where the key is read from, or shows a key: %s", stderr.String())
			}
			if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the -out file was created by a run that could not start: %v", err)
			}
		})
	}
}

// The live report with the simulator's pair where the model and the decision
// model would be: the header, the rows, the records and the summary are made
// the way a live run with -decider makes them.
func TestTheLiveReportWithADecider(t *testing.T) {
	const key = "sk-test-this-is-not-a-key" // secret-scan: allow
	cases := sim.Cases()[:3]
	pair := func(floor float64) schemaflux.Provider {
		return mw.Decisions(sim.New(50*time.Millisecond), sim.NewDecider(5*time.Millisecond), mw.DecisionOptions{Floor: floor})
	}

	for _, tc := range []struct {
		name      string
		args      []string
		floor     float64
		header    string
		escalates bool
	}{
		{"the decider answers the decisions", []string{"-decider", "jev"}, 0, "decisions to jev,", false},
		{"under a floor it clears", []string{"-decider", "jev", "-floor", "0.7"}, 0.7, "decisions to jev with a floor of 0.7,", false},
		{"under a floor it does not clear", []string{"-decider", "jev", "-floor", "0.999"}, 0.999, "decisions to jev with a floor of 0.999,", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked, err := parse(append([]string{"-mode", "live", "-flow", "fused"}, tc.args...), &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			asked.deciderKey = "tsk-the-decider-key-0123456789"
			synctest.Test(t, func(t *testing.T) {
				var stdout, sink bytes.Buffer
				failed, err := liveThrough(context.Background(), asked, cases, pair(tc.floor), nil, key, &stdout, &sink)
				if err != nil || failed {
					t.Fatalf("failed: %t, err: %v\n%s", failed, err, stdout.String())
				}
				if first := strings.SplitN(stdout.String(), "\n", 2)[0]; !strings.Contains(first, tc.header) {
					t.Errorf("the header does not say %q: %s", tc.header, first)
				}
				decided, escalated := 0, 0
				for _, line := range strings.Split(strings.TrimSpace(sink.String()), "\n") {
					var made record
					if err := json.Unmarshal([]byte(line), &made); err != nil {
						t.Fatal(err)
					}
					if !made.Match || made.Decider != "jev" || made.Floor != tc.floor {
						t.Errorf("%s: %+v", made.ID, made)
					}
					decided += made.DecidedCalls
					escalated += made.Escalations
				}
				if tc.escalates && (decided != 0 || escalated == 0) || !tc.escalates && (decided == 0 || escalated != 0) {
					t.Errorf("%d decisions answered by the decider and %d sent on", decided, escalated)
				}
				if !strings.Contains(stdout.String(), "\ndecisions: ") {
					t.Errorf("the report has no line on how the decisions were answered:\n%s", stdout.String())
				}
				if strings.Contains(stdout.String()+sink.String(), key) || strings.Contains(stdout.String()+sink.String(), asked.deciderKey) {
					t.Error("a key is in the report")
				}
			})
		})
	}
}

func TestWhatAMeteredCallSaysOfADecision(t *testing.T) {
	yes := schemaflux.DecisionAnswer{Kind: schemaflux.DecisionYesNo, Yes: true}
	cases := []struct {
		name      string
		resp      schemaflux.Response
		decider   string
		escalated string
	}{
		{"a generating provider's answer", schemaflux.Response{Content: `{"q1":true}`, Provider: "openai"}, "", ""},
		{"a typed answer", schemaflux.Response{Provider: "jev", Answers: []schemaflux.DecisionAnswer{yes}}, "jev", ""},
		{"a typed answer from a provider that names nobody", schemaflux.Response{Answers: []schemaflux.DecisionAnswer{yes}}, "", ""},
		{"nothing", schemaflux.Response{}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := answeredTyped(tc.resp); got != tc.decider {
				t.Errorf("answeredTyped = %q, want %q", got, tc.decider)
			}
			if got := escalated(tc.resp); got != tc.escalated {
				t.Errorf("escalated = %q, want %q", got, tc.escalated)
			}
		})
	}

	// An answer after an escalation, as the pair returns it.
	generator := sim.New(time.Millisecond)
	low := sim.NewDecider(time.Millisecond)
	low.ModelConfidence = 0.2
	provider := measure(mw.Decisions(generator, low, mw.DecisionOptions{Floor: 0.5}))
	labelled := sim.Cases()[1]
	client := schemaflux.Client{Provider: provider}
	if _, err := schemaflux.Is(schemaflux.WithClient(context.Background(), client), labelled.Message.Text(),
		"Is this message spam, a marketing blast or an automated notification, and not a customer writing to a support desk?"); err != nil {
		t.Fatalf("Is: %v", err)
	}
	calls, _ := provider.made()
	if len(calls) != 1 || calls[0].Decider != "" || calls[0].Escalated != "below the floor" || calls[0].Said == "" {
		t.Errorf("the call is recorded as %+v", calls)
	}
	made := record{Operations: calls}
	made.decided(settings{decider: "jev", floor: 0.5})
	if made.Decider != "jev" || made.Floor != 0.5 || made.DecidedCalls != 0 || made.Escalations != 1 {
		t.Errorf("the record counts %+v", made)
	}
	if got := deciding(settings{}); got != "" {
		t.Errorf("deciding with no decider = %q", got)
	}
}
