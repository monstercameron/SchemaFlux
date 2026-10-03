package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

// This file is the one place the library parses an environment variable's
// value. The booleans were parsed five ways ("true" only; "true" or "1"; "1",
// "true", "yes" or "on" in two packages), so SCHEMAFLUX_DEBUG=yes turned the
// logger to debug and left the operations' debug branch off, and a value none
// of them recognised was treated as off with no message anywhere.
//
// A boolean is read by strconv.ParseBool, the rule the standard library and so
// most Go programs already follow: 1, t, T, TRUE, true and True are true; 0, f,
// F, FALSE, false and False are false; surrounding space is ignored. "yes",
// "on" and mixed case such as "tRuE" are not booleans there, and are not here.
// A value that is none of those is reported once, naming the variable and never
// its value, and the variable then counts as unset.

// EnvProblem names an environment variable whose value the library could not
// use. It never carries the value: an environment variable may hold a
// credential, and a report of a malformed one is read by whoever holds the logs.
type EnvProblem struct {
	// Variable is the name of the environment variable.
	Variable string

	// Want says what the variable accepts.
	Want string
}

var (
	envReportMu sync.Mutex
	envReported = map[string]bool{}
	envReporter = reportEnvProblemToStderr
)

func reportEnvProblemToStderr(problem EnvProblem) {
	fmt.Fprintf(os.Stderr, "schemaflux: the value of %s is not usable; it takes %s, so the default applies\n",
		problem.Variable, problem.Want)
}

// SetEnvProblemReporter replaces where a malformed environment value is
// reported, which is standard error by default, and returns a function that puts
// the previous reporter back. Installing a reporter also forgets which values
// were already reported, so a reporter sees every problem found after it is
// installed.
func SetEnvProblemReporter(report func(EnvProblem)) (restore func()) {
	envReportMu.Lock()
	defer envReportMu.Unlock()
	previous := envReporter
	if report == nil {
		report = reportEnvProblemToStderr
	}
	envReporter = report
	envReported = map[string]bool{}
	return func() {
		envReportMu.Lock()
		defer envReportMu.Unlock()
		envReporter = previous
		envReported = map[string]bool{}
	}
}

// reportEnvProblem reports a malformed value once per variable and value. The
// value is the dedupe key and is held only in memory; it is never given to the
// reporter.
func reportEnvProblem(name, raw, want string) {
	envReportMu.Lock()
	key := name + "\x00" + raw
	if envReported[key] {
		envReportMu.Unlock()
		return
	}
	envReported[key] = true
	report := envReporter
	envReportMu.Unlock()

	report(EnvProblem{Variable: name, Want: want})
}

// EnvBool reads a boolean environment variable by the rule of
// strconv.ParseBool, with surrounding space ignored. An unset or empty variable
// is false. Any other value is reported once, naming the variable, and read as
// false.
func EnvBool(name string) bool {
	return EnvBoolOr(name, false)
}

// EnvBoolOr is EnvBool for a variable whose default is not false: an unset or
// empty variable, and a malformed one, give fallback.
func EnvBoolOr(name string, fallback bool) bool {
	if value, ok := EnvBoolSet(name); ok {
		return value
	}
	return fallback
}

// EnvBoolSet reads a boolean environment variable for a caller that treats a
// variable that says nothing differently from one that says false. ok is false
// when the variable is unset or empty, and when its value is malformed, which is
// reported once, naming the variable; value is then false. EnvBool and
// EnvBoolOr are made from it, so there is one reading of a boolean.
func EnvBoolSet(name string) (value, ok bool) {
	raw := os.Getenv(name)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false, false
	}
	value, err := strconv.ParseBool(trimmed)
	if err != nil {
		reportEnvProblem(name, raw, "a boolean: 1, t, T, TRUE, true, True, 0, f, F, FALSE, false or False")
		return false, false
	}
	return value, true
}

// EnvInt reads a whole-number environment variable that must be at least min.
// An unset or empty variable, and a value that is not a whole number or is below
// min, gives fallback; the latter two are reported once, naming the variable.
func EnvInt(name string, fallback, min int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min {
		reportEnvProblem(name, raw, fmt.Sprintf("a whole number of at least %d", min))
		return fallback
	}
	return value
}

// EnvPositiveDuration reads a duration environment variable that must be
// greater than zero. An unset or empty variable, and a value that is not a
// duration or is not positive, gives fallback; the latter two are reported once,
// naming the variable.
func EnvPositiveDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		reportEnvProblem(name, raw, "a positive duration such as 30s")
		return fallback
	}
	return value
}

// EnvChoice reads an environment variable that must be one of allowed, folded to
// lower case. An unset or empty variable gives fallback; any other value is
// reported once, naming the variable, and also gives fallback.
func EnvChoice(name, fallback string, allowed ...string) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	folded := strings.ToLower(raw)
	for _, candidate := range allowed {
		if folded == candidate {
			return folded
		}
	}
	reportEnvProblem(name, raw, "one of "+strings.Join(allowed, ", "))
	return fallback
}

// defaultTimeout is the deadline an operation gets when neither the caller nor
// SCHEMAFLUX_TIMEOUT names one.
const defaultTimeout = 30 * time.Second

// TimeoutFromEnv reads SCHEMAFLUX_TIMEOUT. An unset variable gives the default
// of 30 seconds. A value that is not a duration, or is zero or negative, is an
// error naming the variable and not its value: a zero deadline expires the
// moment it is set, so accepting one fails every call that carries no deadline of
// its own.
//
// Code that configures a client calls this and refuses to start on the error.
// GetTimeout is for the per-call reads, where there is nobody to return the
// error to.
func TimeoutFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("SCHEMAFLUX_TIMEOUT"))
	if raw == "" {
		return defaultTimeout, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, types.Newf(types.KindConfiguration, "config",
			"SCHEMAFLUX_TIMEOUT must be a positive duration such as 30s")
	}
	return value, nil
}
