package schemafluxtest_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// exampleDirectory is a directory for an example to record into, and what to call
// when it is done with it.
func exampleDirectory() (string, func()) {
	dir, err := os.MkdirTemp("", "schemafluxtest-example-")
	if err != nil {
		panic(err)
	}
	return filepath.Join(dir, "tickets"), func() { _ = os.RemoveAll(dir) }
}

// Record wraps the provider that is paid for, here a fake standing in for it. It
// answers as that provider does and keeps each exchange.
func ExampleRecord() {
	var live schemafluxtest.Provider
	live.Reply(`{"queue":"billing"}`)
	dir, done := exampleDirectory()
	defer done()

	tape := schemafluxtest.Record(&live, dir)
	resp, err := tape.Complete(context.Background(), schemaflux.Request{Model: "m", User: "I was charged twice"})
	fmt.Println(resp.Content, err, len(tape.Recorded()))
	// Output: {"queue":"billing"} <nil> 1
}

// Save writes the exchanges, one JSON file each, for the test that replays them
// and for the repository that keeps them. A cassette holds the prompts and the
// answers in clear text, so read the files before committing them.
func ExampleTape_Save() {
	var live schemafluxtest.Provider
	live.Reply("Refund issued.")
	dir, done := exampleDirectory()
	defer done()

	tape := schemafluxtest.Record(&live, dir)
	_, _ = tape.Complete(context.Background(), schemaflux.Request{Model: "m", User: "I was charged twice"})
	fmt.Println(tape.Save())

	files, _ := os.ReadDir(dir)
	for _, file := range files {
		fmt.Println(file.Name())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "000.json"))
	fmt.Println(len(data) > 0)
	// Output:
	// <nil>
	// 000.json
	// true
}

// Recorded returns the exchanges kept so far, without writing anything: a test
// that builds a replay in memory needs no directory.
func ExampleTape_Recorded() {
	var live schemafluxtest.Provider
	live.Fail(&schemaflux.Error{Kind: schemaflux.ErrRateLimited, Status: 429})
	tape := schemafluxtest.Record(&live, "unused")
	_, _ = tape.Complete(context.Background(), schemaflux.Request{User: "hello"})

	exchange := tape.Recorded()[0]
	fmt.Println(exchange.Provider, exchange.Request.User, exchange.Failure.Kind, exchange.Failure.Status)
	// Output: test hello rate limited 429
}

// Replay loads the directory Save wrote and answers from it, with no network
// and no key. A request it does not hold is an error of kind ErrConfiguration.
func ExampleReplay() {
	var live schemafluxtest.Provider
	live.Reply("Refund issued.")
	dir, done := exampleDirectory()
	defer done()
	tape := schemafluxtest.Record(&live, dir)
	ask := schemaflux.Request{Model: "m", User: "I was charged twice"}
	_, _ = tape.Complete(context.Background(), ask)
	_ = tape.Save()

	player, err := schemafluxtest.Replay(dir)
	if err != nil {
		fmt.Println(err)
		return
	}
	resp, _ := player.Complete(context.Background(), ask)
	fmt.Println(resp.Content)

	_, err = player.Complete(context.Background(), schemaflux.Request{Model: "m", User: "another question"})
	fmt.Println(errors.Is(err, schemaflux.ErrConfiguration))
	// Output:
	// Refund issued.
	// true
}

// ReplayFrom builds a Player from cassettes in memory, which is how a test
// scripts exactly what a provider said, a failure included, with no file.
func ExampleReplayFrom() {
	player := schemafluxtest.ReplayFrom(
		schemafluxtest.Cassette{
			Request:  schemafluxtest.CassetteRequest{User: "first"},
			Response: &schemafluxtest.CassetteResponse{Content: "one", FinishReason: "stop"},
		},
		schemafluxtest.Cassette{
			Request: schemafluxtest.CassetteRequest{User: "second"},
			Failure: &schemafluxtest.CassetteFailure{Kind: schemaflux.ErrTimeout, Status: 504},
		},
	)
	resp, _ := player.Complete(context.Background(), schemaflux.Request{User: "first"})
	fmt.Println(resp.Content, resp.FinishReason)
	_, err := player.Complete(context.Background(), schemaflux.Request{User: "second"})
	fmt.Println(errors.Is(err, schemaflux.ErrTimeout))
	// Output:
	// one stop
	// true
}

// Lenient answers in the order recorded, whatever is asked: for a test that cares
// about the answers and not about the questions.
func ExamplePlayer_Lenient() {
	player := schemafluxtest.ReplayFrom(
		schemafluxtest.Cassette{Request: schemafluxtest.CassetteRequest{User: "recorded"}, Response: &schemafluxtest.CassetteResponse{Content: "one"}},
	)
	_, err := player.Complete(context.Background(), schemaflux.Request{User: "asked"})
	fmt.Println(err != nil)

	player.Lenient()
	resp, err := player.Complete(context.Background(), schemaflux.Request{User: "asked"})
	fmt.Println(resp.Content, err)
	// Output:
	// true
	// one <nil>
}

// Remaining says how many exchanges have not been played, so that a test can
// assert it used all that it expected to.
func ExamplePlayer_Remaining() {
	player := schemafluxtest.ReplayFrom(
		schemafluxtest.Cassette{Response: &schemafluxtest.CassetteResponse{Content: "one"}},
		schemafluxtest.Cassette{Response: &schemafluxtest.CassetteResponse{Content: "two"}},
	)
	fmt.Println(player.Remaining())
	_, _ = player.Complete(context.Background(), schemaflux.Request{})
	fmt.Println(player.Remaining())
	// Output:
	// 2
	// 1
}
