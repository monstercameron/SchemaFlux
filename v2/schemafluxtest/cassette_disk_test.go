package schemafluxtest_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// goldenExchanges records three exchanges that exercise every member of the file
// format, with nothing in them that changes from one run to another.
func goldenExchanges(t *testing.T, dir string) *schemafluxtest.Tape {
	t.Helper()
	var inner schemafluxtest.Provider
	inner.SetName("golden")
	inner.Respond(func(call int, req schemaflux.Request) (schemaflux.Response, error) {
		switch call {
		case 0:
			return schemaflux.Response{
				Content: "<p>Refund & reply</p>", Model: "gold-1", Provider: "gateway", Upstream: "gold-host", FinishReason: "stop",
				ServiceTier: "flex", Cached: true, Hedged: true,
				Usage: schemaflux.Usage{Input: 10, Output: 4, Total: 14, Cached: 2, CacheWrite: 1, Reasoning: 3},
			}, nil
		case 1:
			return schemaflux.Response{
				Model: "gold-1",
				Answers: []schemaflux.DecisionAnswer{
					{Kind: schemaflux.DecisionYesNo, Yes: true, ModelConfidence: 0.75, ConfidenceReported: true},
					{Kind: schemaflux.DecisionChoice, Label: "billing"},
					{Kind: schemaflux.DecisionScale, Level: 4},
				},
				Escalation: &schemaflux.DecisionEscalation{Provider: "jev", Model: "jev-1", Reason: "below the floor", ModelConfidence: 0.25, ConfidenceReported: true},
			}, nil
		}
		return schemaflux.Response{}, &schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429, RetryAfter: 1500 * time.Millisecond}
	})
	tape := schemafluxtest.Record(&inner, dir)
	requests := []schemaflux.Request{
		{
			Model: "gold-1", System: "Sort the ticket.", User: "I want a refund.", Temperature: 0.2, MaxTokens: 300, Format: "json",
			SchemaName: "Ticket", Schema: map[string]any{"type": "object"}, Effort: schemaflux.EffortLow, EffortPreferred: true,
		},
		{
			Model: "gold-1", WebSearch: true,
			Decision: &schemaflux.Decision{
				State: `"a ticket"`, Guidance: "strict",
				Questions: []schemaflux.DecisionQuestion{
					{Kind: schemaflux.DecisionYesNo, Ask: "Urgent?"},
					{Kind: schemaflux.DecisionChoice, Ask: "Queue?", Options: []string{"billing", "tech"}, Criteria: []string{"money", ""}},
					{Kind: schemaflux.DecisionScale, Ask: "Anger?", Low: 1, High: 5},
				},
			},
		},
		{Model: "gold-1", User: "again"},
	}
	for _, req := range requests {
		_, _ = tape.Complete(t.Context(), req)
	}
	return tape
}

func TestTheFileFormatIsStableByteForByte(t *testing.T) {
	t.Parallel()
	golden := filepath.Join("testdata", "cassettes", "golden")
	dir := filepath.Join(t.TempDir(), "tape")
	if err := goldenExchanges(t, dir).Save(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SCHEMAFLUXTEST_WRITE_GOLDEN") != "" {
		if err := os.MkdirAll(golden, 0o755); err != nil {
			t.Fatal(err)
		}
		saved, _ := os.ReadDir(dir)
		for _, entry := range saved {
			data, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err := os.WriteFile(filepath.Join(golden, entry.Name()), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	want, err := os.ReadDir(golden)
	if err != nil || len(want) != 3 {
		t.Fatalf("the golden directory: %v, %d files", err, len(want))
	}
	for _, entry := range want {
		wantBytes, err := os.ReadFile(filepath.Join(golden, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("Save wrote no %s: %v", entry.Name(), err)
		}
		if string(got) != string(wantBytes) {
			t.Errorf("%s changed. The format of a committed cassette must not change without a new version.\nwant:\n%s\ngot:\n%s", entry.Name(), wantBytes, got)
		}
	}
}

func TestACommittedCassetteOfTheFormatStillReplays(t *testing.T) {
	t.Parallel()
	player, err := schemafluxtest.Replay(filepath.Join("testdata", "cassettes", "golden"))
	if err != nil {
		t.Fatal(err)
	}
	if player.Name() != "golden" || player.ModelFor(schemaflux.TierFast) != "gold-1" || player.Remaining() != 3 {
		t.Errorf("name %q, model %q, remaining %d", player.Name(), player.ModelFor(schemaflux.TierFast), player.Remaining())
	}
	first, err := player.Complete(t.Context(), schemaflux.Request{
		Model: "gold-1", System: "Sort the ticket.", User: "I want a refund.", Temperature: 0.2, MaxTokens: 300, Format: "json",
		SchemaName: "Ticket", Schema: map[string]any{"type": "object"}, Effort: schemaflux.EffortLow, EffortPreferred: true,
	})
	if err != nil || first.Content != "<p>Refund & reply</p>" || first.Usage.Reasoning != 3 || !first.Cached || !first.Hedged || first.Upstream != "gold-host" {
		t.Errorf("the first exchange: %+v %v", first, err)
	}
	second, err := player.Complete(t.Context(), schemaflux.Request{
		Model: "gold-1", WebSearch: true,
		Decision: &schemaflux.Decision{
			State: `"a ticket"`, Guidance: "strict",
			Questions: []schemaflux.DecisionQuestion{
				{Kind: schemaflux.DecisionYesNo, Ask: "Urgent?"},
				{Kind: schemaflux.DecisionChoice, Ask: "Queue?", Options: []string{"billing", "tech"}, Criteria: []string{"money", ""}},
				{Kind: schemaflux.DecisionScale, Ask: "Anger?", Low: 1, High: 5},
			},
		},
	})
	if err != nil || len(second.Answers) != 3 || second.Answers[1].Label != "billing" || second.Escalation == nil || second.Escalation.Reason != "below the floor" {
		t.Errorf("the second exchange: %+v %v", second, err)
	}
	_, err = player.Complete(t.Context(), schemaflux.Request{Model: "gold-1", User: "again"})
	var e *schemaflux.Error
	if !errors.As(err, &e) || e.Kind != schemaflux.ErrRateLimited || e.Status != 429 || e.RetryAfter != 1500*time.Millisecond {
		t.Errorf("the third exchange: %v", err)
	}
}

func TestSaveRemovesStaleCassettesAndLeavesOtherFilesAlone(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "tape")
	save := func(n int) {
		var inner schemafluxtest.Provider
		tape := schemafluxtest.Record(&inner, dir)
		for i := range n {
			if _, err := tape.Complete(t.Context(), cassetteRequest(fmt.Sprint(i))); err != nil {
				t.Fatal(err)
			}
		}
		if err := tape.Save(); err != nil {
			t.Fatal(err)
		}
	}
	save(5)
	for _, name := range []string{"notes.txt", "README", "a.json", "1.json.bak"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "7.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	save(3)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"000.json", "001.json", "002.json", "1.json.bak", "7.json", "README", "a.json", "notes.txt"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("the directory holds %v, want %v", names, want)
	}
}

func TestSaveOfNothingWritesNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "never")
	tape := schemafluxtest.Record(&schemafluxtest.Provider{}, dir)
	if err := tape.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Save of nothing made %s: %v", dir, err)
	}
}

func TestSaveFailsWithAnErrorThatNamesWhatItCouldNotDo(t *testing.T) {
	t.Parallel()
	exchange := func(t *testing.T, dir string, answer func(*schemaflux.Response)) *schemafluxtest.Tape {
		var inner schemafluxtest.Provider
		inner.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			var resp schemaflux.Response
			if answer != nil {
				answer(&resp)
			}
			return resp, nil
		})
		tape := schemafluxtest.Record(&inner, dir)
		if _, err := tape.Complete(t.Context(), cassetteRequest("x")); err != nil {
			t.Fatal(err)
		}
		return tape
	}
	t.Run("the directory is a file", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := exchange(t, file, nil).Save()
		if err == nil || cassetteKind(t, err) != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), file) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no directory was given", func(t *testing.T) {
		t.Parallel()
		err := exchange(t, "", nil).Save()
		if err == nil || cassetteKind(t, err) != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), "no directory") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("the first file is blocked by a directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "000.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := exchange(t, dir, nil).Save()
		if err == nil || !strings.Contains(err.Error(), "000.json") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a number JSON cannot hold", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "nan")
		err := exchange(t, dir, func(r *schemaflux.Response) {
			r.Answers = []schemaflux.DecisionAnswer{{Kind: schemaflux.DecisionYesNo, ModelConfidence: math.NaN()}}
		}).Save()
		if err == nil || !strings.Contains(err.Error(), "exchange 1") {
			t.Errorf("err = %v", err)
		}
		if _, statErr := os.Stat(dir); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("a refused Save made the directory: %v", statErr)
		}
	})
}

func TestReplayOfADirectoryItCannotUseNamesTheDirectory(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	onlyText := t.TempDir()
	if err := os.WriteFile(filepath.Join(onlyText, "notes.txt"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(onlyText, "000.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	cases := []struct {
		name, dir string
		notExist  bool
	}{
		{"a missing directory", missing, true},
		{"an empty directory", empty, false},
		{"a directory of other files and a directory named like one", onlyText, false},
		{"a file", file, false},
		{"no path", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			player, err := schemafluxtest.Replay(tc.dir)
			if err == nil || player != nil {
				t.Fatalf("Replay returned %v, %v", player, err)
			}
			if cassetteKind(t, err) != schemaflux.ErrConfiguration {
				t.Errorf("kind = %v", cassetteKind(t, err))
			}
			if tc.dir != "" && !strings.Contains(err.Error(), tc.dir) {
				t.Errorf("the error does not name the directory: %v", err)
			}
			if tc.notExist && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("errors.Is(err, fs.ErrNotExist) is false: %v", err)
			}
		})
	}
}

func TestReplayRefusesACorruptFileAndNamesItWithoutQuotingIt(t *testing.T) {
	t.Parallel()
	const canary = "FILE-CONTENT-CANARY-9911"
	valid := `{"version":1,"provider":"p","request":{"user":"` + canary + `"},"response":{"content":"` + canary + `"}}`
	cases := []struct {
		name string
		data string
		why  string
	}{
		{"empty", ``, "empty or cut short"},
		{"white space", " \n\t ", "empty or cut short"},
		{"cut short", valid[:len(valid)/2], "cut short"},
		{"cut at the last brace", valid[:len(valid)-1], "cut short"},
		{"not JSON", `this is not json ` + canary, "not valid JSON"},
		{"an array", `[` + valid + `]`, "wrong type"},
		{"null", `null`, "no version"},
		{"a string", `"` + canary + `"`, "wrong type"},
		{"no version", `{"provider":"p","response":{"content":"` + canary + `"}}`, "no version"},
		{"another version", strings.Replace(valid, `"version":1`, `"version":2`, 1), "version 2"},
		{"a version of the wrong type", strings.Replace(valid, `"version":1`, `"version":"1"`, 1), "wrong type"},
		{"a member this version does not know", strings.Replace(valid, `"provider":"p"`, `"provider":"p","`+canary+`":1`, 1), "does not know"},
		{"a nested member this version does not know", strings.Replace(valid, `"content"`, `"`+canary+`":true,"content"`, 1), "does not know"},
		{"something after the cassette", valid + ` ` + valid, "after the cassette"},
		{"garbage after the cassette", valid + ` ` + canary, "after the cassette"},
		{"neither a response nor a failure", `{"version":1,"provider":"p","request":{"user":"` + canary + `"}}`, "neither a response nor a failure"},
		{"both", `{"version":1,"response":{"content":"` + canary + `"},"failure":{"kind":"timeout"}}`, "or both"},
		{"a kind that does not exist", `{"version":1,"failure":{"kind":"` + canary + `"}}`, "kind this version does not know"},
		{"the kind unknown", `{"version":1,"failure":{"kind":"unknown"}}`, "kind this version does not know"},
		{"a wait that is not a duration", `{"version":1,"failure":{"kind":"timeout","retry_after":"` + canary + `"}}`, "not a duration"},
		{"a negative wait", `{"version":1,"failure":{"kind":"timeout","retry_after":"-1s"}}`, "not a duration"},
		{"a decision kind that is nothing", `{"version":1,"request":{"decision":{"questions":[{"kind":"` + canary + `"}]}},"response":{"content":""}}`, "neither a name nor a number"},
		{"an answer kind that is nothing", `{"version":1,"response":{"content":"","answers":[{"kind":"` + canary + `"}]}}`, "neither a name nor a number"},
		{"a decision kind out of range", `{"version":1,"request":{"decision":{"questions":[{"kind":"300"}]}},"response":{"content":""}}`, "neither a name nor a number"},
		{"a number where text goes", `{"version":1,"response":{"content":7}}`, "wrong type"},
		{"binary", "\x00\x01\x02\xff\xfe" + canary, "not valid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "000.json"), []byte(valid), 0o644); err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(dir, "001.json")
			if err := os.WriteFile(bad, []byte(tc.data), 0o644); err != nil {
				t.Fatal(err)
			}
			player, err := schemafluxtest.Replay(dir)
			if err == nil || player != nil {
				t.Fatalf("Replay of a corrupt file returned %v, %v", player, err)
			}
			if cassetteKind(t, err) != schemaflux.ErrConfiguration {
				t.Errorf("kind = %v", cassetteKind(t, err))
			}
			if !strings.Contains(err.Error(), bad) {
				t.Errorf("the error does not name %s: %v", bad, err)
			}
			if !strings.Contains(err.Error(), tc.why) {
				t.Errorf("the error does not say %q: %v", tc.why, err)
			}
			var e *schemaflux.Error
			if errors.As(err, &e) && (strings.Contains(e.Detail(), canary) || strings.Contains(fmt.Sprint(e.Err), canary)) {
				t.Errorf("the error quotes the file: %v / %v", e.Detail(), e.Err)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("the error quotes the file: %v", err)
			}
		})
	}
}

func TestReplayPlaysFilesInTheOrderOfTheirNumbers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"100.json", "9.json", "010.json", "2.json", "000.json", "b.json", "a.json", "1000.json", "99.json"} {
		body := fmt.Sprintf(`{"version":1,"response":{"content":%q}}`, name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, ignored := range []string{"notes.txt", "7.json.bak"} {
		if err := os.WriteFile(filepath.Join(dir, ignored), []byte("not a cassette"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	player, err := schemafluxtest.Replay(dir)
	if err != nil {
		t.Fatal(err)
	}
	player.Lenient()
	var got []string
	for range 9 {
		resp, err := player.Complete(t.Context(), schemaflux.Request{})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, resp.Content)
	}
	want := []string{"000.json", "2.json", "9.json", "010.json", "99.json", "100.json", "1000.json", "a.json", "b.json"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("played in the order %v, want %v", got, want)
	}
	if player.Remaining() != 0 {
		t.Errorf("Remaining = %d", player.Remaining())
	}
}

func TestMoreThanAThousandExchangesReplayInOrder(t *testing.T) {
	t.Parallel()
	const n = 1005
	var inner schemafluxtest.Provider
	inner.Respond(func(call int, _ schemaflux.Request) (schemaflux.Response, error) {
		return schemaflux.Response{Content: fmt.Sprint(call)}, nil
	})
	dir := filepath.Join(t.TempDir(), "many")
	tape := schemafluxtest.Record(&inner, dir)
	for i := range n {
		if _, err := tape.Complete(t.Context(), schemaflux.Request{User: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	player := cassetteReplayOf(t, tape, dir)
	for i := range n {
		resp, err := player.Complete(t.Context(), schemaflux.Request{User: fmt.Sprint(i)})
		if err != nil || resp.Content != fmt.Sprint(i) {
			t.Fatalf("exchange %d: %q %v", i, resp.Content, err)
		}
	}
}

func TestACassetteIsItsOwnJSON(t *testing.T) {
	t.Parallel()
	c := schemafluxtest.Cassette{
		Provider: "p",
		Request:  schemafluxtest.CassetteRequest{Model: "m", User: "<u>", Decision: &schemaflux.Decision{}},
		Failure:  &schemafluxtest.CassetteFailure{Kind: schemaflux.ErrRateLimited, Status: 429, RetryAfter: 90 * time.Second},
	}
	encoded, err := c.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":1,"provider":"p","request":{"model":"m","user":"<u>","decision":{}},"failure":{"kind":"rate limited","status":429,"retry_after":"1m30s"}}`
	if string(encoded) != want {
		t.Errorf("json.Marshal wrote\n%s\nwant\n%s", encoded, want)
	}
	var back schemafluxtest.Cassette
	if err := json.Unmarshal(encoded, &back); err != nil || !reflect.DeepEqual(back, c) {
		t.Errorf("round trip: %+v %v", back, err)
	}
	// json.Marshal escapes markup in what a MarshalJSON returns, as it always
	// does, and says the same thing.
	viaMarshal, err := json.Marshal(c)
	if err != nil || json.Unmarshal(viaMarshal, &back) != nil || !reflect.DeepEqual(back, c) {
		t.Errorf("through json.Marshal: %s %v", viaMarshal, err)
	}
	for name, part := range map[string]any{
		"request":  c.Request,
		"failure":  *c.Failure,
		"response": schemafluxtest.CassetteResponse{Content: "x", Model: "m"},
	} {
		if _, err := json.Marshal(part); err != nil {
			t.Errorf("the %s does not marshal alone: %v", name, err)
		}
	}
	// A failure of no kind is written as the kind the library takes it for.
	encoded, _ = json.Marshal(schemafluxtest.CassetteFailure{})
	if string(encoded) != `{"kind":"unavailable"}` {
		t.Errorf("a failure of no kind is %s", encoded)
	}
}

// FuzzReadingACassetteFile hands the reader of a directory whatever the fuzzer
// makes up as a cassette file, and requires that it neither panics nor reads
// something it cannot write back the same.
func FuzzReadingACassetteFile(f *testing.F) {
	golden, _ := filepath.Glob(filepath.Join("testdata", "cassettes", "golden", "*.json"))
	for _, name := range golden {
		if data, err := os.ReadFile(name); err == nil {
			f.Add(data)
		}
	}
	for _, seed := range []string{
		``, `{`, `null`, `[]`, `{"version":1}`, `{"version":1,"response":{}}`, `{"version":1,"failure":{"kind":"timeout","retry_after":"1s"}}`,
		`{"version":1,"request":{"decision":{"questions":[{"kind":"7"}]}},"response":{"answers":[{"kind":"choice","label":"x"}]}}`,
		`{"version":1,"response":{"tool_calls":[{"name":"n"}]},"junk":1}`, "\x00\xff{",
	} {
		f.Add([]byte(seed))
	}
	dir := f.TempDir()
	file := filepath.Join(dir, "000.json")
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := os.WriteFile(file, data, 0o644); err != nil {
			t.Skip(err)
		}
		player, err := schemafluxtest.Replay(dir)
		if err != nil {
			if !strings.Contains(err.Error(), file) || player != nil {
				t.Fatalf("a refusal that does not name the file, or returns a player: %v", err)
			}
			return
		}
		var c schemafluxtest.Cassette
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatalf("Replay read what json.Unmarshal refuses: %v", err)
		}
		again, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("a cassette that was read cannot be written: %v", err)
		}
		var second schemafluxtest.Cassette
		if err := json.Unmarshal(again, &second); err != nil || !reflect.DeepEqual(second, c) {
			t.Fatalf("a cassette is not what it was after being written and read: %v\n%+v\n%+v", err, second, c)
		}
		// And playing it, whatever it holds, ends in an answer or an error.
		player.Lenient()
		_, _ = player.Complete(t.Context(), schemaflux.Request{})
	})
}
