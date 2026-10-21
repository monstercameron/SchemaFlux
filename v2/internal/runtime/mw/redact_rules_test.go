package mw

import (
	"encoding/json"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// redactorOf is the scrubbing function of a RedactEgress built with opts.
func redactorOf(opts ...RedactOption) func(string) string {
	return RedactEgress(opts...)(nil).(*redactingProvider).redact
}

// Credential shapes are built, so that no literal in this file is one.
var (
	redactTestFineGrained = "github_pat_" + strings.Repeat("A1b2", 6) + "_" + strings.Repeat("Z9y8", 15)
	redactTestJWT         = "eyJ" + "hbGciOiJIUzI1NiJ9" + "." + "eyJ" + "zdWIiOiIxMjM0NTY3ODkwIn0" + "." + "dBjftJeZ4CVPmB92K27uhbUJU1p1r"
	redactTestStripe      = "sk_" + "live_" + strings.Repeat("a1B2", 6)
)

func TestRedactEgressScrubsTheCredentialShapesItNames(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"a fine-grained GitHub token", "token " + redactTestFineGrained + " here", "token [redacted:github-fine-grained-token] here"},
		{"a fine-grained token with a short body is not one", "github_pat_short", "github_pat_short"},
		{"a word ending in the prefix is not a token", "xgithub_pat_" + strings.Repeat("a", 30), "xgithub_pat_" + strings.Repeat("a", 30)},
		{"a hyphen is not the prefix", "github-pat_" + strings.Repeat("a", 30), "github-pat_" + strings.Repeat("a", 30)},
		{"a JWT", "header " + redactTestJWT + ".", "header [redacted:jwt]."},
		{"two segments are not a JWT", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0"},
		{"a live Stripe key", "key=" + redactTestStripe, "key=[redacted:stripe-key]"},
		{"a restricted Stripe key", "key=rk_" + "live_" + strings.Repeat("Q7", 10), "key=[redacted:stripe-key]"},
		{"a test Stripe key is not a live one", "key=sk_" + "test_" + strings.Repeat("a1B2", 6), "key=sk_" + "test_" + strings.Repeat("a1B2", 6)},
		{"a short Stripe key is not one", "sk_live_abc", "sk_live_abc"},
		{"a GitHub token", "ghp_" + strings.Repeat("a1", 20), "[redacted:github-token]"},
		{"a Slack token", "xoxb-" + "1234567890-abcdefghij", "[redacted:slack-token]"},
		{"an AWS access key id", "AKIA" + "ABCDEFGHIJKLMNOP", "[redacted:aws-access-key-id]"},
		{"an AWS key id inside a longer word is not one", "XAKIA" + "ABCDEFGHIJKLMNOP", "XAKIA" + "ABCDEFGHIJKLMNOP"},
		{"a Google key", "AIza" + strings.Repeat("a1B2", 9), "[redacted:google-api-key]"},
		{"risk-adjusted is not an OpenAI key", "a risk-adjusted return, a desk-top, a task-based plan", "a risk-adjusted return, a desk-top, a task-based plan"},
		{"a key after a near miss", "risk-adjusted then sk-" + strings.Repeat("a1", 12), "risk-adjusted then [redacted:openai-key]"},
	}
	redact := redactorOf()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redact(tc.input); got != tc.want {
				t.Errorf("redact(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// A bearer token is replaced and the words around it are kept, so that a header
// that is a value inside JSON is still JSON.
func TestRedactEgressKeepsTheShapeAroundABearerToken(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"a header", "Authorization: Bearer abc123", "Authorization: Bearer [redacted:bearer-header]"},
		{"a header in JSON", `{"Authorization": "Bearer abc123"}`, `{"Authorization": "Bearer [redacted:bearer-header]"}`},
		{"a header in JSON with more after it", `{"Authorization":"Bearer abc123","Accept":"*/*"}`, `{"Authorization":"Bearer [redacted:bearer-header]","Accept":"*/*"}`},
		{"a header in single quotes", `-H 'Authorization: Bearer abc123' --url x`, `-H 'Authorization: Bearer [redacted:bearer-header]' --url x`},
		{"every case", "AUTHORIZATION:BEARER abc123", "AUTHORIZATION:BEARER [redacted:bearer-header]"},
		{"a long bearer token on its own", "use Bearer " + strings.Repeat("a1", 12) + " for it", "use Bearer [redacted:bearer-header] for it"},
		{"a short word after bearer is not a token", "the bearer of bad news", "the bearer of bad news"},
		{"a bearer of a sentence", "Bearer bonds pay interest", "Bearer bonds pay interest"},
		{"a header with no bearer", "Authorization: Basic dXNlcjpwYXNz", "Authorization: Basic dXNlcjpwYXNz"},
		{"a token that is a marker already", "Authorization: Bearer [redacted:bearer-header]", "Authorization: Bearer [redacted:bearer-header]"},
	}
	redact := redactorOf()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.input)
			if got != tc.want {
				t.Errorf("redact(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if strings.HasPrefix(tc.input, "{") && !json.Valid([]byte(got)) {
				t.Errorf("redact(%q) = %q is not JSON", tc.input, got)
			}
		})
	}
}

func TestRedactEgressScrubsAddressesOfEveryScript(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"a plain address", "mail jane.doe@example.com now", "mail [redacted:email] now"},
		{"an underscore in the name", "mail user_name@example.com now", "mail [redacted:email] now"},
		{"an underscore at the start", "mail _ab@example.com now", "mail [redacted:email] now"},
		{"an accented name", "mail jänë@example.com now", "mail [redacted:email] now"},
		{"an internationalised domain", "mail jane@bücher.de now", "mail [redacted:email] now"},
		{"a name in another script", "mail 田中@example.jp now", "mail [redacted:email] now"},
		{"a plus tag", "mail a+tag@example.co.uk now", "mail [redacted:email] now"},
		{"two addresses", "a@b.co, c@d.org", "[redacted:email], [redacted:email]"},
		{"in angle brackets", "From: Jane <jane@example.com>", "From: Jane <[redacted:email]>"},
		{"in quotes", `"jane@example.com"`, `"[redacted:email]"`},
		{"at the end of a sentence", "Write to jane@example.com.", "Write to [redacted:email]."},
		{"an at sign with no name", "at @example.com", "at @example.com"},
		{"an at sign with no domain", "jane@ and more", "jane@ and more"},
		{"no top level domain", "jane@localhost", "jane@localhost"},
		{"a one letter top level domain", "jane@example.c", "jane@example.c"},
		{"a handle", "follow @jane on x", "follow @jane on x"},
		{"a lone at sign", "@", "@"},
		{"an address that follows another", "x@y.com@z.org", "[redacted:email]@z.org"},
	}
	redact := redactorOf()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redact(tc.input); got != tc.want {
				t.Errorf("redact(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// The address rule is run on the stretch around each "@" and not on the whole
// text. It has to find what the expression finds on the whole text, which is
// checked on many short texts made of the characters where the two could differ.
func TestRedactEgressFindsAddressesAsTheExpressionDoesOnTheWholeText(t *testing.T) {
	var email redactRule
	for _, pattern := range builtinRedactPatterns {
		if pattern.name == "email" {
			email = newRedactRule(pattern, "")
		}
	}
	alphabet := []string{"a", "b", "Z", "1", ".", "-", "_", "+", "%", "@", "@", "é", "田", " ", "\n", "<", ">", "com", ".org", "x.y", "ñ", "́"}
	random := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		var text strings.Builder
		for range 1 + random.IntN(14) {
			text.WriteString(alphabet[random.IntN(len(alphabet))])
		}
		input := text.String()
		var want [][2]int
		for _, m := range email.pattern.FindAllStringIndex(input, -1) {
			want = append(want, [2]int{m[0], m[1]})
		}
		var got [][2]int
		for _, span := range email.find(input, 0, nil) {
			got = append(got, [2]int{span.start, span.end})
		}
		if !slices.Equal(got, want) {
			t.Fatalf("on %q the stretches are %v, the expression on the whole text finds %v", input, got, want)
		}
	}
}

func TestRedactSpansDecidesBetweenOverlappingMatches(t *testing.T) {
	rule := func(name, expression string) redactPattern {
		return redactPattern{name: name, pattern: regexp.MustCompile(expression)}
	}
	cases := []struct {
		name     string
		patterns []redactPattern
		input    string
		want     string
	}{
		{"a short rule before a long one leaves no tail", []redactPattern{rule("short", `abc`), rule("long", `abc123`)}, "xx abc123 yy", "xx <long> yy"},
		{"a long rule before a short one is the same", []redactPattern{rule("long", `abc123`), rule("short", `abc`)}, "xx abc123 yy", "xx <long> yy"},
		{"the earlier rule of two of one length", []redactPattern{rule("first", `abc`), rule("second", `a.c`)}, "abc", "<first>"},
		{"the later rule when it is the longer", []redactPattern{rule("first", `ab`), rule("second", `abc`)}, "abc", "<second>"},
		{"a match that starts first wins over a longer one that starts later", []redactPattern{rule("left", `ab`), rule("right", `bcde`)}, "abcde", "<left>cde"},
		{"a rule does not match the marker of another", []redactPattern{rule("b", `b`), rule("a", `a`)}, "b a", "<b> <a>"},
		{"a marker is not rewritten by a rule that matches its letters", []redactPattern{rule("name", `secret`), rule("any", `[a-z]+`)}, "the secret", "<any> <name>"},
		{"matches apart are all replaced", []redactPattern{rule("n", `\d+`)}, "1 22 333", "<n> <n> <n>"},
		{"an empty match is not a thing to remove", []redactPattern{rule("e", `x*`)}, "abc", "abc"},
		{"nothing matches", []redactPattern{rule("n", `\d+`)}, "abc", "abc"},
		{"no rules", nil, "abc", "abc"},
		{"a group is the part replaced", []redactPattern{{name: "g", pattern: regexp.MustCompile(`key=(\w+)`), group: 1}}, "key=abc", "key=<g>"},
		{"a group that did not take part is nothing", []redactPattern{{name: "g", pattern: regexp.MustCompile(`k(x)?=`), group: 1}}, "k=", "k="},
		{"two groups of one rule", []redactPattern{{name: "g", pattern: regexp.MustCompile(`(\d)-(\d)`), group: 2}}, "1-2 3-4", "1-<g> 3-<g>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := make([]redactRule, len(tc.patterns))
			for i, pattern := range tc.patterns {
				rules[i] = newRedactRule(pattern, "<"+pattern.name+">")
			}
			if got := redactSpans(tc.input, rules); got != tc.want {
				t.Errorf("redactSpans(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestRedactEgressRulesOfTheCallerDoNotCutEachOthersMarkers(t *testing.T) {
	redact := redactorOf(WithoutBuiltins(),
		WithPattern("a-short", regexp.MustCompile(`abc`)),
		WithPattern("b-long", regexp.MustCompile(`abc123`)))
	if got, want := redact("xx abc123 yy"), "xx [redacted:b-long] yy"; got != want {
		t.Errorf("a short rule before a long one: %q, want %q", got, want)
	}

	redact = redactorOf(WithoutBuiltins(),
		WithPattern("b", regexp.MustCompile(`b`)),
		WithPattern("a", regexp.MustCompile(`a`)))
	if got, want := redact("b a"), "[redacted:b] [redacted:a]"; got != want {
		t.Errorf("a rule after one whose marker holds its letters: %q, want %q", got, want)
	}
}

func TestRedactIndex(t *testing.T) {
	cases := []struct {
		name, text, needle string
		fold               bool
		want               int
	}{
		{"exact at the start", "bearer x", "bearer", false, 0},
		{"exact in the middle", "a bearer x", "bearer", false, 2},
		{"exact is case sensitive", "BEARER", "bearer", false, -1},
		{"folded at the start", "BEARER x", "bearer", true, 0},
		{"folded in the middle", "the BeArEr is", "bearer", true, 4},
		{"folded at the end", "xx bearer", "bearer", true, 3},
		{"folded exactly the needle", "Bearer", "bearer", true, 0},
		{"folded absent", "there is no such word", "bearer", true, -1},
		{"folded text shorter than the needle", "bear", "bearer", true, -1},
		{"folded empty text", "", "bearer", true, -1},
		{"folded after a false start", "bearebearer", "bearer", true, 5},
		{"folded after several false starts", "bbbb bear bearr BEARER", "bearer", true, 16},
		{"folded needle with a rare letter", "an AUTHORIZATION header", "authorization", true, 3},
		{"folded needle with the rare letter in the text only", "authoriza", "authorization", true, -1},
		{"folded one byte short at the end", "xx authorizatio", "authorization", true, -1},
		{"folded multi byte text before it", "é田 Bearer", "bearer", true, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactIndex(tc.text, tc.needle, tc.fold); got != tc.want {
				t.Errorf("redactIndex(%q, %q, %v) = %d, want %d", tc.text, tc.needle, tc.fold, got, tc.want)
			}
		})
	}
}

// The cost of redaction on a request that holds nothing to redact. A text no
// rule matches is scanned for the few literals the rules start on and is not
// copied, so the figure to hold is the allocations: none, whatever the size.
func TestRedactAllocatesNothingForTextThatHoldsNothingToRedact(t *testing.T) {
	redact := redactorOf()
	for _, size := range []int{0, 1, 100, 3_500, 110_000} {
		text := redactBenchText(size)
		if allocs := testing.AllocsPerRun(20, func() { redact(text) }); allocs != 0 {
			t.Errorf("redact of %d clean bytes allocated %v times, want 0", size, allocs)
		}
	}
	// Text that starts a rule's literal and is not a match for it is the same.
	near := strings.Repeat("a risk-adjusted desk-top, bearer of news, eyJ nothing, @ alone, AKIA short. ", 100)
	if allocs := testing.AllocsPerRun(20, func() { redact(near) }); allocs != 0 {
		t.Errorf("redact of near misses allocated %v times, want 0", allocs)
	}
}
