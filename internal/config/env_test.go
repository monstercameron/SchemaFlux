package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/internal/types"
)

func recordEnvProblems(t *testing.T) *[]EnvProblem {
	t.Helper()
	var problems []EnvProblem
	t.Cleanup(SetEnvProblemReporter(func(problem EnvProblem) { problems = append(problems, problem) }))
	return &problems
}

// A boolean is read by strconv.ParseBool, the standard library's rule, after
// surrounding space is trimmed. SCHEMAFLUX_DEBUG=1 is what a shell user types.
func TestEnvBoolReadsWhatParseBoolReads(t *testing.T) {
	cases := []struct {
		raw     string
		want    bool
		problem bool
	}{
		{"1", true, false},
		{"t", true, false},
		{"T", true, false},
		{"TRUE", true, false},
		{"true", true, false},
		{"True", true, false},
		{"0", false, false},
		{"f", false, false},
		{"F", false, false},
		{"FALSE", false, false},
		{"false", false, false},
		{"False", false, false},
		{"  true  ", true, false},
		{"\t1\n", true, false},
		{" F ", false, false},
		{"", false, false},
		{"   ", false, false},
		{"yes", false, true},
		{"no", false, true},
		{"on", false, true},
		{"off", false, true},
		{"y", false, true},
		{"n", false, true},
		{"tRuE", false, true},
		{"fAlSe", false, true},
		{"enabled", false, true},
		{"truee", false, true},
		{"2", false, true},
		{"-1", false, true},
		{"01", false, true},
		{"1.0", false, true},
		{"t rue", false, true},
		{"true,false", false, true},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			problems := recordEnvProblems(t)
			setenvAndReload(t, "SCHEMAFLUX_TEST_FLAG", tc.raw)

			if got := EnvBool("SCHEMAFLUX_TEST_FLAG"); got != tc.want {
				t.Errorf("EnvBool(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			if got := len(*problems) == 1; got != tc.problem {
				t.Errorf("EnvBool(%q) reported %d problem(s), want a report: %v", tc.raw, len(*problems), tc.problem)
			}
		})
	}
}

// With a fallback of true, a malformed value and an empty one give true, and
// every spelling ParseBool reads gives what it says.
func TestEnvBoolOrGivesTheFallbackForAnUnusableValue(t *testing.T) {
	cases := []struct {
		raw     string
		want    bool
		problem bool
	}{
		{"", true, false},
		{"  ", true, false},
		{"maybe", true, true},
		{"yes", true, true},
		{"on", true, true},
		{"1", true, false},
		{"T", true, false},
		{"0", false, false},
		{"f", false, false},
		{"false", false, false},
		{" FALSE ", false, false},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			problems := recordEnvProblems(t)
			setenvAndReload(t, "SCHEMAFLUX_TEST_FLAG", tc.raw)
			if got := EnvBoolOr("SCHEMAFLUX_TEST_FLAG", true); got != tc.want {
				t.Errorf("EnvBoolOr(%q, true) = %v, want %v", tc.raw, got, tc.want)
			}
			if got := len(*problems) == 1; got != tc.problem {
				t.Errorf("EnvBoolOr(%q, true) reported %d problem(s), want a report: %v", tc.raw, len(*problems), tc.problem)
			}
		})
	}

	// With a fallback of false, a malformed value gives false.
	problems := recordEnvProblems(t)
	setenvAndReload(t, "SCHEMAFLUX_TEST_FLAG", "maybe")
	if EnvBoolOr("SCHEMAFLUX_TEST_FLAG", false) || len(*problems) != 1 {
		t.Errorf("a malformed value with a false fallback gave true or went unreported (%d reports)", len(*problems))
	}
}

// EnvBoolSet is the reading EnvBool and EnvBoolOr are made from, for a caller
// that treats an unset variable differently from a false one.
func TestEnvBoolSetSaysWhetherTheVariableGaveAnAnswer(t *testing.T) {
	cases := []struct {
		raw     string
		value   bool
		ok      bool
		problem bool
	}{
		{"", false, false, false},
		{"  ", false, false, false},
		{"true", true, true, false},
		{"1", true, true, false},
		{" T ", true, true, false},
		{"false", false, true, false},
		{"0", false, true, false},
		{"F", false, true, false},
		{"yes", false, false, true},
		{"on", false, false, true},
		{"tRuE", false, false, true},
		{"2", false, false, true},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			problems := recordEnvProblems(t)
			setenvAndReload(t, "SCHEMAFLUX_TEST_FLAG", tc.raw)
			value, ok := EnvBoolSet("SCHEMAFLUX_TEST_FLAG")
			if value != tc.value || ok != tc.ok {
				t.Errorf("EnvBoolSet(%q) = %v, %v; want %v, %v", tc.raw, value, ok, tc.value, tc.ok)
			}
			if got := len(*problems) == 1; got != tc.problem {
				t.Errorf("EnvBoolSet(%q) reported %d problem(s), want a report: %v", tc.raw, len(*problems), tc.problem)
			}
		})
	}
}

// A problem is reported once for a variable and value, names the variable, and
// never carries the value: an environment variable may hold a credential.
func TestAnEnvironmentProblemIsReportedOnceAndNeverCarriesTheValue(t *testing.T) {
	problems := recordEnvProblems(t)
	setenvAndReload(t, "SCHEMAFLUX_TEST_FLAG", "sk-secret-value-123")

	for i := 0; i < 10; i++ {
		EnvBool("SCHEMAFLUX_TEST_FLAG")
	}
	if len(*problems) != 1 {
		t.Fatalf("reported %d times, want once", len(*problems))
	}
	problem := (*problems)[0]
	if problem.Variable != "SCHEMAFLUX_TEST_FLAG" {
		t.Errorf("Variable = %q", problem.Variable)
	}
	if strings.Contains(problem.Want, "secret") || strings.Contains(problem.Variable, "secret") {
		t.Errorf("the report carries the value: %+v", problem)
	}

	// A different bad value is a different problem.
	setenvAndReload(t, "SCHEMAFLUX_TEST_FLAG", "another-bad-value")
	EnvBool("SCHEMAFLUX_TEST_FLAG")
	if len(*problems) != 2 {
		t.Errorf("a changed bad value was not reported: %d reports", len(*problems))
	}
}

func TestEnvIntRefusesWhatIsNotAWholeNumberAtOrAboveTheMinimum(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		problem bool
	}{
		{"", 7, false},
		{"0", 0, false},
		{"5", 5, false},
		{" 5 ", 5, false},
		{"-1", 7, true},
		{"abc", 7, true},
		{"1.5", 7, true},
		{"99999999999999999999", 7, true},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			problems := recordEnvProblems(t)
			setenvAndReload(t, "SCHEMAFLUX_TEST_COUNT", tc.raw)
			if got := EnvInt("SCHEMAFLUX_TEST_COUNT", 7, 0); got != tc.want {
				t.Errorf("EnvInt(%q) = %d, want %d", tc.raw, got, tc.want)
			}
			if got := len(*problems) == 1; got != tc.problem {
				t.Errorf("EnvInt(%q): reported %d problem(s), want a report: %v", tc.raw, len(*problems), tc.problem)
			}
		})
	}
}

func TestEnvPositiveDurationRefusesZeroNegativeAndGarbage(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		problem bool
	}{
		{"", time.Second, false},
		{"2s", 2 * time.Second, false},
		{"150ms", 150 * time.Millisecond, false},
		{"0s", time.Second, true},
		{"0", time.Second, true},
		{"-5s", time.Second, true},
		{"soon", time.Second, true},
		{"5", time.Second, true},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			problems := recordEnvProblems(t)
			setenvAndReload(t, "SCHEMAFLUX_TEST_DURATION", tc.raw)
			if got := EnvPositiveDuration("SCHEMAFLUX_TEST_DURATION", time.Second); got != tc.want {
				t.Errorf("EnvPositiveDuration(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			if got := len(*problems) == 1; got != tc.problem {
				t.Errorf("EnvPositiveDuration(%q): reported %d problem(s), want a report: %v", tc.raw, len(*problems), tc.problem)
			}
		})
	}
}

func TestEnvChoiceFoldsCaseAndRefusesAnythingElse(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		problem bool
	}{
		{"", "auto", false},
		{"uuid", "uuid", false},
		{"UUID", "uuid", false},
		{"  none ", "none", false},
		{"uuiid", "auto", true},
		{"random", "auto", true},
		{"AUTO", "auto", false},
		{"None", "none", false},
		{"   ", "auto", false},
		{"uu id", "auto", true},
		{"uuid,none", "auto", true},
		{"\tuuid\t", "uuid", false},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			problems := recordEnvProblems(t)
			setenvAndReload(t, "SCHEMAFLUX_TEST_CHOICE", tc.raw)
			if got := EnvChoice("SCHEMAFLUX_TEST_CHOICE", "auto", "auto", "uuid", "none"); got != tc.want {
				t.Errorf("EnvChoice(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if got := len(*problems) == 1; got != tc.problem {
				t.Errorf("EnvChoice(%q): reported %d problem(s), want a report: %v", tc.raw, len(*problems), tc.problem)
			}
		})
	}
}

// A timeout of zero or less expires as it is set. GetTimeout fell back to
// accepting one, so every call that carried no deadline of its own failed at once.
func TestGetTimeoutRefusesAZeroOrNegativeValue(t *testing.T) {
	for _, raw := range []string{"0s", "0", "-1s", "-5m", "0ms", "not-a-duration"} {
		t.Run(raw, func(t *testing.T) {
			problems := recordEnvProblems(t)
			setenvAndReload(t, "SCHEMAFLUX_TIMEOUT", raw)

			if got := GetTimeout(); got != 30*time.Second {
				t.Errorf("GetTimeout() = %v with SCHEMAFLUX_TIMEOUT=%q, want the 30s default", got, raw)
			}
			if len(*problems) != 1 || (*problems)[0].Variable != "SCHEMAFLUX_TIMEOUT" {
				t.Errorf("the refusal was not reported as SCHEMAFLUX_TIMEOUT: %+v", *problems)
			}
		})
	}
}

func TestTimeoutFromEnvReturnsAnErrorNamingTheVariableButNotTheValue(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		refused bool
	}{
		{"", 30 * time.Second, false},
		{"45s", 45 * time.Second, false},
		{" 2m ", 2 * time.Minute, false},
		{"1ns", time.Nanosecond, false},
		{"0s", 0, true},
		{"0", 0, true},
		{"-1s", 0, true},
		{"-1", 0, true},
		{"forever", 0, true},
		{"sk-looks-like-a-key", 0, true},
	}
	for _, tc := range cases {
		t.Run("["+tc.raw+"]", func(t *testing.T) {
			setenvAndReload(t, "SCHEMAFLUX_TIMEOUT", tc.raw)
			got, err := TimeoutFromEnv()
			if tc.refused {
				if err == nil {
					t.Fatalf("TimeoutFromEnv() accepted %q as %v", tc.raw, got)
				}
				if got != 0 {
					t.Errorf("a refusal came with a duration: %v", got)
				}
				if !strings.Contains(err.Error(), "SCHEMAFLUX_TIMEOUT") {
					t.Errorf("the error does not name the variable: %v", err)
				}
				if tc.raw != "" && len(tc.raw) > 3 && strings.Contains(err.Error(), tc.raw) {
					t.Errorf("the error repeats the value: %v", err)
				}
				if types.KindOf(err) != types.KindConfiguration || !errors.Is(err, types.ErrConfiguration) {
					t.Errorf("the error is not a classified configuration error: %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("TimeoutFromEnv() = %v, %v; want %v, nil", got, err, tc.want)
			}
		})
	}
}

// The booleans have one reading in every getter, so SCHEMAFLUX_DEBUG=1 cannot
// mean debug to one reader and not to another.
func TestEveryBooleanGetterReadsTheSameValues(t *testing.T) {
	getters := map[string]struct {
		variable string
		read     func() bool
	}{
		"debug":   {"SCHEMAFLUX_DEBUG", GetDebugMode},
		"metrics": {"SCHEMAFLUX_METRICS", IsMetricsEnabled},
	}
	for name, getter := range getters {
		for raw, want := range map[string]bool{"true": true, "false": false, "1": true, "0": false, "T": true, "yes": false, "on": false, "TRUE": true, "": false} {
			t.Run(name+"="+raw, func(t *testing.T) {
				recordEnvProblems(t)
				SetDebugMode(false)
				SetMetricsEnabled(false)
				setenvAndReload(t, "SCHEMAFLUX_DEBUG", "")
				setenvAndReload(t, "SCHEMAFLUX_METRICS", "")
				setenvAndReload(t, getter.variable, raw)

				if got := getter.read(); got != want {
					t.Errorf("%s=%q read as %v, want %v", getter.variable, raw, got, want)
				}
			})
		}
	}
}
