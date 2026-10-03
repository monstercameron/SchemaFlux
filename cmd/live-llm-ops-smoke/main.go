package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/smokecases"
)

func main() {
	key := strings.TrimSpace(os.Getenv("SCHEMAFLUX_API_KEY"))
	if key == "" {
		fmt.Fprintln(os.Stderr, "SCHEMAFLUX_API_KEY is required")
		os.Exit(2)
	}
	os.Setenv("SCHEMAFLUX_PROVIDER", "openai")
	if os.Getenv("SCHEMAFLUX_TIMEOUT") == "" {
		os.Setenv("SCHEMAFLUX_TIMEOUT", "60s")
	}
	// Init's error used to be discarded here, so a key the client could not
	// use produced sixty-five failures that each looked like the operation's
	// fault instead of one line saying the client never came up.
	if err := schemaflux.Init(key); err != nil {
		fmt.Fprintln(os.Stderr, "could not initialise the client:", err)
		os.Exit(2)
	}

	// The cases live in internal/smokecases so that tests/wirecontract can run
	// the same list offline and check what each one sends.
	tests := smokecases.All()
	var failed []string
	startAll := time.Now()
	for i, tc := range tests {
		start := time.Now()
		err := tc.Run()
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", tc.Name, err))
			fmt.Printf("[%02d/%02d] FAIL %-24s %s\n", i+1, len(tests), tc.Name, err)
			continue
		}
		fmt.Printf("[%02d/%02d] PASS %-24s %s\n", i+1, len(tests), tc.Name, time.Since(start).Round(time.Millisecond))
	}
	fmt.Printf("\nCompleted %d LLM op smoke tests in %s\n", len(tests), time.Since(startAll).Round(time.Millisecond))
	if len(failed) == 0 {
		fmt.Println("All LLM ops passed.")
		return
	}
	fmt.Printf("%d failures:\n", len(failed))
	for _, f := range failed {
		fmt.Printf("- %s\n", f)
	}
	os.Exit(1)
}
