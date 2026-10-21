package schemafluxtest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// conversationTurns are the two requests of a conversation with one tool call, as
// a chat session sends them: the question, and then the question again with the
// model's call and the tool's result after it.
func conversationTurns() (first, second schemaflux.Request) {
	first = schemaflux.Request{
		Model: "model-a", System: "Answer questions about the weather.", Format: "text", MaxTokens: 400,
		Tools:    weatherTools(),
		Messages: askedForWeather()[:1],
	}
	second = first
	second.Messages = askedForWeather()
	return first, second
}

// scriptedConversation answers the first turn with a call and the second with
// words.
func scriptedConversation(p *schemafluxtest.Provider) {
	p.Respond(func(call int, req schemaflux.Request) (schemaflux.Response, error) {
		if call == 0 {
			return schemaflux.Response{
				FinishReason: "tool_calls", Model: "model-a-2026", Usage: schemaflux.Usage{Input: 30, Output: 9, Total: 39},
				ToolCalls: []schemaflux.ToolCall{{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}},
			}, nil
		}
		return schemaflux.Response{Content: "It is 8 degrees in Oslo.", FinishReason: "stop", Model: "model-a-2026", Usage: schemaflux.Usage{Input: 52, Output: 8, Total: 60}}, nil
	})
}

func TestATwoTurnConversationWithOneToolCallRoundTripsThroughSaveAndReplay(t *testing.T) {
	t.Parallel()
	tape, _, dir := cassetteTape(t, scriptedConversation)
	first, second := conversationTurns()
	liveFirst, err := tape.Complete(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	liveSecond, err := tape.Complete(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if len(liveFirst.ToolCalls) != 1 || liveSecond.Content == "" {
		t.Fatalf("the scripted conversation did not run: %+v %+v", liveFirst, liveSecond)
	}

	recorded := tape.Recorded()
	if len(recorded) != 2 {
		t.Fatalf("%d exchanges were recorded", len(recorded))
	}
	if got := recorded[0].Request; !reflect.DeepEqual(got.Tools, first.Tools) || !reflect.DeepEqual(got.Messages, first.Messages) {
		t.Errorf("the first request was kept as %+v", got)
	}
	if got := recorded[1].Request.Messages; !reflect.DeepEqual(got, second.Messages) {
		t.Errorf("the second request's conversation was kept as %+v", got)
	}
	if calls := recorded[0].Response.ToolCalls; len(calls) != 1 || calls[0] != (schemafluxtest.CassetteToolCall{ID: "call_1", Name: "weather", Arguments: `{"city":"Oslo"}`}) {
		t.Errorf("the reply's calls were kept as %+v", calls)
	}

	player := cassetteReplayOf(t, tape, dir)
	gotFirst, err := player.Complete(t.Context(), first)
	if err != nil {
		t.Fatalf("the first turn did not replay: %v", err)
	}
	gotSecond, err := player.Complete(t.Context(), second)
	if err != nil {
		t.Fatalf("the second turn did not replay: %v", err)
	}
	for _, c := range []struct{ got, want schemaflux.Response }{{gotFirst, liveFirst}, {gotSecond, liveSecond}} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("a replayed answer differs:\n got %+v\nwant %+v", c.got, c.want)
		}
	}
	if player.Remaining() != 0 {
		t.Errorf("%d exchanges were not played", player.Remaining())
	}

	// The same through memory, with the turns asked in the other order: each
	// request finds the exchange it matches, not the next one in the list.
	memory := schemafluxtest.ReplayFrom(recorded...)
	again, err := memory.Complete(t.Context(), second)
	if err != nil || again.Content != liveSecond.Content {
		t.Errorf("the second turn first: %+v, %v", again, err)
	}
	if again, err = memory.Complete(t.Context(), first); err != nil || len(again.ToolCalls) != 1 {
		t.Errorf("the first turn second: %+v, %v", again, err)
	}
}

func TestAPlayerDeliversTheCallsOfAReplyInTheLastChunkOfAStream(t *testing.T) {
	t.Parallel()
	tape, _, _ := cassetteTape(t, scriptedConversation)
	first, _ := conversationTurns()
	if _, err := tape.Complete(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	var last schemaflux.Chunk
	for chunk, err := range schemafluxtest.ReplayFrom(tape.Recorded()...).Stream(t.Context(), first) {
		if err != nil {
			t.Fatal(err)
		}
		last = chunk
	}
	if !last.Done || len(last.Response.ToolCalls) != 1 || last.Response.ToolCalls[0].ID != "call_1" {
		t.Errorf("the last chunk = %+v", last)
	}
}

// A cassette is a copy of the conversation: a tool's arguments and results are in
// the file in clear text, and a person who commits it has to read it.
func TestAFileHoldsTheToolDeclarationsTheCallsAndTheResultsInFull(t *testing.T) {
	t.Parallel()
	tape, _, dir := cassetteTape(t, scriptedConversation)
	_, second := conversationTurns()
	second.Messages[2].Content = "result-canary: 8 degrees"
	if _, err := tape.Complete(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err := tape.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "000.json"))
	if err != nil {
		t.Fatal(err)
	}
	file := string(raw)
	for _, want := range []string{
		`"tools": [`, `"name": "weather"`, `"description": "the weather in a city"`, `"parameters": {`, `"city": {`,
		`"messages": [`, `"role": "assistant"`, `"tool_calls": [`, `"id": "call_1"`, `"arguments": "{\"city\":\"Oslo\"}"`,
		`"role": "tool"`, `"tool_call_id": "call_1"`, `"content": "result-canary: 8 degrees"`,
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the file does not hold %s:\n%s", want, file)
		}
	}
	// And the file is the same bytes the next time.
	if err := tape.Save(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(filepath.Join(dir, "000.json")); string(again) != file {
		t.Error("saving the same exchange again wrote other bytes")
	}
}

// The shapes of credentials are replaced in what a conversation holds as they are
// in a prompt, and a request that held one still matches the file saved from it.
func TestCredentialShapesInAConversationAreReplacedAtSaveAndStillMatch(t *testing.T) {
	t.Parallel()
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789" // secret-scan: allow
	const github = "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"   // secret-scan: allow
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) {
		p.Respond(func(int, schemaflux.Request) (schemaflux.Response, error) {
			return schemaflux.Response{ToolCalls: []schemaflux.ToolCall{{ID: "call_2", Name: "weather", Arguments: json.RawMessage(`{"token":"` + github + `"}`)}}}, nil
		})
	})
	_, second := conversationTurns()
	second.Messages = append([]schemaflux.Message(nil), second.Messages...)
	second.Messages[0].Content = "my key is " + key
	second.Messages[1] = schemaflux.Message{Role: "assistant", ToolCalls: []schemaflux.ToolCall{{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"auth":"` + key + `"}`)}}}
	second.Messages[2].Content = "the vendor said " + github
	second.Tools = weatherTools()
	second.Tools[0].Description = "uses " + key
	second.Tools[0].Parameters = map[string]any{"type": "object", "default": key, "enum": []any{github, "b"}}
	if _, err := tape.Complete(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	player := cassetteReplayOf(t, tape, dir)
	for _, name := range []string{"000.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), key) || strings.Contains(string(raw), github) {
			t.Errorf("a credential reached the file:\n%s", raw)
		}
		if strings.Count(string(raw), "[redacted by schemafluxtest]") < 6 {
			t.Errorf("fewer places were replaced than held a credential:\n%s", raw)
		}
	}
	resp, err := player.Complete(t.Context(), second)
	if err != nil {
		t.Fatalf("a request that held a credential no longer matches the file saved from it: %v", err)
	}
	if len(resp.ToolCalls) != 1 || strings.Contains(string(resp.ToolCalls[0].Arguments), github) {
		t.Errorf("the replayed call = %+v: the credential in a reply's arguments was kept", resp.ToolCalls)
	}
}

func TestAReplayDoesNotAnswerAConversationThatDiffersInWhatItWasToldOrWhatWasDeclared(t *testing.T) {
	t.Parallel()
	tape, _, _ := cassetteTape(t, scriptedConversation)
	_, second := conversationTurns()
	if _, err := tape.Complete(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	recorded := tape.Recorded()

	clone := func() schemaflux.Request {
		_, r := conversationTurns()
		return r
	}
	cases := []struct {
		name   string
		edit   func(*schemaflux.Request)
		differ string
	}{
		{"a tool result", func(r *schemaflux.Request) { r.Messages[2].Content = "9 degrees" }, "Messages"},
		{"the id a result answers", func(r *schemaflux.Request) { r.Messages[2].ToolCallID = "call_2" }, "Messages"},
		{"the arguments of an earlier call", func(r *schemaflux.Request) { r.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"city":"Rome"}`) }, "Messages"},
		{"the name of an earlier call", func(r *schemaflux.Request) { r.Messages[1].ToolCalls[0].Name = "clock" }, "Messages"},
		{"the id of an earlier call", func(r *schemaflux.Request) { r.Messages[1].ToolCalls[0].ID = "call_9" }, "Messages"},
		{"a role", func(r *schemaflux.Request) { r.Messages[0].Role = "assistant" }, "Messages"},
		{"the question", func(r *schemaflux.Request) { r.Messages[0].Content = "How cold is Oslo?" }, "Messages"},
		{"a message too few", func(r *schemaflux.Request) { r.Messages = r.Messages[:2] }, "Messages"},
		{"a message too many", func(r *schemaflux.Request) {
			r.Messages = append(r.Messages, schemaflux.Message{Role: "user", Content: "and Rome?"})
		}, "Messages"},
		{"no conversation", func(r *schemaflux.Request) { r.Messages = nil }, "Messages"},
		{"a call that was dropped", func(r *schemaflux.Request) { r.Messages[1].ToolCalls = nil }, "Messages"},
		{"a tool's description", func(r *schemaflux.Request) { r.Tools[0].Description = "another" }, "Tools"},
		{"a tool's name", func(r *schemaflux.Request) { r.Tools[1].Name = "time" }, "Tools"},
		{"a tool's parameters", func(r *schemaflux.Request) { r.Tools[0].Parameters = map[string]any{"type": "object"} }, "Tools"},
		{"a tool's parameters dropped", func(r *schemaflux.Request) { r.Tools[0].Parameters = nil }, "Tools"},
		{"a tool too few", func(r *schemaflux.Request) { r.Tools = r.Tools[:1] }, "Tools"},
		{"the tools in another order", func(r *schemaflux.Request) { r.Tools[0], r.Tools[1] = r.Tools[1], r.Tools[0] }, "Tools"},
		{"no tools", func(r *schemaflux.Request) { r.Tools = nil }, "Tools"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			varied := clone()
			c.edit(&varied)
			_, err := schemafluxtest.ReplayFrom(recorded...).Complete(t.Context(), varied)
			if err == nil {
				t.Fatal("a conversation that differs was answered")
			}
			if cassetteKind(t, err) != schemaflux.ErrConfiguration || !strings.Contains(err.Error(), "differs in "+c.differ) {
				t.Errorf("the refusal does not name %s: %v", c.differ, err)
			}
			if strings.Contains(err.Error(), "How warm is Oslo") || strings.Contains(err.Error(), "8 degrees") {
				t.Errorf("the refusal quotes the conversation: %v", err)
			}
		})
	}
	// The conversation itself is answered, with a schema whose numbers a file
	// would read back as floats, and with an empty list in place of none.
	if _, err := schemafluxtest.ReplayFrom(recorded...).Complete(t.Context(), clone()); err != nil {
		t.Errorf("the same conversation was refused: %v", err)
	}
}

func TestAnEmptyListOfToolsOrMessagesMatchesNone(t *testing.T) {
	t.Parallel()
	var inner schemafluxtest.Provider
	tape := schemafluxtest.Record(&inner, t.TempDir())
	req := schemaflux.Request{User: "u", Tools: []schemaflux.Tool{}, Messages: []schemaflux.Message{}}
	if _, err := tape.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := schemafluxtest.ReplayFrom(tape.Recorded()...).Complete(t.Context(), schemaflux.Request{User: "u"}); err != nil {
		t.Errorf("an empty list did not match none: %v", err)
	}
}

func TestSchemaNumbersOfAToolSurviveTheFileAndStillMatch(t *testing.T) {
	t.Parallel()
	tape, _, dir := cassetteTape(t, func(p *schemafluxtest.Provider) { p.Reply("ok") })
	req := schemaflux.Request{
		User: "u",
		Tools: []schemaflux.Tool{{Name: "n", Parameters: map[string]any{
			"type": "object", "minimum": 1, "ratio": 0.5, "big": int64(1) << 40,
			"properties": map[string]any{"a": map[string]any{"type": "integer", "maximum": 10}},
			"required":   []string{"a"},
			"enum":       []int{1, 2, 3},
			"nullable":   nil,
		}}},
	}
	if _, err := tape.Complete(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	player := cassetteReplayOf(t, tape, dir)
	if _, err := player.Complete(t.Context(), req); err != nil {
		t.Fatalf("a schema with numbers did not match the file saved from it: %v", err)
	}
}

func TestWhatATapeRecordedIsACopyOfTheConversation(t *testing.T) {
	t.Parallel()
	tape, _, _ := cassetteTape(t, scriptedConversation)
	_, second := conversationTurns()
	if _, err := tape.Complete(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	// Editing the history the request was built from changes nothing kept.
	second.Messages[2].Content = "changed"
	second.Messages[1].ToolCalls[0].Arguments[3] = 'X'
	second.Tools[0].Name = "changed"
	_, want := conversationTurns()
	if got := tape.Recorded()[0].Request; !reflect.DeepEqual(got.Messages, want.Messages) || !reflect.DeepEqual(got.Tools, want.Tools) {
		t.Errorf("an edit of the request reached the tape: %+v", got)
	}
	// And so does editing what Recorded returned.
	read := tape.Recorded()
	read[0].Request.Messages[0].Content = "changed"
	read[0].Request.Messages[1].ToolCalls[0].Arguments[3] = 'Y'
	read[0].Request.Tools[1].Name = "changed"
	read[0].Response.ToolCalls[0].Name = "changed"
	if got := tape.Recorded()[0]; !reflect.DeepEqual(got.Request.Messages, want.Messages) || got.Request.Tools[1].Name != "clock" {
		t.Errorf("an edit of what Recorded returned reached the tape: %+v", got.Request)
	}
}

func TestAConversationCassetteWritesAsItsOwnMembersAndReadsBack(t *testing.T) {
	t.Parallel()
	c := schemafluxtest.Cassette{
		Provider: "p",
		Request: schemafluxtest.CassetteRequest{
			Tools:    []schemaflux.Tool{{Name: "lookup", Description: "find", Parameters: map[string]any{"type": "object"}}, {Name: "send"}},
			Messages: []schemaflux.Message{{Role: "user", Content: "q"}, {Role: "assistant", ToolCalls: []schemaflux.ToolCall{{ID: "c1", Name: "lookup", Arguments: json.RawMessage(`{"id":7}`)}}}, {Role: "tool", ToolCallID: "c1", Content: "r"}},
		},
		Response: &schemafluxtest.CassetteResponse{ToolCalls: []schemafluxtest.CassetteToolCall{
			{ID: "call_1", Name: "lookup", Arguments: `{"id":7}`},
			{Name: "send"},
		}},
	}
	encoded, err := c.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"tools":[{"name":"lookup","description":"find","parameters":{"type":"object"}},{"name":"send"}]`,
		`"messages":[{"role":"user","content":"q"},{"role":"assistant","tool_calls":[{"id":"c1","name":"lookup","arguments":"{\"id\":7}"}]},{"role":"tool","content":"r","tool_call_id":"c1"}]`,
		`"tool_calls":[{"id":"call_1","name":"lookup","arguments":"{\"id\":7}"},{"name":"send"}]`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("the JSON does not hold %s:\n%s", want, encoded)
		}
	}
	var back schemafluxtest.Cassette
	if err := back.UnmarshalJSON(encoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, c) {
		t.Errorf("round trip:\n%+v\n%+v", back, c)
	}
	alone, err := schemafluxtest.CassetteToolCall{ID: "i", Name: "n", Arguments: "{}"}.MarshalJSON()
	if err != nil || string(alone) != `{"id":"i","name":"n","arguments":"{}"}` {
		t.Errorf("a tool call alone: %s %v", alone, err)
	}
}

func TestACassetteFileOfTheOldCountsAndNamesIsRefusedAndASurplusMemberToo(t *testing.T) {
	t.Parallel()
	for name, document := range map[string]string{
		"a message count":               `{"version":1,"provider":"p","request":{"messages":3},"response":{"content":"c","usage":{}}}`,
		"a list of tool names":          `{"version":1,"provider":"p","request":{"tools":["lookup"]},"response":{"content":"c","usage":{}}}`,
		"an unknown member":             `{"version":1,"provider":"p","request":{"messages":[{"role":"user","extra":1}]},"response":{"content":"c","usage":{}}}`,
		"an unknown tool member":        `{"version":1,"provider":"p","request":{"tools":[{"name":"n","extra":1}]},"response":{"content":"c","usage":{}}}`,
		"a call that is no object":      `{"version":1,"provider":"p","request":{"messages":[{"role":"assistant","tool_calls":["x"]}]},"response":{"content":"c","usage":{}}}`,
		"parameters that are no object": `{"version":1,"provider":"p","request":{"tools":[{"name":"n","parameters":"x"}]},"response":{"content":"c","usage":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var c schemafluxtest.Cassette
			err := c.UnmarshalJSON([]byte(document))
			if err == nil {
				t.Fatalf("the document was read: %+v", c)
			}
			if strings.Contains(err.Error(), "lookup") || strings.Contains(err.Error(), "extra") {
				t.Errorf("the error quotes the document: %v", err)
			}
		})
	}
}
