// Command bench times the support desk's four flows over the labelled
// messages.
//
// Offline, which is the default, the provider is the simulator: every answer
// is the label's and takes a fixed time to arrive, so the numbers are the
// shape of the program and are the same on every run apart from the
// milliseconds.
//
//	go run ./examples/flowdesk/cmd/bench
//	go run ./examples/flowdesk/cmd/bench -flow fused -delay 50ms
//
// Live, the same messages go to a real model through the library's OpenAI
// provider, and the report adds what the model got right, the tokens and what
// they cost. It spends money, so it has to be asked for, and the key is read
// from the OPENAI_API_KEY environment variable and from nowhere else.
//
//	go run ./examples/flowdesk/cmd/bench -mode live -n 3 -out runs.jsonl
//
// With -decider the desk's decisions, the questions whose answer is a yes or
// no, a choice or a level, go to a decision provider, and everything it writes
// still goes to the generating one (mw.Decisions). Offline the decision provider
// is the simulator's own, which answers from the same labels in a tenth of the
// time. Live it is one of the library's, by name, with its key read from its
// own variable: jev (TYPESAFE_API_KEY), jev-openrouter (OPENROUTER_API_KEY) or
// jev-litellm (LITELLM_API_KEY, and LITELLM_BASE_URL). -floor is the probability
// below which a decision provider's answer is not used and the generating
// provider is asked: there is none unless it is named.
//
//	go run ./examples/flowdesk/cmd/bench -decider sim -flow every
//	go run ./examples/flowdesk/cmd/bench -mode live -decider jev -floor 0.7 -n 3
//
// What a live comparison of the flows does not hold equal. When no -effort is
// named the library chooses each call's reasoning effort itself, from what the
// call carries: none when neither its questions nor the text they are asked of
// mention a number, and its default when one does. The same question can
// therefore run at different efforts in different flows, since each flow puts
// it in a different call with different company: the fast flow's one look
// carries the urgency's scale and always reasons, where the one-at-a-time
// flow's single yes-or-no question about a text with no digit in it does not.
// Latency and accuracy compared across flows are then compared at efforts the
// flows did not choose alike by design. Naming -effort holds every call to one;
// the live report says so when it is not named.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/examples/flowdesk/desk"
	"github.com/monstercameron/schemaflux/v2/examples/flowdesk/sim"
	"github.com/monstercameron/schemaflux/v2/mw"
	"github.com/monstercameron/schemaflux/v2/provider"
)

// A flow is one of the three ways the desk handles a message.
type flow struct {
	name   string
	handle func(*desk.Desk, context.Context, desk.Message) (desk.Outcome, error)
}

var flows = []flow{
	{"fused", (*desk.Desk).Handle},
	{"oneatatime", (*desk.Desk).HandleOneAtATime},
	{"fast", (*desk.Desk).HandleFast},
	{"ahead", (*desk.Desk).HandleAhead},
}

// settings is what the flags asked for.
type settings struct {
	mode    string
	flows   []flow
	delay   time.Duration
	model   string
	effort  string
	triage  string
	tier    string
	limit   int
	reps    int
	out     string
	ceiling float64
	timeout time.Duration
	hedge   time.Duration
	trace   bool
	logs    bool

	// decider names the decision provider the desk's decisions go to, or is
	// empty for none; floor is the probability below which its answer is passed
	// over for the generating provider's, and zero is no floor. deciderKey is
	// the live decision provider's key, read from its own variable.
	decider    string
	floor      float64
	deciderKey string
}

// deciderKeys is the environment variable each live decision provider's key is
// read from.
var deciderKeys = map[string]string{
	"jev":            "TYPESAFE_API_KEY",
	"jev-openrouter": "OPENROUTER_API_KEY",
	"jev-litellm":    "LITELLM_API_KEY",
}

// minimumDelay is the shortest delay the offline mode takes. The simulator's
// calls overlap when each takes longer than it takes to start the next, which
// on a busy machine a millisecond did not always manage, and a group of three
// calls was counted as three waits.
const minimumDelay = 5 * time.Millisecond

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	asked, err := parse(args, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stderr, "bench:", err)
		}
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cases := sim.Cases()
	if asked.limit > 0 && asked.limit < len(cases) {
		cases = cases[:asked.limit]
	}

	// The key is looked for before the -out file is created, so that a run
	// that cannot start leaves nothing behind.
	key := os.Getenv("OPENAI_API_KEY")
	if asked.mode == "live" && key == "" {
		fmt.Fprintln(stderr, "bench: live mode reads the key from the OPENAI_API_KEY environment variable, and it is not set")
		return 2
	}
	if asked.mode == "live" && asked.decider != "" {
		variable := deciderKeys[asked.decider]
		if asked.deciderKey = os.Getenv(variable); asked.deciderKey == "" {
			fmt.Fprintf(stderr, "bench: -decider %s reads its key from the %s environment variable, and it is not set\n", asked.decider, variable)
			return 2
		}
	}

	var sink io.Writer
	if asked.out != "" {
		file, err := os.OpenFile(asked.out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(stderr, "bench:", err)
			return 2
		}
		defer file.Close()
		sink = file
	}

	var failed bool
	switch asked.mode {
	case "offline":
		failed = offline(ctx, asked, cases, stdout, sink)
	default:
		failed, err = live(ctx, asked, cases, key, stdout, sink)
		if err != nil {
			fmt.Fprintln(stderr, "bench:", withoutKey(withoutKey(err.Error(), key), asked.deciderKey))
			return 2
		}
	}
	if failed {
		return 1
	}
	return 0
}

func parse(args []string, stderr io.Writer) (settings, error) {
	var asked settings
	var which string
	flags := flag.NewFlagSet("bench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&asked.mode, "mode", "offline", "offline (the simulator) or live (a real model, which spends money)")
	flags.StringVar(&which, "flow", "both", "fused, oneatatime, fast, ahead, both (fused and oneatatime), all (the first three) or every")
	flags.DurationVar(&asked.delay, "delay", 200*time.Millisecond, "offline: how long the simulator takes over each call, at least 5ms")
	flags.StringVar(&asked.model, "model", "gpt-6-luna", "live: the model every operation uses")
	flags.StringVar(&asked.effort, "effort", "", "live: the reasoning effort every operation uses (none, minimal, low, medium, high); empty leaves it to the library, which picks one per call from what the call carries")
	flags.StringVar(&asked.triage, "triage-effort", "", "live: the reasoning effort of the triage alone, over -effort; empty leaves it at -effort")
	flags.StringVar(&asked.tier, "tier", "", "live: the provider's service tier (priority, flex, default, auto); empty sends none")
	flags.IntVar(&asked.limit, "n", 0, "run only the first n messages (0 runs them all)")
	flags.IntVar(&asked.reps, "reps", 1, "how many times to run each message through each flow")
	flags.StringVar(&asked.out, "out", "", "append one JSON object per message run to this file")
	flags.Float64Var(&asked.ceiling, "ceiling", 0.05, "live: the most one message run may spend, in USD")
	flags.DurationVar(&asked.timeout, "timeout", 60*time.Second, "live: how long one call to the model may take")
	flags.DurationVar(&asked.hedge, "hedge", 0, "live: send a second copy of a call that has not answered within this long, and use whichever answers first (0 sends every call once)")
	flags.BoolVar(&asked.trace, "trace", false, "after each run, list its calls in the order they started: when, how long, and what the model answered")
	flags.StringVar(&asked.decider, "decider", "", "send the decisions to a decision provider and everything else to the generating one: offline any name plays the simulator's decider; live it is jev, jev-openrouter or jev-litellm, with its key in its own variable")
	flags.Float64Var(&asked.floor, "floor", 0, "with -decider: the probability below which the decision provider's answer is passed over and the generating provider is asked (0 is no floor; measure one on labelled data before trusting it)")
	flags.BoolVar(&asked.logs, "logs", false, "let the library's own error log through to standard error. It is not filtered: the report's key filter does not see the library's log, so an error it logs can quote what a provider replied")
	if err := flags.Parse(args); err != nil {
		return asked, err
	}

	switch asked.mode {
	case "offline", "live":
	default:
		return asked, fmt.Errorf("-mode is %q; it is offline or live", asked.mode)
	}
	switch which {
	case "both":
		asked.flows = flows[:2]
	case "all":
		asked.flows = flows[:3]
	case "every":
		asked.flows = flows
	case "fused":
		asked.flows = flows[:1]
	case "oneatatime":
		asked.flows = flows[1:2]
	case "fast":
		asked.flows = flows[2:3]
	case "ahead":
		asked.flows = flows[3:]
	default:
		return asked, fmt.Errorf("-flow is %q; it is fused, oneatatime, fast, ahead, both (fused and oneatatime), all (the first three) or every", which)
	}
	switch {
	case asked.delay < 0:
		return asked, fmt.Errorf("-delay is negative")
	case asked.mode == "offline" && asked.delay < minimumDelay:
		return asked, fmt.Errorf("-delay is %v and offline it is at least %v: the waits are counted from which calls overlap, and a call that takes less than it takes to start the next does not overlap it, so below that the count is the scheduler's noise and not the shape of the program", asked.delay, minimumDelay)
	case asked.reps < 1:
		return asked, fmt.Errorf("-reps is %d; it is at least 1", asked.reps)
	case asked.limit < 0:
		return asked, fmt.Errorf("-n is negative")
	case asked.hedge < 0:
		return asked, fmt.Errorf("-hedge is negative")
	case asked.mode == "live" && asked.ceiling <= 0:
		return asked, fmt.Errorf("-ceiling is %v; a live run needs a ceiling above zero", asked.ceiling)
	case asked.mode == "live" && strings.TrimSpace(asked.model) == "":
		return asked, fmt.Errorf("-model is empty")
	case !(asked.floor >= 0 && asked.floor <= 1):
		return asked, fmt.Errorf("-floor is %v; it is a probability, from 0 to 1", asked.floor)
	case asked.floor > 0 && strings.TrimSpace(asked.decider) == "":
		return asked, fmt.Errorf("-floor is for a decision provider, and no -decider is named")
	}
	asked.decider = strings.ToLower(strings.TrimSpace(asked.decider))
	if asked.mode == "live" && asked.decider != "" {
		if _, known := deciderKeys[asked.decider]; !known {
			return asked, fmt.Errorf("-decider is %q; live it is jev, jev-openrouter or jev-litellm", asked.decider)
		}
	}
	return asked, nil
}

// call is one call to the provider, as a record of a run lists it.
type call struct {
	Operation string `json:"operation"`
	// StartMS is when the call started, counted from the start of the run, the
	// same instant the run's WallMS is counted from, and MS how long it took.
	StartMS         float64 `json:"start_ms"`
	MS              float64 `json:"ms"`
	InputTokens     int     `json:"input_tokens,omitempty"`
	OutputTokens    int     `json:"output_tokens,omitempty"`
	ReasoningTokens int     `json:"reasoning_tokens,omitempty"`
	Failed          bool    `json:"failed,omitempty"`

	// Where the call's time went, as the library's provider measured it: the
	// connection it had to set up (nothing, on one it reused), the wait for
	// the first byte, which is the model's own time, and the reading of the
	// answer.
	Reused  bool    `json:"reused,omitempty"`
	SetupMS float64 `json:"setup_ms,omitempty"`
	WaitMS  float64 `json:"wait_ms,omitempty"`
	ReadMS  float64 `json:"read_ms,omitempty"`

	// Said is what the call answered, on one line: a decision as its questions
	// and their answers, anything else as the start of its text. It is written
	// after the run, from prompt and reply, which are what the call kept: the
	// reading of a request and a reply is work, and done while the run was
	// still going it was counted as the run's own time.
	Said          string `json:"said,omitempty"`
	prompt, reply string

	// Decider is the decision provider that answered the call, typed, and is
	// empty for a call a generating provider answered. Escalated is why a
	// decision provider that was asked first was passed over, when one was:
	// "below the floor", "unavailable" or "unsupported".
	Decider   string `json:"decider,omitempty"`
	Escalated string `json:"escalated,omitempty"`
}

// record is one message through one flow. It is a row of the report and a
// line of the -out file.
type record struct {
	Time  string `json:"time"`
	Mode  string `json:"mode"`
	Model string `json:"model,omitempty"`
	Flow  string `json:"flow"`
	Rep   int    `json:"rep"`
	ID    string `json:"id"`
	Kind  string `json:"kind"`

	// What the run was run with. Records of runs made with different
	// parameters can be appended to one file and told apart. DelayMS is the
	// simulator's delay, offline. The rest are live: Effort is empty when the
	// library chose each call's effort, TriageEffort and Tier are empty when
	// not named, and HedgeMS is zero when every call was sent once.
	DelayMS      float64 `json:"delay_ms,omitempty"`
	Effort       string  `json:"effort,omitempty"`
	TriageEffort string  `json:"triage_effort,omitempty"`
	Tier         string  `json:"tier,omitempty"`
	HedgeMS      float64 `json:"hedge_ms,omitempty"`
	// Decider is the decision provider the run's decisions went to, and Floor
	// the probability below which its answer was passed over. Both are empty
	// for a run with none.
	Decider string  `json:"decider,omitempty"`
	Floor   float64 `json:"floor,omitempty"`

	Status     string `json:"status"`
	WantStatus string `json:"want_status"`
	// Match says the outcome is the label's: every field of it offline, and
	// the fields a model decides, as opposed to writes, live.
	Match      bool     `json:"match"`
	Mismatches []string `json:"mismatches,omitempty"`
	Redrafted  bool     `json:"redrafted"`
	Error      string   `json:"error,omitempty"`

	Calls       int `json:"calls"`
	MaxInFlight int `json:"max_in_flight"`
	// Waits is the number of times the run waited for the provider, counted
	// from the order of events, and CriticalPathMS is the same count as time:
	// the sum over the waits of the longest call of each, as measured. Waits
	// alone says nothing of how long a wait was.
	Waits          int     `json:"waits"`
	CriticalPathMS float64 `json:"critical_path_ms"`
	// HedgedCalls is how many calls of the run were sent a second time.
	HedgedCalls int `json:"hedged_calls,omitempty"`
	// DecidedCalls is how many of the run's calls a decision provider answered,
	// and Escalations how many it was asked first and the generating provider
	// answered.
	DecidedCalls int `json:"decided_calls,omitempty"`
	Escalations  int `json:"escalations,omitempty"`

	// WallMS is the whole run. ProviderMS is the part of it during which at
	// least one call was in flight, and OutsideMS is the rest: what the
	// library and the desk did between calls.
	WallMS        float64 `json:"wall_ms"`
	ProviderMS    float64 `json:"provider_ms"`
	OutsideMS     float64 `json:"outside_ms"`
	ProviderShare float64 `json:"provider_share"`

	// Tokens are the provider's own figures, summed over the calls. Cost is
	// what the client's ledger recorded, and CostComplete is false when a
	// call could not be priced, or when a hedge sent a second copy of a call
	// that the ledger does not see, in which case the cost is a floor.
	InputTokens     int      `json:"input_tokens,omitempty"`
	OutputTokens    int      `json:"output_tokens,omitempty"`
	CachedTokens    int      `json:"cached_tokens,omitempty"`
	ReasoningTokens int      `json:"reasoning_tokens,omitempty"`
	CostUSD         *float64 `json:"cost_usd,omitempty"`
	CostComplete    bool     `json:"cost_complete,omitempty"`

	Operations []call `json:"operations"`
}

func milliseconds(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

// handle runs one message through one flow and measures it. The outcome is
// returned with the record so that each mode can compare it with the label in
// its own way.
//
// The provider is told when the run starts, and counts each call's start from
// that same instant: the run's wall time is counted from it too, so a call
// that began at the very start of the run begins at zero and none can end after
// the run does.
func handle(ctx context.Context, with flow, labelled sim.Case, provider *metered, client schemaflux.Client, support *desk.Desk) (desk.Outcome, record) {
	started := time.Now()
	provider.start(started)
	outcome, err := with.handle(support, schemaflux.WithClient(ctx, client), labelled.Message)
	wall := time.Since(started)
	busy := provider.Busy()

	made := record{
		Time: started.UTC().Format(time.RFC3339),
		Flow: with.name, ID: labelled.Message.ID, Kind: labelled.Kind,
		Status: outcome.Status, WantStatus: labelled.Status, Redrafted: outcome.Redrafted,
		Calls: provider.Count(), MaxInFlight: provider.MaxInFlight(), Waits: provider.Waits(),
		CriticalPathMS: milliseconds(provider.CriticalPath()),
		WallMS:         milliseconds(wall), ProviderMS: milliseconds(busy), OutsideMS: milliseconds(wall - busy),
	}
	if wall > 0 {
		made.ProviderShare = float64(busy) / float64(wall)
	}
	if err != nil {
		made.Status = "error"
		made.Error = err.Error()
	}
	return outcome, made
}

// write appends a record to the -out file.
func write(sink io.Writer, made record) {
	if sink == nil {
		return
	}
	line, err := json.Marshal(made)
	if err != nil {
		return
	}
	fmt.Fprintf(sink, "%s\n", line)
}

// offline runs the messages against the simulator and reports whether any run
// failed or missed its label.
func offline(ctx context.Context, asked settings, cases []sim.Case, stdout, sink io.Writer) bool {
	fmt.Fprintf(stdout, "flowdesk bench: offline, %v per call%s, %d messages, %d rep(s)\n\n", asked.delay, deciding(asked), len(cases), asked.reps)
	fmt.Fprintf(stdout, "%-5s %-11s %-18s %5s %9s %9s %10s %5s\n", "id", "flow", "status", "calls", "in flight", "wall ms", "outside ms", "waits")

	var all []record
	failed := false
	for rep := 1; rep <= asked.reps; rep++ {
		for _, labelled := range cases {
			for _, with := range asked.flows {
				provider := measure(simulated(asked))
				client := schemaflux.Client{Provider: provider}
				outcome, made := handle(ctx, with, labelled, provider, client, desk.New(sim.OpenTickets()))
				made.Mode, made.Rep = asked.mode, rep
				made.DelayMS = milliseconds(asked.delay)
				made.Operations, _ = provider.made()
				made.decided(asked)
				made.Match = made.Error == "" && outcome == labelled.Want()
				if !made.Match && made.Error == "" {
					made.Mismatches = differences(outcome, labelled.Want())
				}
				failed = failed || !made.Match

				fmt.Fprintf(stdout, "%-5s %-11s %-18s %5d %9d %9.0f %10.0f %5d%s\n",
					made.ID, made.Flow, made.Status, made.Calls, made.MaxInFlight, made.WallMS, made.OutsideMS, made.Waits, note(made, ""))
				if asked.trace {
					playByPlay(stdout, labelled, outcome, made)
				}
				write(sink, made)
				all = append(all, made)
				if ctx.Err() != nil {
					return true
				}
			}
		}
	}

	fmt.Fprintf(stdout, "\n%-11s %8s %7s %6s %6s %6s %12s %14s %10s\n", "flow", "messages", "matched", "errors", "calls", "waits", "mean wall ms", "median wall ms", "mean waits")
	for _, with := range asked.flows {
		total := summarise(all, with.name)
		fmt.Fprintf(stdout, "%-11s %8d %7d %6d %6d %6d %12.0f %14.0f %10.2f\n",
			with.name, total.runs, total.matched, total.errors, total.calls, total.waits, total.meanWall, total.medianWall, total.meanWaits)
	}
	decisions(stdout, asked, all)
	return failed
}

// simulated is the offline provider: the simulator, or, with -decider, the
// simulator paired with its own decision provider, which answers the desk's
// decisions from the same labels in a tenth of the time.
func simulated(asked settings) schemaflux.Provider {
	generator := sim.New(asked.delay)
	if asked.decider == "" {
		return generator
	}
	return mw.Decisions(generator, sim.NewDecider(max(asked.delay/10, minimumDelay)), mw.DecisionOptions{Floor: asked.floor})
}

// deciding is what a report's first line says of the decision provider.
func deciding(asked settings) string {
	if asked.decider == "" {
		return ""
	}
	said := ", decisions to " + asked.decider
	if asked.floor > 0 {
		said += fmt.Sprintf(" with a floor of %v", asked.floor)
	}
	return said
}

// decided counts, on a run's record, the calls a decision provider answered and
// the ones it was asked first and passed over, and names the provider and the
// floor the run was made with.
func (made *record) decided(asked settings) {
	made.Decider, made.Floor = asked.decider, asked.floor
	for _, one := range made.Operations {
		if one.Decider != "" {
			made.DecidedCalls++
		}
		if one.Escalated != "" {
			made.Escalations++
		}
	}
}

// decisions says, under the summary, how the decisions of the runs were
// answered: by the decision provider, or by the generating provider after it.
func decisions(stdout io.Writer, asked settings, all []record) {
	if asked.decider == "" {
		return
	}
	decided, escalated, assessed := 0, 0, 0
	for _, made := range all {
		decided += made.DecidedCalls
		escalated += made.Escalations
		for _, one := range made.Operations {
			if one.Operation == sim.OpAssess {
				assessed++
			}
		}
	}
	fmt.Fprintf(stdout, "\ndecisions: %d calls were decisions; %s answered %d, and %d it was asked first went on to the generating provider.\n",
		assessed, asked.decider, decided, escalated)
}

// note is what a row says after its numbers: nothing for a run that matched
// its label, and otherwise why it did not. key is taken out of an error, which
// can quote a provider's reply.
func note(made record, key string) string {
	switch {
	case made.Error != "":
		return "  error: " + withoutKey(made.Error, key)
	case len(made.Mismatches) > 0:
		return "  differs: " + strings.Join(made.Mismatches, ", ")
	}
	return ""
}

// keyShaped is anything that looks like an API key: "sk-" and then eight or
// more of the characters a key is made of, which include the asterisks and the
// dots a provider masks one with.
var keyShaped = regexp.MustCompile(`sk-[A-Za-z0-9_\-*.]{8,}`)

// withoutKey takes the key out of a text that is about to be printed or
// written. An error can quote what a provider replied, and a provider can
// quote the key it was sent: whole, masked, or cut short.
//
// The exact key is replaced first. Then anything shaped like a key, which
// catches one cut off anywhere past its first eleven characters and one with
// its middle masked. Then the key's first eight characters and its last four
// where they sit against asterisks or dots, which is what a provider that
// shows "sk-proj-********abcd" shows, and which the other two miss when the
// prefix is not "sk-".
func withoutKey(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "[key]")
	}
	text = keyShaped.ReplaceAllString(text, "[key]")
	if key == "" {
		return text
	}

	prefix := key[:min(8, len(key))]
	suffix := key[max(len(key)-4, 0):]
	masked := regexp.MustCompile(`(?:` + regexp.QuoteMeta(prefix) + `[*.]+(?:` + regexp.QuoteMeta(suffix) + `)?|[*.]+` + regexp.QuoteMeta(suffix) + `)`)
	return masked.ReplaceAllString(text, "[key]")
}

// totals is a flow's line of the summary.
//
// The means, the median and the share are over the runs that completed, the
// ones that returned an outcome: a run that failed returned early, with few
// calls and a short wall time, and counted in it would make the flow that fails
// look faster. They are counted in errors. For the same reason calls and waits
// are the completed runs'. Tokens and cost are all the runs', since a failed
// run spent them.
type totals struct {
	runs, matched, errors, completed, calls, waits int

	meanWall, medianWall, meanWaits float64
	meanProvider, meanOutside       float64
	// share is the time spent in the provider as a part of the time spent in
	// all: total provider time over total wall time, so a long run weighs more
	// than a short one. It is not a mean of each run's own ratio.
	share float64

	input, output int
	cost          float64
	costComplete  bool
}

func summarise(all []record, flowName string) totals {
	total := totals{costComplete: true}
	var walls []float64
	var wall, provider, outside float64
	for _, made := range all {
		if made.Flow != flowName {
			continue
		}
		total.runs++
		total.input += made.InputTokens
		total.output += made.OutputTokens
		if made.CostUSD != nil {
			total.cost += *made.CostUSD
		}
		total.costComplete = total.costComplete && made.CostComplete
		if made.Error != "" {
			total.errors++
			continue
		}
		total.completed++
		if made.Match {
			total.matched++
		}
		total.calls += made.Calls
		total.waits += made.Waits
		walls = append(walls, made.WallMS)
		wall += made.WallMS
		provider += made.ProviderMS
		outside += made.OutsideMS
	}
	if total.completed == 0 {
		return total
	}
	completed := float64(total.completed)
	total.meanWall = wall / completed
	total.meanProvider = provider / completed
	total.meanOutside = outside / completed
	total.meanWaits = float64(total.waits) / completed
	if wall > 0 {
		total.share = provider / wall
	}
	total.medianWall = middle(walls)
	return total
}

// differences names the fields of an outcome that are not the label's.
func differences(got, want desk.Outcome) []string {
	var found []string
	differs := func(name string, same bool) {
		if !same {
			found = append(found, name)
		}
	}
	differs("status", got.Status == want.Status)
	differs("language", got.Language == want.Language)
	differs("team", got.Team == want.Team)
	differs("intent", got.Intent == want.Intent)
	differs("urgency", got.Urgency == want.Urgency)
	differs("ticket", got.TicketID == want.TicketID && got.New == want.New)
	differs("details", got.Details == want.Details)
	differs("reply", got.Reply == want.Reply)
	differs("redrafted", got.Redrafted == want.Redrafted)
	differs("handoff", got.Handoff == want.Handoff)
	differs("next action", got.NextAction == want.NextAction)
	differs("writes again", got.WillWriteAgain == want.WillWriteAgain)
	return found
}

// decisionDifferences names the decisions of an outcome that are not the
// label's. It is what a model's run is held to: the status, and for a message
// that was worked the team, the intent, the urgency to within one, the
// language and the ticket. What the model wrote, and whether its first draft
// passed the audit, vary from run to run and are not counted, and neither are
// the next action, the prediction that the customer writes again and the
// details: the live report's "match" is this and no more, and says so.
func decisionDifferences(got, want desk.Outcome) []string {
	var found []string
	differs := func(name string, same bool) {
		if !same {
			found = append(found, name)
		}
	}
	differs("status", got.Status == want.Status)
	if got.Status == desk.StatusDiscarded || want.Status == desk.StatusDiscarded {
		// A message that was discarded, or should have been, was not triaged
		// on one side, and there is nothing further to compare.
		return found
	}
	differs("language", got.Language == want.Language)
	differs("team", got.Team == want.Team)
	differs("intent", got.Intent == want.Intent)
	differs("urgency", got.Urgency-want.Urgency <= 1 && want.Urgency-got.Urgency <= 1)
	differs("ticket", got.TicketID == want.TicketID && got.New == want.New)
	return found
}

// metered is a real provider with a count kept of its calls: how many, how
// many at once, how long each took and what tokens each reported. The library
// does report usage for a call that returns a Result, but the desk's flow is
// twenty calls of ten kinds, most of which return a plain value, so the count
// is taken here, where every one of them passes.
//
// A call keeps the least it can while the run is going: the operation, the
// times, the tokens, and the request's user prompt and the reply's text, which
// are strings it already holds. What each call said is worked out from those
// after the run.
type metered struct {
	schemaflux.Provider
	log    sim.Log
	hedges *hedging
	// began is when the run this provider measures began: each call's start
	// is counted from it. It is set before the run, by start.
	began time.Time

	mu    sync.Mutex
	calls []call
	usage schemaflux.Usage
}

// measure returns a provider with a count kept of its calls, for a run that
// starts now.
func measure(provider schemaflux.Provider) *metered {
	return &metered{Provider: provider, began: time.Now()}
}

// start says when the run begins. It is called before the flow is, and the
// calls the flow makes count their starts from it.
func (m *metered) start(at time.Time) { m.began = at }

func (m *metered) Complete(ctx context.Context, req schemaflux.Request) (schemaflux.Response, error) {
	operation := sim.Operation(req)
	if operation == "" {
		operation = "other"
	}
	end := m.log.Begin(operation)
	started := time.Now()
	resp, err := m.Provider.Complete(ctx, req)
	took := time.Since(started)
	end()
	if m.hedges != nil {
		m.hedges.saw(resp.Hedged)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, call{
		Operation: operation, StartMS: milliseconds(started.Sub(m.began)), MS: milliseconds(took),
		prompt: req.User, reply: sim.Reply(req, resp),
		Decider: answeredTyped(resp), Escalated: escalated(resp),
		InputTokens: resp.Usage.Input, OutputTokens: resp.Usage.Output,
		ReasoningTokens: resp.Usage.Reasoning,
		Failed:          err != nil,
		Reused:          resp.Timing.Reused,
		SetupMS:         milliseconds(resp.Timing.Setup()),
		WaitMS:          milliseconds(resp.Timing.Wait),
		ReadMS:          milliseconds(resp.Timing.Read),
	})
	m.usage.Input += resp.Usage.Input
	m.usage.Output += resp.Usage.Output
	m.usage.Cached += resp.Usage.Cached
	m.usage.Reasoning += resp.Usage.Reasoning
	m.usage.Total += resp.Usage.Total
	return resp, err
}

// answeredTyped names the decision provider that answered a call typed, or is
// empty for an answer a generating provider wrote.
func answeredTyped(resp schemaflux.Response) string {
	if len(resp.Answers) == 0 {
		return ""
	}
	return resp.Provider
}

// escalated is why a decision provider that was asked first was passed over,
// or is empty when none was.
func escalated(resp schemaflux.Response) string {
	if resp.Escalation == nil {
		return ""
	}
	return resp.Escalation.Reason
}

// made returns the calls so far, each with what it said, and the tokens they
// reported between them. It reads every request and reply, so it is for after
// a run.
func (m *metered) made() ([]call, schemaflux.Usage) {
	m.mu.Lock()
	calls := append([]call(nil), m.calls...)
	usage := m.usage
	m.mu.Unlock()

	for i := range calls {
		calls[i].Said = said(calls[i].Operation, schemaflux.Request{User: calls[i].prompt}, calls[i].reply)
	}
	return calls, usage
}

func (m *metered) Count() int                  { return m.log.Count() }
func (m *metered) MaxInFlight() int            { return m.log.MaxInFlight() }
func (m *metered) Waits() int                  { return m.log.Waits() }
func (m *metered) Busy() time.Duration         { return m.log.Busy() }
func (m *metered) CriticalPath() time.Duration { return m.log.CriticalPath() }

// live runs the messages against a real model, through the library's OpenAI
// provider.
func live(ctx context.Context, asked settings, cases []sim.Case, key string, stdout, sink io.Writer) (failed bool, err error) {
	openai, err := provider.New("openai", provider.Config{
		APIKey: key, Timeout: asked.timeout, Retries: 2, RetryBackoff: time.Second,
		ServiceTier: asked.tier,
	})
	if err != nil {
		return false, err
	}
	// The connection is opened before the first message, so that the first
	// message's first call does not pay for it and the figures for every
	// message are of the same kind.
	if err := (schemaflux.Client{Provider: openai, Model: asked.model}).Warm(ctx); err != nil {
		return false, err
	}
	var hedges hedging
	model := hedges.through(openai, asked.hedge)
	if asked.decider != "" {
		// The decision provider, by name. The decisions go to it and everything
		// else to the model above; a hedge is the generating provider's alone.
		decider, err := provider.New(asked.decider, provider.Config{APIKey: asked.deciderKey, Timeout: asked.timeout})
		if err != nil {
			return false, err
		}
		if err := (schemaflux.Client{Provider: decider}).Warm(ctx); err != nil {
			return false, err
		}
		model = mw.Decisions(model, decider, mw.DecisionOptions{Floor: asked.floor})
	}
	failed, err = liveThrough(ctx, asked, sim.WithoutReferences(cases), model, &hedges, key, stdout, sink)
	hedges.report(stdout, asked.hedge)
	return failed, err
}

// liveThrough is the live run against a given provider. It is apart from live
// so that the report can be tested with a provider that is not a model.
// hedges is the count the provider's hedge, if it has one, reports to.
func liveThrough(ctx context.Context, asked settings, cases []sim.Case, model schemaflux.Provider, hedges *hedging, key string, stdout, sink io.Writer) (failed bool, err error) {
	if hedges == nil {
		hedges = &hedging{}
	}
	runs := len(cases) * len(asked.flows) * asked.reps
	effort := asked.effort
	if effort == "" {
		effort = "the library's"
	}
	parameters := ""
	if asked.triage != "" {
		parameters += ", triage effort " + asked.triage
	}
	if asked.tier != "" {
		parameters += ", tier " + asked.tier
	}
	if asked.hedge > 0 {
		parameters += ", hedge after " + asked.hedge.String()
	}
	parameters += deciding(asked)
	fmt.Fprintf(stdout, "flowdesk bench: live, model %s, effort %s%s, %d messages, %d rep(s), %d runs, at most $%.2f each\n",
		asked.model, effort, parameters, len(cases), asked.reps, runs, asked.ceiling)
	if asked.effort == "" {
		fmt.Fprintln(stdout, "note: with no -effort the library picks each call's reasoning effort from what the call carries (none when no number is in its questions or its text), so the flows do not run their questions at the same effort.")
	}
	fmt.Fprintf(stdout, "match: the status, language, team, intent, ticket and an urgency within one of the label's; the next action, the details and the wording are not held to it.\n\n")
	fmt.Fprintf(stdout, "%-5s %-11s %3s %-18s %-5s %5s %5s %8s %8s %6s %7s %7s %10s\n",
		"id", "flow", "rep", "status", "match", "calls", "waits", "wall ms", "model ms", "model%", "in tok", "out tok", "cost USD")

	var all []record
	for rep := 1; rep <= asked.reps; rep++ {
		for _, labelled := range cases {
			for _, with := range asked.flows {
				provider := measure(model)
				provider.hedges = hedges
				// A client of its own for each run, so that its ledger is the
				// cost of that run and its ceiling stops that run alone.
				//
				// The model and the effort are the client's: the desk makes its
				// calls through a dozen operations whose options it builds in
				// as many ways, and the client is the one place that reaches
				// them all.
				budget := &schemaflux.Budget{Limit: asked.ceiling}
				client := schemaflux.Client{Provider: provider, Budget: budget, Model: asked.model,
					Effort: schemaflux.Effort(asked.effort), Timeout: asked.timeout}
				support := desk.New(sim.OpenTickets())
				support.Judgment.Effort = schemaflux.Effort(asked.triage)

				hedgedBefore := hedges.sentTwice.Load()
				outcome, made := handle(ctx, with, labelled, provider, client, support)
				made.Mode, made.Model, made.Rep = asked.mode, asked.model, rep
				made.Effort, made.TriageEffort, made.Tier, made.HedgeMS = asked.effort, asked.triage, asked.tier, milliseconds(asked.hedge)
				made.Error = withoutKey(withoutKey(made.Error, key), asked.deciderKey)
				calls, usage := provider.made()
				made.Operations = calls
				made.decided(asked)
				made.InputTokens, made.OutputTokens = usage.Input, usage.Output
				made.CachedTokens, made.ReasoningTokens = usage.Cached, usage.Reasoning
				spent, complete := budget.Spent()
				// A second copy of a call that a hedge sent is a request the
				// provider bills and the client's ledger does not see: the cost
				// of a run that had one is a floor.
				made.HedgedCalls = int(hedges.sentTwice.Load() - hedgedBefore)
				if made.HedgedCalls > 0 {
					complete = false
				}
				made.CostUSD, made.CostComplete = &spent, complete
				if made.Error == "" {
					made.Mismatches = decisionDifferences(outcome, labelled.Want())
					made.Match = len(made.Mismatches) == 0
				}
				failed = failed || made.Error != ""

				fmt.Fprintf(stdout, "%-5s %-11s %3d %-18s %-5t %5d %5d %8.0f %8.0f %5.0f%% %7d %7d %10s%s\n",
					made.ID, made.Flow, made.Rep, made.Status, made.Match, made.Calls, made.Waits,
					made.WallMS, made.ProviderMS, 100*made.ProviderShare, made.InputTokens, made.OutputTokens,
					dollars(spent, complete), note(made, key))
				if asked.trace {
					playByPlay(stdout, labelled, outcome, made)
				}
				write(sink, made)
				all = append(all, made)
				if ctx.Err() != nil {
					return true, ctx.Err()
				}
			}
		}
	}

	fmt.Fprintf(stdout, "\n%-11s %4s %7s %6s %6s %12s %14s %10s %13s %15s %6s %8s %8s %10s\n",
		"flow", "runs", "matched", "errors", "calls", "mean wall ms", "median wall ms", "mean waits",
		"mean model ms", "mean outside ms", "model%", "in tok", "out tok", "cost USD")
	for _, with := range asked.flows {
		total := summarise(all, with.name)
		fmt.Fprintf(stdout, "%-11s %4d %7d %6d %6d %12.0f %14.0f %10.2f %13.0f %15.0f %5.0f%% %8d %8d %10s\n",
			with.name, total.runs, total.matched, total.errors, total.calls, total.meanWall, total.medianWall, total.meanWaits,
			total.meanProvider, total.meanOutside, 100*total.share, total.input, total.output,
			dollars(total.cost, total.costComplete))
	}
	breakdown(stdout, all, asked.flows)
	decisions(stdout, asked, all)
	return failed, nil
}

// breakdown says where the calls' time went, by operation, for each flow: how
// long a call of each kind took, how much of that was the model's own time,
// how much of what it wrote was reasoning, and what the connections cost.
func breakdown(stdout io.Writer, all []record, which []flow) {
	for _, with := range which {
		byOperation := map[string][]call{}
		var names []string
		fresh, calls := 0, 0
		var setup float64
		for _, made := range all {
			if made.Flow != with.name {
				continue
			}
			for _, one := range made.Operations {
				if _, seen := byOperation[one.Operation]; !seen {
					names = append(names, one.Operation)
				}
				byOperation[one.Operation] = append(byOperation[one.Operation], one)
				calls++
				setup += one.SetupMS
				if !one.Reused && one.SetupMS > 0 {
					fresh++
				}
			}
		}
		if calls == 0 {
			continue
		}
		sort.Slice(names, func(i, j int) bool {
			return total(byOperation[names[i]]) > total(byOperation[names[j]])
		})
		fmt.Fprintf(stdout, "\n%s: where the calls' time went (%d calls, %d on a new connection, %.0f ms setting connections up)\n",
			with.name, calls, fresh, setup)
		fmt.Fprintf(stdout, "%-10s %5s %9s %9s %9s %8s %8s %8s %10s\n",
			"operation", "calls", "median ms", "p90 ms", "wait ms", "read ms", "in tok", "out tok", "reasoning")
		for _, name := range names {
			made := byOperation[name]
			var times, waits, reads []float64
			in, out, reasoning := 0, 0, 0
			for _, one := range made {
				times = append(times, one.MS)
				waits = append(waits, one.WaitMS)
				reads = append(reads, one.ReadMS)
				in += one.InputTokens
				out += one.OutputTokens
				reasoning += one.ReasoningTokens
			}
			share := 0.0
			if out > 0 {
				share = 100 * float64(reasoning) / float64(out)
			}
			fmt.Fprintf(stdout, "%-10s %5d %9.0f %9.0f %9.0f %8.0f %8d %8d %9.0f%%\n",
				name, len(made), middle(times), ninetieth(times), middle(waits), middle(reads),
				in/len(made), out/len(made), share)
		}
	}
}

func total(calls []call) float64 {
	sum := 0.0
	for _, one := range calls {
		sum += one.MS
	}
	return sum
}

// middle is the median of some figures: the middle one, or the mean of the two
// in the middle.
func middle(figures []float64) float64 {
	if len(figures) == 0 {
		return 0
	}
	sorted := append([]float64(nil), figures...)
	sort.Float64s(sorted)
	half := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[half-1] + sorted[half]) / 2
	}
	return sorted[half]
}

// ninetieth is the 90th percentile of some figures by the nearest-rank rule:
// the smallest figure that at least nine tenths of them do not exceed. With
// fewer than ten figures that is the largest of them.
func ninetieth(figures []float64) float64 {
	if len(figures) == 0 {
		return 0
	}
	sorted := append([]float64(nil), figures...)
	sort.Float64s(sorted)
	at := (len(sorted)*9 + 9) / 10
	return sorted[min(at, len(sorted))-1]
}

// dollars writes a cost. A ledger that saw a call it could not price holds a
// floor and not a total, and is written as one.
func dollars(usd float64, complete bool) string {
	if complete {
		return fmt.Sprintf("%.6f", usd)
	}
	return fmt.Sprintf(">=%.6f", usd)
}
