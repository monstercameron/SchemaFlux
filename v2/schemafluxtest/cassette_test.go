package schemafluxtest_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/ops"
	"github.com/monstercameron/schemaflux/internal/types"
	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/provider"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A request the tests below send, which differs only by the user message.
func cassetteRequest(user string) schemaflux.Request {
	return schemaflux.Request{
		Model:      "model-a",
		System:     "You sort support tickets. Answer in JSON.",
		User:       user,
		MaxTokens:  200,
		Format:     "json",
		SchemaName: "Ticket",
	}
}

// cassetteTape is a Tape over a scripted Provider and the directory it saves to.
func cassetteTape(t *testing.T, script func(*schemafluxtest.Provider)) (*schemafluxtest.Tape, *schemafluxtest.Provider, string) {
	t.Helper()
	var inner schemafluxtest.Provider
	script(&inner)
	dir := filepath.Join(t.TempDir(), "tape")
	return schemafluxtest.Record(&inner, dir), &inner, dir
}

// cassetteReplayOf saves what the tape recorded and loads it back from disk.
func cassetteReplayOf(t *testing.T, tape *schemafluxtest.Tape, dir string) *schemafluxtest.Player {
	t.Helper()
	if err := tape.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	player, err := schemafluxtest.Replay(dir)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return player
}

func cassetteKind(t *testing.T, err error) schemaflux.Kind {
	t.Helper()
	var e *schemaflux.Error
	if !errors.As(err, &e) {
		t.Fatalf("the error is not a *schemaflux.Error: %T %v", err, err)
	}
	return e.Kind
}

func TestARecordedExchangeReplaysFromDiskWithTheSameAnswer(t *testing.T) {
	t.Parallel()
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) {
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{
				Content: `{"queue":"billing"}`, Model: "model-a-2026", FinishReason: "stop", ServiceTier: "default",
				Usage: schemaflux.Usage{Input: 12, Output: 5, Total: 17, Cached: 4},
			}, nil
		})
	})
	live, err := tape.Complete(t.Context(), cassetteRequest("My invoice is wrong"))
	if err != nil {
		t.Fatal(err)
	}
	player := cassetteReplayOf(t, tape, dir)
	replayed, err := player.Complete(t.Context(), cassetteRequest("My invoice is wrong"))
	if err != nil {
		t.Fatalf("the replay failed: %v", err)
	}
	if fmt.Sprintf("%+v", replayed) != fmt.Sprintf("%+v", live) {
		t.Errorf("the replay answered\n%+v\nand the live provider\n%+v", replayed, live)
	}
	if player.Remaining() != 0 {
		t.Errorf("Remaining = %d after the only exchange was played", player.Remaining())
	}
}

func TestAReplayIsFromTheSameBytesAsTheRecording(t *testing.T) {
	t.Parallel()
	// Save twice, into two directories, from two tapes that saw the same
	// exchanges: the files are the same bytes, so a recording committed twice is
	// not a diff.
	write := func() map[string]string {
		tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) { p.Reply(`{"a":1}`, `{"b":2}`) })
		for _, user := range []string{"first <ticket> & more", "second"} {
			if _, err := tape.Complete(t.Context(), cassetteRequest(user)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tape.Save(); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[entry.Name()] = string(data)
		}
		return files
	}
	first, second := write(), write()
	if len(first) != 2 {
		t.Fatalf("saved %d files, want 2: %v", len(first), slices.Collect(mapKeys(first)))
	}
	for name, data := range first {
		if second[name] != data {
			t.Errorf("%s differs between two recordings of the same exchanges", name)
		}
		if strings.Contains(data, "recorded_at") {
			t.Errorf("%s holds a timestamp: %s", name, data)
		}
		if !strings.HasSuffix(data, "}\n") {
			t.Errorf("%s does not end with a newline", name)
		}
	}
	if !strings.Contains(first["000.json"], "<ticket> & more") {
		t.Errorf("markup in a prompt is escaped in the file, which makes it unreadable: %s", first["000.json"])
	}
}

func mapKeys[K comparable, V any](m map[K]V) iter.Seq[K] {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func TestAFailureReplaysWithItsKindItsStatusAndItsWait(t *testing.T) {
	t.Parallel()
	kinds := []schemaflux.Kind{
		schemaflux.ErrConfiguration, schemaflux.ErrInvalidRequest, schemaflux.ErrUnsupported, schemaflux.ErrUnauthorized,
		schemaflux.ErrPolicy, schemaflux.ErrBudgetExceeded, schemaflux.ErrBusy, schemaflux.ErrRateLimited,
		schemaflux.ErrUnavailable, schemaflux.ErrTimeout, schemaflux.ErrCanceled, schemaflux.ErrClosed,
		schemaflux.ErrTooLarge, schemaflux.ErrTruncated, schemaflux.ErrRefused, schemaflux.ErrInvalidAnswer,
		schemaflux.ErrReviewRequired, schemaflux.ErrInternal,
	}
	for _, kind := range kinds {
		t.Run(kind.Error(), func(t *testing.T) {
			t.Parallel()
			tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) {
				p.Fail(&schemaflux.Error{Kind: kind, Status: 429, RetryAfter: 1500 * time.Millisecond, Message: "words of the library"})
			})
			if _, err := tape.Complete(t.Context(), cassetteRequest("x")); err == nil {
				t.Fatal("the scripted failure did not happen")
			}
			player := cassetteReplayOf(t, tape, dir)
			_, err := player.Complete(t.Context(), cassetteRequest("x"))
			var e *schemaflux.Error
			if !errors.As(err, &e) {
				t.Fatalf("the replayed failure is not an *Error: %v", err)
			}
			if e.Kind != kind || !errors.Is(err, kind) {
				t.Errorf("replayed as %v, recorded as %v", e.Kind, kind)
			}
			if e.Status != 429 || e.RetryAfter != 1500*time.Millisecond {
				t.Errorf("status %d, wait %v: not what was recorded", e.Status, e.RetryAfter)
			}
			if e.Provider != "test" || e.Model != "model-a" {
				t.Errorf("the route of the failure is %q/%q", e.Provider, e.Model)
			}
		})
	}
}

func TestAFailureThatSaysNothingOfItsKindIsAnUnavailableProvider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want schemaflux.Kind
	}{
		{"a bare error", errors.New("boom"), schemaflux.ErrUnavailable},
		{"a wrapped bare error", fmt.Errorf("while calling: %w", errors.New("boom")), schemaflux.ErrUnavailable},
		{"an *Error with no kind", &schemaflux.Error{Message: "m"}, schemaflux.ErrUnavailable},
		{"a bare Kind", schemaflux.ErrRateLimited, schemaflux.ErrRateLimited},
		{"a Kind in a chain", fmt.Errorf("wrapped: %w", schemaflux.ErrTooLarge), schemaflux.ErrTooLarge},
		{"an *Error in a chain", fmt.Errorf("wrapped: %w", &schemaflux.Error{Kind: schemaflux.ErrRefused}), schemaflux.ErrRefused},
		{"an explicit internal error", &schemaflux.Error{Kind: schemaflux.ErrInternal}, schemaflux.ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tape, _, _ := cassetteTape(t, func(p *schemafluxtest.Provider) { p.Fail(tc.err) })
			_, _ = tape.Complete(t.Context(), cassetteRequest("x"))
			recorded := tape.Recorded()
			if len(recorded) != 1 || recorded[0].Failure == nil {
				t.Fatalf("recorded %+v", recorded)
			}
			if got := recorded[0].Failure.Kind; got != tc.want {
				t.Errorf("recorded as %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAFailureKeepsNothingAProviderWrote(t *testing.T) {
	t.Parallel()
	const vendor = "VENDOR-WORDS-ABOUT-THE-USER-MESSAGE-CANARY"
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) {
		p.Fail(&schemaflux.Error{
			Kind: schemaflux.ErrInvalidRequest, Status: 400, Message: vendor, Code: vendor, RequestID: vendor,
			Err: errors.New(vendor),
		})
	})
	_, _ = tape.Complete(t.Context(), cassetteRequest("x"))
	if err := tape.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "000.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "VENDOR") {
		t.Errorf("the file holds what the provider wrote:\n%s", data)
	}
	player, err := schemafluxtest.Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = player.Complete(t.Context(), cassetteRequest("x"))
	if err == nil || strings.Contains(err.Error(), "VENDOR") {
		t.Errorf("the replayed failure %v quotes the provider", err)
	}
	var e *schemaflux.Error
	if errors.As(err, &e) && (e.RequestID != "" || e.Code != "" || e.Detail() != e.Error()) {
		t.Errorf("the replayed failure carries a request id, a code or a detail: %#v", e)
	}
}

func TestACallerGivingUpIsNotAnExchangeOfTheProvider(t *testing.T) {
	t.Parallel()
	tape, _, _ := cassetteTape(t, func(p *schemafluxtest.Provider) { p.Delay(time.Hour) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tape.Complete(ctx, cassetteRequest("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got := tape.Recorded(); len(got) != 0 {
		t.Errorf("a cancellation was recorded as an exchange: %+v", got)
	}

	// A timeout the provider reports while the caller's context is alive is
	// the provider's: it is kept.
	var late schemafluxtest.Provider
	late.Fail(&schemaflux.Error{Kind: schemaflux.ErrTimeout, Status: 504})
	kept := schemafluxtest.Record(&late, t.TempDir())
	_, _ = kept.Complete(t.Context(), cassetteRequest("x"))
	if got := kept.Recorded(); len(got) != 1 || got[0].Failure == nil || got[0].Failure.Kind != schemaflux.ErrTimeout {
		t.Errorf("a gateway timeout was not recorded: %+v", got)
	}
}

func TestATypedDecisionReplaysTypedAndEqualFieldForField(t *testing.T) {
	t.Parallel()
	decision := &schemaflux.Decision{
		State: `"a ticket about a refund"`, Guidance: "be strict",
		Questions: []schemaflux.DecisionQuestion{
			{Kind: schemaflux.DecisionYesNo, Ask: "Is it urgent?"},
			{Kind: schemaflux.DecisionChoice, Ask: "Which queue?", Options: []string{"billing", "tech"}, Criteria: []string{"money", ""}},
			{Kind: schemaflux.DecisionScale, Ask: "How angry?", Low: 1, High: 5},
		},
	}
	decider := schemafluxtest.NewDecider(schemafluxtest.Yes(0.97), schemafluxtest.Chose("billing"), schemafluxtest.Level(4, 0.5))
	dir := filepath.Join(t.TempDir(), "decisions")
	tape := schemafluxtest.Record(decider, dir)
	request := schemaflux.Request{Model: "test-model", Decision: decision}
	live, err := tape.Complete(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Answers) != 3 {
		t.Fatalf("the decider answered %+v", live.Answers)
	}

	player := cassetteReplayOf(t, tape, dir)
	replayed, err := player.Complete(t.Context(), schemaflux.Request{Model: "test-model", Decision: cassetteCopyOfDecision(decision)})
	if err != nil {
		t.Fatalf("the replay of a decision failed: %v", err)
	}
	if !slices.Equal(replayed.Answers, live.Answers) {
		t.Errorf("replayed answers\n%+v\nwant\n%+v", replayed.Answers, live.Answers)
	}
	if replayed.Content != "" {
		t.Errorf("a decision answered with text %q", replayed.Content)
	}

	// The decision is matched too: the same request with one question changed is
	// not the request that was recorded.
	changed := cassetteCopyOfDecision(decision)
	changed.Questions[1].Options = []string{"billing", "sales"}
	again := cassetteReplayOf(t, tape, dir)
	_, err = again.Complete(t.Context(), schemaflux.Request{Model: "test-model", Decision: changed})
	if cassetteKind(t, err) != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), "Decision") {
		t.Errorf("a changed decision was answered or refused for another reason: %v", err)
	}
}

func cassetteCopyOfDecision(d *schemaflux.Decision) *schemaflux.Decision {
	c := &schemaflux.Decision{State: d.State, Guidance: d.Guidance}
	for _, q := range d.Questions {
		q.Options, q.Criteria = slices.Clone(q.Options), slices.Clone(q.Criteria)
		c.Questions = append(c.Questions, q)
	}
	return c
}

func TestAnEscalationReplaysWithTheAnswer(t *testing.T) {
	t.Parallel()
	want := &schemaflux.DecisionEscalation{Provider: "jev", Model: "jev-1", Reason: "below the floor", ModelConfidence: 0.31, ConfidenceReported: true}
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) {
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{
				Content: `{"q1":true}`, Provider: "generator", Model: "gen-1", Escalation: want,
				Answers: []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.8, ConfidenceReported: true}},
			}, nil
		})
	})
	request := cassetteRequest("q")
	if _, err := tape.Complete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	player := cassetteReplayOf(t, tape, dir)
	got, err := player.Complete(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.Escalation == nil || *got.Escalation != *want {
		t.Errorf("escalation = %+v, want %+v", got.Escalation, want)
	}
	if got.Escalation == want {
		t.Error("the replayed escalation is the pointer the provider returned, so a test that changes it changes the cassette")
	}
	if got.Provider != "generator" || got.Model != "gen-1" || len(got.Answers) != 1 || !got.Answers[0].Yes {
		t.Errorf("the rest of the response did not replay: %+v", got)
	}
}

func TestACredentialInAPromptIsRedactedInTheFileAndStillMatches(t *testing.T) {
	t.Parallel()
	const key = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789" // secret-scan: allow: a key-shaped string that is not a key
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) {
		p.Reply(`{"echo":"the model repeated sk-ant-api03-abcdefghijklmnopqrstuvwxyz and Authorization: Bearer abcdef123456"}`) // secret-scan: allow
	})
	request := cassetteRequest("my key is " + key)
	request.Decision = &schemaflux.Decision{State: `"` + key + `"`, Guidance: key, Questions: []schemaflux.DecisionQuestion{
		{Kind: schemaflux.DecisionChoice, Ask: key, Options: []string{key, "b"}, Criteria: []string{key, ""}},
	}}
	if _, err := tape.Complete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	player := cassetteReplayOf(t, tape, dir)

	data, err := os.ReadFile(filepath.Join(dir, "000.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{key, "abcdefghijklmnopqrstuvwxyz0123456789", "api03-abcdefghijklmnopqrstuvwxyz", "Bearer abcdef123456"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("the file holds %q:\n%s", secret, data)
		}
	}
	if !strings.Contains(string(data), "[redacted by schemafluxtest]") {
		t.Errorf("the file does not say that it redacted something:\n%s", data)
	}
	if _, err := player.Complete(t.Context(), request); err != nil {
		t.Errorf("the request that held the key no longer matches the file saved from it: %v", err)
	}
	// The in-memory recording is as it was: only what is written is redacted.
	if got := tape.Recorded()[0].Request.User; !strings.Contains(got, key) {
		t.Errorf("Recorded() holds %q, which is not the request", got)
	}
}

func TestSaveRefusesAFileThatStillHoldsACredentialAndLeavesTheEarlierRecordingAlone(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "tape")
	var good schemafluxtest.Provider
	good.Reply("one")
	first := schemafluxtest.Record(&good, dir)
	if _, err := first.Complete(t.Context(), cassetteRequest("earlier")); err != nil {
		t.Fatal(err)
	}
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "000.json"))
	if err != nil {
		t.Fatal(err)
	}

	// A key in the model name is in no text that is replaced.
	var inner schemafluxtest.Provider
	second := schemafluxtest.Record(&inner, dir)
	request := cassetteRequest("later")
	request.Model = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789" // secret-scan: allow
	if _, err := second.Complete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	err = second.Save()
	if err == nil || cassetteKind(t, err) != schemaflux.ErrConfiguration {
		t.Fatalf("Save of a file holding a key: %v", err)
	}
	if !strings.Contains(err.Error(), "OpenAI key") || strings.Contains(err.Error(), "abcdefghijklmnop") {
		t.Errorf("the refusal must name the shape and not the key: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "000.json"))
	if err != nil || string(after) != string(before) {
		t.Errorf("a refused Save changed the earlier recording: %v", err)
	}
}

func TestAReplayThatIsAskedSomethingItDoesNotHoldSaysWhatWasAsked(t *testing.T) {
	t.Parallel()
	const canary = "USER-MESSAGE-CANARY-4417"
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) { p.Reply("a", "b") })
	if _, err := tape.Complete(t.Context(), cassetteRequest("the recorded message")); err != nil {
		t.Fatal(err)
	}
	player := cassetteReplayOf(t, tape, dir)

	asked := cassetteRequest(canary)
	asked.Temperature = 0.4
	_, err := player.Complete(t.Context(), asked)
	if err == nil {
		t.Fatal("a request the recording does not hold was answered")
	}
	if cassetteKind(t, err) != schemaflux.ErrConfiguration {
		t.Errorf("kind = %v, want configuration", cassetteKind(t, err))
	}
	message := err.Error()
	for _, want := range []string{"request 1", `model "model-a"`, `schema "Ticket"`, "You sort support tickets", "a user message of 24 bytes", "exchange 1", "User", "Temperature"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not say %q: %s", want, message)
		}
	}
	if strings.Contains(message, canary) || strings.Contains(message, "the recorded message") {
		t.Errorf("the message prints a user message: %s", message)
	}
	if player.Remaining() != 1 {
		t.Errorf("a refused request used an exchange: Remaining = %d", player.Remaining())
	}
}

func TestAReplayRunsOutOfExchangesWithAnErrorThatCountsThem(t *testing.T) {
	t.Parallel()
	player := schemafluxtest.ReplayFrom(
		schemafluxtest.Cassette{Request: schemafluxtest.CassetteRequest{User: "a"}, Response: &schemafluxtest.CassetteResponse{Content: "1"}},
		schemafluxtest.Cassette{Request: schemafluxtest.CassetteRequest{User: "a"}, Response: &schemafluxtest.CassetteResponse{Content: "2"}},
	)
	for want := range 2 {
		resp, err := player.Complete(t.Context(), schemaflux.Request{User: "a"})
		if err != nil || resp.Content != fmt.Sprint(want+1) {
			t.Fatalf("call %d: %q %v", want+1, resp.Content, err)
		}
	}
	_, err := player.Complete(t.Context(), schemaflux.Request{User: "a"})
	if err == nil || cassetteKind(t, err) != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), "all 2 exchanges were played") {
		t.Errorf("a third request: %v", err)
	}

	none := schemafluxtest.ReplayFrom()
	_, err = none.Complete(t.Context(), schemaflux.Request{})
	if err == nil || !strings.Contains(err.Error(), "no exchange at all") {
		t.Errorf("an empty replay: %v", err)
	}
}

func TestALenientReplayAnswersInOrderWhateverIsAsked(t *testing.T) {
	t.Parallel()
	player := schemafluxtest.ReplayFrom(
		schemafluxtest.Cassette{Request: schemafluxtest.CassetteRequest{User: "recorded"}, Response: &schemafluxtest.CassetteResponse{Content: "one"}},
		schemafluxtest.Cassette{Request: schemafluxtest.CassetteRequest{User: "other"}, Response: &schemafluxtest.CassetteResponse{Content: "two"}},
	)
	if _, err := player.Complete(t.Context(), schemaflux.Request{User: "different"}); err == nil {
		t.Fatal("a strict replay answered a request it does not hold")
	}
	player.Lenient()
	if player.Remaining() != 2 {
		t.Fatalf("Remaining = %d", player.Remaining())
	}
	for i, want := range []string{"one", "two"} {
		resp, err := player.Complete(t.Context(), schemaflux.Request{User: "whatever", Model: "m"})
		if err != nil || resp.Content != want {
			t.Fatalf("call %d: %q %v", i+1, resp.Content, err)
		}
		if got := player.Remaining(); got != 1-i {
			t.Errorf("after call %d Remaining = %d", i+1, got)
		}
	}
	if _, err := player.Complete(t.Context(), schemaflux.Request{}); err == nil || !strings.Contains(err.Error(), "all 2 exchanges were played") {
		t.Errorf("a lenient replay past its end: %v", err)
	}
}

func TestAReplayHonoursAContextThatIsDone(t *testing.T) {
	t.Parallel()
	player := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{Response: &schemafluxtest.CassetteResponse{Content: "x"}})
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := player.Complete(cancelled, schemaflux.Request{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := player.Complete(expired, schemaflux.Request{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if player.Remaining() != 1 {
		t.Errorf("a context that was done used an exchange")
	}
	if resp, err := player.Complete(t.Context(), schemaflux.Request{}); err != nil || resp.Content != "x" {
		t.Errorf("the exchange was not there for the call that followed: %q %v", resp.Content, err)
	}
}

func TestAnExchangeWithNeitherAnOutcomeNorBothIsRefusedWhenPlayed(t *testing.T) {
	t.Parallel()
	cases := map[string]schemafluxtest.Cassette{
		"neither": {},
		"both":    {Response: &schemafluxtest.CassetteResponse{}, Failure: &schemafluxtest.CassetteFailure{Kind: schemaflux.ErrTimeout}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := schemafluxtest.ReplayFrom(c).Complete(t.Context(), schemaflux.Request{})
			if err == nil || cassetteKind(t, err) != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), "exchange 1") {
				t.Errorf("err = %v", err)
			}
		})
	}
	// A failure with no kind is what the library takes it for.
	_, err := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{Failure: &schemafluxtest.CassetteFailure{}}).Complete(t.Context(), schemaflux.Request{})
	if cassetteKind(t, err) != schemaflux.ErrUnavailable {
		t.Errorf("a failure of no kind replayed as %v", cassetteKind(t, err))
	}
}

func TestReplayFromAndRecordedHoldCopies(t *testing.T) {
	t.Parallel()
	original := schemafluxtest.Cassette{
		Provider: "p",
		Request: schemafluxtest.CassetteRequest{User: "u", Tools: []schemaflux.Tool{{Name: "a"}}, Decision: &schemaflux.Decision{
			Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionChoice, Options: []string{"x", "y"}}},
		}},
		Response: &schemafluxtest.CassetteResponse{Content: "c", Answers: []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, Yes: true}}},
	}
	player := schemafluxtest.ReplayFrom(original)
	original.Response.Content = "changed"
	original.Response.Answers[0].Yes = false
	original.Request.Decision.Questions[0].Options[0] = "changed"
	resp, err := player.Complete(t.Context(), schemaflux.Request{User: "u", Tools: []schemaflux.Tool{{Name: "a"}}, Decision: &schemaflux.Decision{
		Questions: []schemaflux.DecisionQuestion{{Kind: schemaflux.DecisionChoice, Options: []string{"x", "y"}}},
	}})
	if err != nil || resp.Content != "c" || !resp.Answers[0].Yes {
		t.Errorf("a cassette changed after ReplayFrom changed the replay: %+v %v", resp, err)
	}
	resp.Answers[0].Yes = false
	again := schemafluxtest.ReplayFrom(schemafluxtest.Cassette{Response: &schemafluxtest.CassetteResponse{Answers: []schemaflux.DecisionAnswer{{Yes: true}}}})
	first, _ := again.Complete(t.Context(), schemaflux.Request{})
	first.Answers[0].Yes = false
	if got := again.Remaining(); got != 0 {
		t.Errorf("Remaining = %d", got)
	}

	var inner schemafluxtest.Provider
	inner.Reply("x")
	tape := schemafluxtest.Record(&inner, t.TempDir())
	_, _ = tape.Complete(t.Context(), cassetteRequest("u"))
	got := tape.Recorded()
	got[0].Response.Content = "tampered"
	got = append(got, schemafluxtest.Cassette{})
	if again := tape.Recorded(); len(again) != 1 || again[0].Response.Content != "x" {
		t.Errorf("a change to what Recorded returned changed the tape: %+v", again)
	}
}

func TestAPlayerUsedFromManyGoroutinesAnswersEachRequestItsOwnExchangeOnce(t *testing.T) {
	t.Parallel()
	const n = 64
	var cassettes []schemafluxtest.Cassette
	for i := range n {
		cassettes = append(cassettes, schemafluxtest.Cassette{
			Request:  schemafluxtest.CassetteRequest{User: fmt.Sprintf("ticket %d", i)},
			Response: &schemafluxtest.CassetteResponse{Content: fmt.Sprintf("answer %d", i)},
		})
	}
	// Reverse order: the answers must follow the request, not the arrival.
	slices.Reverse(cassettes)
	player := schemafluxtest.ReplayFrom(cassettes...)

	var wg sync.WaitGroup
	results := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := player.Complete(t.Context(), schemaflux.Request{User: fmt.Sprintf("ticket %d", i)})
			if err != nil {
				results[i] = "error: " + err.Error()
				return
			}
			results[i] = resp.Content
		}()
	}
	wg.Wait()
	for i, got := range results {
		if want := fmt.Sprintf("answer %d", i); got != want {
			t.Errorf("request %d got %q, want %q", i, got, want)
		}
	}
	if player.Remaining() != 0 {
		t.Errorf("Remaining = %d", player.Remaining())
	}
}

func TestIdenticalRequestsFromManyGoroutinesEachTakeADifferentExchange(t *testing.T) {
	t.Parallel()
	const n = 64
	var cassettes []schemafluxtest.Cassette
	for i := range n {
		cassettes = append(cassettes, schemafluxtest.Cassette{Response: &schemafluxtest.CassetteResponse{Content: fmt.Sprint(i)}})
	}
	player := schemafluxtest.ReplayFrom(cassettes...)
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range n + 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := player.Complete(t.Context(), schemaflux.Request{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				seen["error"]++
				return
			}
			seen[resp.Content]++
		}()
	}
	wg.Wait()
	if seen["error"] != 8 {
		t.Errorf("%d requests were refused, want the 8 that came after the exchanges ran out", seen["error"])
	}
	for i := range n {
		if seen[fmt.Sprint(i)] != 1 {
			t.Errorf("exchange %d was played %d times", i, seen[fmt.Sprint(i)])
		}
	}
}

func TestATapeUsedFromManyGoroutinesKeepsEveryExchange(t *testing.T) {
	t.Parallel()
	var inner schemafluxtest.Provider
	inner.Reply("x")
	tape := schemafluxtest.Record(&inner, t.TempDir())
	const n = 100
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = tape.Complete(t.Context(), cassetteRequest(fmt.Sprint(i)))
			_ = tape.Recorded()
		}()
	}
	wg.Wait()
	recorded := tape.Recorded()
	if len(recorded) != n {
		t.Fatalf("recorded %d of %d", len(recorded), n)
	}
	users := map[string]bool{}
	for _, c := range recorded {
		users[c.Request.User] = true
	}
	if len(users) != n {
		t.Errorf("%d distinct exchanges of %d", len(users), n)
	}
}

func TestATapeIsTransparentToTheLibrarysLookupOfWhatItWraps(t *testing.T) {
	t.Parallel()
	var inner schemafluxtest.Provider
	inner.SetName("inner")
	tape := schemafluxtest.Record(&inner, t.TempDir())
	if tape.Name() != "inner" {
		t.Errorf("Name = %q, want the wrapped provider's", tape.Name())
	}
	var wrapper provider.Unwrapper = tape
	if wrapper.Unwrap() != schemaflux.Provider(&inner) {
		t.Errorf("Unwrap did not return the wrapped provider")
	}
	// The retry settings and the model of a tier are found under the tape, since it has
	// neither: a recording does not change how the library calls the provider.
	if _, hasOwn := any(tape).(provider.Retrier); hasOwn {
		t.Error("the tape has retry settings of its own and hides the provider's")
	}
	if _, hasOwn := any(tape).(provider.Decider); hasOwn {
		t.Error("the tape declares decision limits of its own, which would make every provider under it a decision provider")
	}
	found := false
	for p := schemaflux.Provider(tape); p != nil; {
		if retrier, ok := p.(provider.Retrier); ok {
			retries, _ := retrier.RetryPolicy()
			found = retries < 0
			break
		}
		unwrapper, ok := p.(provider.Unwrapper)
		if !ok {
			break
		}
		p = unwrapper.Unwrap()
	}
	if !found {
		t.Error("the wrapped provider's retry settings are not reachable through the tape")
	}

	empty := schemafluxtest.Record(nil, t.TempDir())
	if _, err := empty.Complete(t.Context(), schemaflux.Request{}); err == nil || cassetteKind(t, err) != schemaflux.ErrConfiguration {
		t.Errorf("a tape with no provider: %v", err)
	}
	if empty.Name() == "" {
		t.Error("a tape with no provider has no name")
	}
}

func TestATapeRecordsAStreamWhenItCompletesAndAFailureWhenItBreaks(t *testing.T) {
	t.Parallel()
	collect := func(seq iter.Seq2[schemaflux.Chunk, error]) (string, error) {
		var text strings.Builder
		for chunk, err := range seq {
			if err != nil {
				return text.String(), err
			}
			text.WriteString(chunk.Delta)
		}
		return text.String(), nil
	}

	var inner schemafluxtest.Provider
	inner.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{Content: "streamed answer", Usage: schemaflux.Usage{Input: 3, Output: 2, Total: 5}}, nil
	})
	tape := schemafluxtest.Record(&inner, t.TempDir())
	text, err := collect(tape.Stream(t.Context(), cassetteRequest("s")))
	if err != nil || text != "streamed answer" {
		t.Fatalf("stream = %q, %v", text, err)
	}
	recorded := tape.Recorded()
	if len(recorded) != 1 || recorded[0].Response == nil || recorded[0].Response.Content != "streamed answer" || recorded[0].Response.Usage.Total != 5 {
		t.Fatalf("recorded %+v", recorded)
	}

	// The recorded exchange plays as a stream too, and as a whole answer.
	player := schemafluxtest.ReplayFrom(recorded...)
	if text, err = collect(player.Stream(t.Context(), cassetteRequest("s"))); err != nil || text != "streamed answer" {
		t.Errorf("replayed stream = %q, %v", text, err)
	}

	// A stream the caller leaves early is not an exchange.
	abandoned := schemafluxtest.Record(&inner, t.TempDir())
	for range abandoned.Stream(t.Context(), cassetteRequest("s")) {
		break
	}
	if got := abandoned.Recorded(); len(got) != 0 {
		t.Errorf("an abandoned stream was recorded: %+v", got)
	}

	// A stream that breaks is a failure.
	var broken schemafluxtest.Provider
	broken.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable})
	failing := schemafluxtest.Record(&broken, t.TempDir())
	if _, err := collect(failing.Stream(t.Context(), cassetteRequest("s"))); !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Errorf("the stream's error = %v", err)
	}
	if got := failing.Recorded(); len(got) != 1 || got[0].Failure == nil || got[0].Failure.Kind != schemaflux.ErrUnavailable {
		t.Errorf("a broken stream recorded %+v", got)
	}
	player = schemafluxtest.ReplayFrom(failing.Recorded()...)
	if _, err := collect(player.Stream(t.Context(), cassetteRequest("s"))); !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Errorf("a recorded failure streamed as %v", err)
	}

	// A provider that cannot stream is reported as one that cannot.
	plain := schemafluxtest.Record(cassetteNoStream{}, t.TempDir())
	if _, err := collect(plain.Stream(t.Context(), cassetteRequest("s"))); !errors.Is(err, schemaflux.ErrUnsupported) {
		t.Errorf("a tape over a provider that cannot stream: %v", err)
	}
}

// cassetteNoStream is a provider with Complete and Name and nothing else.
type cassetteNoStream struct{}

func (cassetteNoStream) Name() string { return "plain" }
func (cassetteNoStream) Complete(context.Context, schemaflux.Request) (schemaflux.Response, error) {
	return schemaflux.Response{Content: "x"}, nil
}

func TestAPlayerIsNamedAndModelledAfterItsRecording(t *testing.T) {
	t.Parallel()
	exchange := func(provider, model string) schemafluxtest.Cassette {
		return schemafluxtest.Cassette{Provider: provider, Request: schemafluxtest.CassetteRequest{Model: model}, Response: &schemafluxtest.CassetteResponse{}}
	}
	cases := []struct {
		name      string
		cassettes []schemafluxtest.Cassette
		wantName  string
		wantModel string
	}{
		{"none", nil, "replay", ""},
		{"one", []schemafluxtest.Cassette{exchange("openai", "gpt")}, "openai", "gpt"},
		{"the same twice", []schemafluxtest.Cassette{exchange("openai", "gpt"), exchange("openai", "gpt")}, "openai", "gpt"},
		{"two providers", []schemafluxtest.Cassette{exchange("openai", "gpt"), exchange("anthropic", "gpt")}, "replay", "gpt"},
		{"two models", []schemafluxtest.Cassette{exchange("openai", "a"), exchange("openai", "b")}, "openai", ""},
		{"no model named", []schemafluxtest.Cassette{exchange("openai", "")}, "openai", ""},
		{"no provider named", []schemafluxtest.Cassette{exchange("", "gpt")}, "replay", "gpt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			player := schemafluxtest.ReplayFrom(tc.cassettes...)
			if player.Name() != tc.wantName {
				t.Errorf("Name = %q, want %q", player.Name(), tc.wantName)
			}
			for _, tier := range []schemaflux.Tier{schemaflux.TierSmart, schemaflux.TierFast, schemaflux.TierQuick} {
				if got := player.ModelFor(tier); got != tc.wantModel {
					t.Errorf("ModelFor(%v) = %q, want %q", tier, got, tc.wantModel)
				}
			}
			if retries, _ := player.RetryPolicy(); retries >= 0 {
				t.Errorf("RetryPolicy retries = %d: a player that retries would answer a recorded failure with the next exchange", retries)
			}
		})
	}
}

func TestACallOfTheLibraryIsRecordedOverABuiltInProviderAndReplayedWithNoProviderAtAll(t *testing.T) {
	t.Parallel()
	// The built-in provider is the OpenAI-compatible one, pointed at a server on
	// the loopback interface that answers every request with the same answer and
	// counts the requests it received.
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"r","status":"completed","model":"gpt-test","output":[{"type":"message","content":[`+
			`{"type":"output_text","text":"{\"q1\":true}"}]}],"usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}`)
	}))
	t.Cleanup(server.Close)
	local, err := provider.New("openai", provider.Config{APIKey: "sk-test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "openai")
	tape := schemafluxtest.Record(local, dir)

	// No model is named: the library finds the built-in provider's model for
	// the tier through the tape, which wraps it and hides nothing.
	ctx := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: tape})
	live, err := ops.Is(ctx, "a message about a duplicate charge", "Is it about billing?", types.OpOptions{})
	if err != nil {
		t.Fatalf("the call through the tape: %v", err)
	}
	recorded := tape.Recorded()
	if len(recorded) != 1 || recorded[0].Provider != "openai" || recorded[0].Request.Model == "" || recorded[0].Response == nil {
		t.Fatalf("recorded %+v", recorded)
	}
	if !live || requests.Load() != 1 {
		t.Fatalf("the call answered %v after %d requests to the server, want true after 1", live, requests.Load())
	}
	if recorded[0].Request.System == "" || !strings.Contains(recorded[0].Request.User, "duplicate charge") {
		t.Errorf("the exchange does not hold the prompts: %+v", recorded[0].Request)
	}

	player := cassetteReplayOf(t, tape, dir)
	again := schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: player})
	replayed, err := ops.Is(again, "a message about a duplicate charge", "Is it about billing?", types.OpOptions{})
	if err != nil || replayed != live {
		t.Fatalf("the replayed call: %v %v, recorded as %v", replayed, err, live)
	}
	if player.Remaining() != 0 {
		t.Errorf("Remaining = %d", player.Remaining())
	}
	server.Close()
	if requests.Load() != 1 {
		t.Errorf("the replay reached the server: %d requests in all, want 1", requests.Load())
	}

	// The same call about something else is not the call that was recorded.
	other := cassetteReplayOf(t, tape, dir)
	_, err = ops.Is(schemaflux.WithClient(t.Context(), schemaflux.Client{Provider: other}), "a different message", "Is it about billing?", types.OpOptions{})
	if err == nil || !errors.Is(err, schemaflux.ErrConfiguration) || !strings.Contains(err.Error(), "User") {
		t.Errorf("a call that was not recorded: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "a different message") {
		t.Errorf("the error prints the message: %v", err)
	}
}
