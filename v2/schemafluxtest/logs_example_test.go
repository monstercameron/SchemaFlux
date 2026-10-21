package schemafluxtest_test

import (
	"fmt"
	"log/slog"

	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// A Logs is the Logger of a client whose lines a test wants to read, or to see
// there are none of. Here the logger is written to directly.
func ExampleLogs() {
	var logs schemafluxtest.Logs
	log := logs.Logger()
	log.Warn("retrying", "attempt", 2, "provider", "test")

	for _, entry := range logs.Entries() {
		fmt.Println(entry.Level, entry.Message, entry.NumAttrs())
	}
	// Output: WARN retrying 2
}

// Logger returns a *slog.Logger for the Logger field of a client.
func ExampleLogs_Logger() {
	var logs schemafluxtest.Logs
	logger := logs.Logger().With("run", 7).WithGroup("call")
	logger.Info("sent", "tokens", 42)

	entry := logs.Entries()[0]
	entry.Attrs(func(a slog.Attr) bool {
		fmt.Println(a.Key, a.Value)
		return true
	})
	// Output:
	// run 7
	// call [tokens=42]
}

// Entries returns the records kept, in order, as copies.
func ExampleLogs_Entries() {
	var logs schemafluxtest.Logs
	logs.Logger().Info("first")
	logs.Logger().Error("second")

	for _, entry := range logs.Entries() {
		fmt.Println(entry.Level, entry.Message)
	}
	// Output:
	// INFO first
	// ERROR second
}

// Reset forgets what was kept, for a test that logs in phases.
func ExampleLogs_Reset() {
	var logs schemafluxtest.Logs
	logs.Logger().Info("setup")
	logs.Reset()
	logs.Logger().Info("the call under test")

	fmt.Println(len(logs.Entries()), logs.Entries()[0].Message)
	// Output: 1 the call under test
}
