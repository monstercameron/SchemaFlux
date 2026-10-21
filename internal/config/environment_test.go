package config

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// setenvAndReload sets a variable for one test and makes the library read the
// environment again, now and when the test ends, because the settings are read
// once and kept (ReloadEnvironment). The reload at the end is registered first
// so that it runs after the variable has been put back.
func setenvAndReload(t *testing.T, name, value string) {
	t.Helper()
	t.Cleanup(ReloadEnvironment)
	t.Setenv(name, value)
	ReloadEnvironment()
}

func TestASettingIsReadOnceAndKept(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "4")
	if got := GetRepairAttempts(); got != 4 {
		t.Fatalf("GetRepairAttempts = %d, want 4", got)
	}
	t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", "9")
	for i := 0; i < 5; i++ {
		if got := GetRepairAttempts(); got != 4 {
			t.Fatalf("read %d: GetRepairAttempts = %d after the variable changed, want the 4 first read", i, got)
		}
	}
}

func TestReloadEnvironmentTakesTheNewValueOfEverySetting(t *testing.T) {
	cases := []struct {
		name     string
		variable string
		before   string
		after    string
		read     func() any
		want     [2]any
	}{
		{"debug", "SCHEMAFLUX_DEBUG", "false", "true", func() any { return GetDebugMode() }, [2]any{false, true}},
		{"metrics", "SCHEMAFLUX_METRICS", "false", "true", func() any { return IsMetricsEnabled() }, [2]any{false, true}},
		{"repair", "SCHEMAFLUX_REPAIR_ATTEMPTS", "2", "5", func() any { return GetRepairAttempts() }, [2]any{2, 5}},
		{"retries", "SCHEMAFLUX_LLM_MAX_RETRIES", "1", "6", func() any { return GetLLMMaxRetries() }, [2]any{1, 6}},
		{"backoff", "SCHEMAFLUX_LLM_RETRY_BACKOFF", "1s", "2s", func() any { return GetLLMRetryBackoff() }, [2]any{time.Second, 2 * time.Second}},
		{"timeout", "SCHEMAFLUX_TIMEOUT", "10s", "20s", func() any { return GetTimeout() }, [2]any{10 * time.Second, 20 * time.Second}},
		{"model", "SCHEMAFLUX_MODEL", "model-a", "model-b", func() any { return GetModel(types.Fast, "openai") }, [2]any{"model-a", "model-b"}},
		{"smart model", "SCHEMAFLUX_MODEL_SMART", "smart-a", "smart-b", func() any { return GetModel(types.Smart, "openai") }, [2]any{"smart-a", "smart-b"}},
		{"fast model", "SCHEMAFLUX_MODEL_FAST", "fast-a", "fast-b", func() any { return GetModel(types.Fast, "openai") }, [2]any{"fast-a", "fast-b"}},
		{"quick model", "SCHEMAFLUX_MODEL_QUICK", "quick-a", "quick-b", func() any { return GetModel(types.Quick, "openai") }, [2]any{"quick-a", "quick-b"}},
		{"reinforcement", PromptReinforcementVariable, "on", "off", func() any { return PromptReinforcementEnabled() }, [2]any{true, false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setenvAndReload(t, tc.variable, tc.before)
			if got := tc.read(); got != tc.want[0] {
				t.Fatalf("first read = %v, want %v", got, tc.want[0])
			}
			t.Setenv(tc.variable, tc.after)
			if got := tc.read(); got != tc.want[0] {
				t.Fatalf("read after the variable changed = %v, want the first value %v kept", got, tc.want[0])
			}
			ReloadEnvironment()
			if got := tc.read(); got != tc.want[1] {
				t.Errorf("read after ReloadEnvironment = %v, want %v", got, tc.want[1])
			}
		})
	}
}

func TestSettersAreSeenAtOnceWithoutAReload(t *testing.T) {
	t.Cleanup(func() { SetDebugMode(false); SetMetricsEnabled(false) })
	setenvAndReload(t, "SCHEMAFLUX_DEBUG", "false")
	setenvAndReload(t, "SCHEMAFLUX_METRICS", "false")
	if GetDebugMode() || IsMetricsEnabled() {
		t.Fatal("a switch is on with its variable off")
	}
	SetDebugMode(true)
	SetMetricsEnabled(true)
	if !GetDebugMode() || !IsMetricsEnabled() {
		t.Error("a setter did not take effect at once")
	}
	SetDebugMode(false)
	SetMetricsEnabled(false)
	if GetDebugMode() || IsMetricsEnabled() {
		t.Error("turning a setter off did not take effect at once")
	}
}

// The steady path of a call reads no variable: once a setting has been read,
// asking for it again is an atomic load. Reads are counted by the cells, so this
// holds on every platform; the allocation check beside it also catches a cache
// that was removed, but only where os.Getenv allocates, which is on Windows.
func TestAskingForASettingAgainReadsNothing(t *testing.T) {
	ReloadEnvironment()
	t.Cleanup(ReloadEnvironment)
	getters := map[string]func(){
		"debug":         func() { _ = GetDebugMode() },
		"metrics":       func() { _ = IsMetricsEnabled() },
		"model":         func() { _ = GetModel(types.Fast, "openai") },
		"repair":        func() { _ = GetRepairAttempts() },
		"retries":       func() { _ = GetLLMMaxRetries() },
		"backoff":       func() { _ = GetLLMRetryBackoff() },
		"timeout":       func() { _ = GetTimeout() },
		"reinforcement": func() { _ = PromptReinforcementEnabled() },
	}
	for name, get := range getters {
		t.Run(name, func(t *testing.T) {
			get() // the first read, which is allowed to allocate
			before := environmentReads.Load()
			if allocs := testing.AllocsPerRun(100, get); allocs != 0 {
				t.Errorf("%v allocations per call after the first, want 0", allocs)
			}
			if reads := environmentReads.Load() - before; reads != 0 {
				t.Errorf("%d reads of the environment in 101 calls after the first, want none", reads)
			}
		})
	}
}

// Fifty goroutines that ask at once for a setting nobody has read share one
// read of it: a malformed value is reported once, and all of them see one value.
func TestConcurrentFirstUseOfASettingReadsItOnce(t *testing.T) {
	var reports atomic.Int32
	t.Cleanup(SetEnvProblemReporter(func(EnvProblem) { reports.Add(1) }))
	setenvAndReload(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "many")

	var reads atomic.Int32
	cell := newEnvironmentCell[int]()
	read := func() int {
		reads.Add(1)
		return EnvInt("SCHEMAFLUX_REPAIR_ATTEMPTS", 1, 0)
	}

	start := make(chan struct{})
	results := make(chan int, 50)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- cell.get(read)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for value := range results {
		if value != 1 {
			t.Fatalf("a goroutine saw %d, want the default 1", value)
		}
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("the variable was read %d times by 50 goroutines, want 1", got)
	}
	if got := reports.Load(); got != 1 {
		t.Errorf("the malformed value was reported %d times, want once", got)
	}
}

func TestAMalformedSettingIsReportedByTheFirstReadThatNeedsIt(t *testing.T) {
	var problems []EnvProblem
	t.Cleanup(SetEnvProblemReporter(func(p EnvProblem) { problems = append(problems, p) }))
	setenvAndReload(t, "SCHEMAFLUX_TIMEOUT", "soon")

	// A call that only asks whether metrics are on does not read the timeout.
	_ = IsMetricsEnabled()
	if len(problems) != 0 {
		t.Fatalf("reported before the timeout was asked for: %+v", problems)
	}
	if got := GetTimeout(); got != 30*time.Second {
		t.Errorf("GetTimeout = %v, want the 30s default", got)
	}
	_ = GetTimeout()
	if len(problems) != 1 || problems[0].Variable != "SCHEMAFLUX_TIMEOUT" {
		t.Errorf("problems = %+v, want one report naming SCHEMAFLUX_TIMEOUT", problems)
	}
}

// A package that keeps settings of its own registers to hear of every reload.
func TestRegisteredFunctionsAreCalledByEveryReload(t *testing.T) {
	for hooks := 1; hooks <= 10; hooks++ {
		t.Run(fmt.Sprintf("%d registered", hooks), func(t *testing.T) {
			calls := make([]atomic.Int32, hooks)
			for i := range calls {
				OnEnvironmentReload(func() { calls[i].Add(1) })
			}
			ReloadEnvironment()
			for i := range calls {
				if got := calls[i].Load(); got != 1 {
					t.Errorf("function %d of %d was called %d times by a reload, want 1", i, hooks, got)
				}
			}
		})
	}
}

// What a registered function forgets is forgotten after the settings of this
// package have been, so it may read them again.
func TestRegisteredFunctionsRunAfterTheSettingsAreForgotten(t *testing.T) {
	setenvAndReload(t, "SCHEMAFLUX_REPAIR_ATTEMPTS", "3")
	if got := GetRepairAttempts(); got != 3 {
		t.Fatalf("GetRepairAttempts = %d, want 3", got)
	}
	t.Setenv("SCHEMAFLUX_REPAIR_ATTEMPTS", "8")
	var seen atomic.Int32
	OnEnvironmentReload(func() { seen.Store(int32(GetRepairAttempts())) })
	ReloadEnvironment()
	if got := seen.Load(); got != 8 {
		t.Errorf("a registered function that read a setting during the reload saw %d, want the new 8", got)
	}
}
