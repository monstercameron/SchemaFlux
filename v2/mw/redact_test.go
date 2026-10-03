package mw

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// phonePattern is the rule a program adds for the phone numbers of its domain:
// no built-in rule covers a shape this common in ordinary numbers.
var phonePattern = map[string]*regexp.Regexp{"phone": regexp.MustCompile(`\+\d[\d ]{8,}\d`)}

// reached is what the provider was sent, after the middleware under test.
func reached(t *testing.T, opts ...RedactOptions) (provider *schemafluxtest.Provider, send func(schemaflux.Request) (schemaflux.Response, error)) {
	t.Helper()
	provider = new(schemafluxtest.Provider)
	provider.Reply("answer")
	redacting := Redact(opts...)(provider)
	return provider, func(req schemaflux.Request) (schemaflux.Response, error) {
		return redacting.Complete(t.Context(), req)
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()

	t.Run("an email address never reaches the provider, in the user message or the system prompt", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t)
		if _, err := send(schemaflux.Request{System: "reply to " + testEmail, User: "from " + testEmail + " please"}); err != nil {
			t.Fatal(err)
		}
		got := p.Last()
		mustNotContain(t, "the request", got.System+"\n"+got.User, testEmail, "jane.doe")
		if got.User != "from [redacted:email] please" || got.System != "reply to [redacted:email]" {
			t.Errorf("the provider was sent %+v", got)
		}
	})

	for name, secret := range map[string]string{
		"an OpenAI key":     testKey,
		"an Anthropic key":  "sk-" + "ant-" + strings.Repeat("Ab1_", 8),
		"a Google key":      "AI" + "za" + strings.Repeat("Q3x", 12),
		"an AWS key id":     "AK" + "IA" + "ABCDEFGHIJKLMNOP",
		"a GitHub token":    "gh" + "p_" + strings.Repeat("a1", 20),
		"a Slack token":     "xo" + "xb-" + "1234567890-abcdefgh",
		"a bearer header":   `Authorization: Bearer ` + strings.Repeat("tok.", 5),
		"a private key":     "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\n-----END RSA PRIVATE KEY-----",
		"a key with no end": "-----BEGIN PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu",
	} {
		t.Run("a key-shaped string does not reach the provider: "+name, func(t *testing.T) {
			t.Parallel()
			p, send := reached(t)
			if _, err := send(schemaflux.Request{System: "use " + secret, User: "here is " + secret + " ok"}); err != nil {
				t.Fatal(err)
			}
			got := p.Last()
			body := secret
			if i := strings.Index(secret, "\n"); i > 0 {
				body = secret[i+1:]
			}
			mustNotContain(t, "the request", got.System+"\n"+got.User, body, strings.TrimPrefix(secret, "Authorization: Bearer "))
			if !strings.Contains(got.User, "[redacted:") {
				t.Errorf("the user message is %q, want a marker", got.User)
			}
		})
	}

	t.Run("a phone number is not a built-in shape, and a rule of the caller's removes it", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t)
		send(ask("call " + testPhone))
		if !strings.Contains(p.Last().User, testPhone) {
			t.Errorf("the built-ins removed a phone number: %q", p.Last().User)
		}
		p, send = reached(t, RedactOptions{Patterns: phonePattern})
		send(ask("call " + testPhone + " or mail " + testEmail))
		if got := p.Last().User; got != "call [redacted:phone] or mail [redacted:email]" {
			t.Errorf("the provider was sent %q", got)
		}
	})

	t.Run("the marker names the rule, and a flat marker is used as it is", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ marker, want string }{
			{"<%s>", "write <email>"},
			{"***", "write ***"},
			{"%s-%s", "write email-email"},
			{"$1 %s %d", "write $1 email %d"},
			{"", "write [redacted:email]"},
		} {
			p, send := reached(t, RedactOptions{Marker: tc.marker})
			send(ask("write " + testEmail))
			if got := p.Last().User; got != tc.want {
				t.Errorf("marker %q: sent %q, want %q", tc.marker, got, tc.want)
			}
		}
	})

	t.Run("NoBuiltins leaves what the built-ins would remove and still applies the caller's rules", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t, RedactOptions{NoBuiltins: true, Patterns: phonePattern})
		send(ask(testEmail + " " + testPhone))
		if got := p.Last().User; got != testEmail+" [redacted:phone]" {
			t.Errorf("the provider was sent %q", got)
		}
	})

	t.Run("Func runs after the patterns, on what they left", func(t *testing.T) {
		t.Parallel()
		var seen []string
		p, send := reached(t, RedactOptions{Func: func(text string) string {
			seen = append(seen, text)
			return strings.ReplaceAll(text, "Acme", "[company]")
		}})
		send(ask("Acme wrote " + testEmail))
		if len(seen) != 1 || seen[0] != "Acme wrote [redacted:email]" {
			t.Errorf("Func was given %q", seen)
		}
		if got := p.Last().User; got != "[company] wrote [redacted:email]" {
			t.Errorf("the provider was sent %q", got)
		}
	})

	t.Run("a Func that panics fails the request as an internal error and nothing is sent", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t, RedactOptions{Func: func(text string) string { panic("detector bug on " + text) }})
		_, err := send(ask("secret text " + testEmail))
		wantKind(t, err, schemaflux.ErrInternal)
		mustNotContain(t, "the error", err.Error(), "secret text", testEmail, "detector bug")
		if p.Calls() != 0 {
			t.Errorf("the provider was asked %d times, want 0", p.Calls())
		}
	})

	t.Run("a rule with no expression is a configuration error and nothing is sent", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t, RedactOptions{Patterns: map[string]*regexp.Regexp{"broken": nil}})
		_, err := send(ask("q"))
		e := wantKind(t, err, schemaflux.ErrConfiguration)
		if e.Op != "mw.redact" || p.Calls() != 0 {
			t.Errorf("Op %q, %d requests", e.Op, p.Calls())
		}
	})

	t.Run("every rule is looked for in the text as it came, and no rule matches the marker of another, every time", func(t *testing.T) {
		t.Parallel()
		patterns := map[string]*regexp.Regexp{
			"b-second": regexp.MustCompile(`a-first`),
			"a-first":  regexp.MustCompile(`foo`),
		}
		for range 40 {
			p, send := reached(t, RedactOptions{NoBuiltins: true, Patterns: patterns})
			send(ask("foo"))
			if got := p.Last().User; got != "[redacted:a-first]" {
				t.Fatalf("the provider was sent %q: b-second matched the marker a-first wrote", got)
			}
		}
	})

	t.Run("where two rules match the same secret the longer one wins and no tail of it is left", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t, RedactOptions{NoBuiltins: true, Patterns: map[string]*regexp.Regexp{
			"a-short": regexp.MustCompile(`abc`),
			"b-long":  regexp.MustCompile(`abc123`),
		}})
		send(ask("xx abc123 yy"))
		if got := p.Last().User; got != "xx [redacted:b-long] yy" {
			t.Errorf("the provider was sent %q", got)
		}
	})

	t.Run("the fields that are not text are sent as they are, and the caller's request is not changed", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t)
		schema := map[string]any{"description": testEmail}
		req := schemaflux.Request{Model: "m", System: "s", User: "u " + testEmail, Temperature: 0.5, MaxTokens: 7, Format: "json",
			Schema: schema, SchemaName: "n", Effort: "low", CacheKey: "k", WebSearch: true}
		before := req
		send(req)
		got := p.Last()
		if got.Model != "m" || got.Temperature != 0.5 || got.MaxTokens != 7 || got.Format != "json" || got.SchemaName != "n" ||
			got.Effort != "low" || got.CacheKey != "k" || !got.WebSearch || got.Schema["description"] != testEmail {
			t.Errorf("the provider was sent %+v", got)
		}
		if req.User != before.User || req.System != before.System {
			t.Errorf("the caller's request was changed to %+v", req)
		}
	})

	t.Run("what comes back is not redacted: it has left the provider already", func(t *testing.T) {
		t.Parallel()
		var p schemafluxtest.Provider
		p.Reply("write to " + testEmail)
		resp, err := Redact()(&p).Complete(t.Context(), ask("q"))
		if err != nil || resp.Content != "write to "+testEmail {
			t.Errorf("%q, %v", resp.Content, err)
		}
	})

	t.Run("a blank text is left alone", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t)
		send(schemaflux.Request{})
		if got := p.Last(); got.User != "" || got.System != "" {
			t.Errorf("the provider was sent %+v", got)
		}
	})

	t.Run("every text of a decision is redacted, so a decision provider is sent none of it", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.Yes(0.9))
		redacting := Redact(RedactOptions{Patterns: phonePattern})(d)
		decision := &schemaflux.Decision{
			State:    `{"mail":"` + testEmail + `"}`,
			Guidance: "never contact " + testEmail,
			Questions: []schemaflux.DecisionQuestion{{
				Kind: schemaflux.DecisionYesNo, Ask: "is " + testPhone + " valid?",
				Criteria: []string{"owned by " + testEmail},
			}},
		}
		if _, err := redacting.Complete(t.Context(), schemaflux.Request{User: "u", Decision: decision}); err != nil {
			t.Fatal(err)
		}
		got := d.Last().Decision
		text := got.State + "\n" + got.Guidance + "\n" + got.Questions[0].Ask + "\n" + strings.Join(got.Questions[0].Criteria, "\n")
		mustNotContain(t, "the decision", text, testEmail, testPhone)
		if !strings.Contains(got.State, "[redacted:email]") || !strings.Contains(got.Questions[0].Ask, "[redacted:phone]") {
			t.Errorf("the decision was sent as %+v", got)
		}
		if decision.State != `{"mail":"`+testEmail+`"}` || decision.Questions[0].Ask != "is "+testPhone+" valid?" {
			t.Errorf("the caller's decision was changed: %+v", decision)
		}
	})

	t.Run("a choice is answered with the caller's own option when the one that was sent was rewritten", func(t *testing.T) {
		t.Parallel()
		d := new(schemafluxtest.Decider)
		d.Respond(func(_ int, decision schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
			return []schemaflux.DecisionAnswer{schemafluxtest.Chose(decision.Questions[0].Options[1], 0.9)}, nil
		})
		options := []string{"plain", testEmail}
		resp, err := Redact()(d).Complete(t.Context(), schemaflux.Request{User: "u", Decision: choice("which?", options...)})
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Answers[0].Label; got != testEmail {
			t.Errorf("the label is %q, want the caller's option %q", got, testEmail)
		}
		if sent := d.Last().Decision.Questions[0].Options; sent[1] != "[redacted:email]" || sent[0] != "plain" {
			t.Errorf("the options sent were %q", sent)
		}
		if !slices.Equal(options, []string{"plain", testEmail}) {
			t.Errorf("the caller's options were changed to %q", options)
		}
	})

	t.Run("two options that one rule rewrote to the same text are sent apart and each is restored", func(t *testing.T) {
		t.Parallel()
		first, second := "alice@example.com", "bob@example.com"
		for want, index := range map[string]int{first: 0, second: 1} {
			d := new(schemafluxtest.Decider)
			d.Respond(func(_ int, decision schemaflux.Decision) ([]schemaflux.DecisionAnswer, error) {
				return []schemaflux.DecisionAnswer{schemafluxtest.Chose(decision.Questions[0].Options[index], 0.9)}, nil
			})
			resp, err := Redact()(d).Complete(t.Context(), schemaflux.Request{User: "u", Decision: choice("which?", first, second)})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Answers[0].Label != want {
				t.Errorf("option %d came back as %q, want %q", index+1, resp.Answers[0].Label, want)
			}
			sent := d.Last().Decision.Questions[0].Options
			if sent[0] == sent[1] || strings.Contains(sent[0]+sent[1], "@") {
				t.Errorf("the options were sent as %q: they cannot be told apart, or hold the address", sent)
			}
		}
	})

	t.Run("a decision with nothing to redact is sent as it is", func(t *testing.T) {
		t.Parallel()
		d := schemafluxtest.NewDecider(schemafluxtest.No())
		decision := yesNo(`"fine"`, "is it fine?")
		if _, err := Redact()(d).Complete(t.Context(), schemaflux.Request{Decision: decision}); err != nil {
			t.Fatal(err)
		}
		if d.Last().Decision.State != `"fine"` || d.Last().Decision.Questions[0].Ask != "is it fine?" {
			t.Errorf("the decision was sent as %+v", d.Last().Decision)
		}
	})

	t.Run("option values merge left to right with a later Marker that is not empty winning", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t, RedactOptions{Marker: "<one>", NoBuiltins: true}, RedactOptions{}, RedactOptions{Marker: "<two>", Patterns: phonePattern})
		send(ask(testEmail + " " + testPhone))
		if got := p.Last().User; got != testEmail+" <two>" {
			t.Errorf("the provider was sent %q", got)
		}
	})

	t.Run("a later options value adds rules and never drops those of an earlier one", func(t *testing.T) {
		t.Parallel()
		ssn := regexp.MustCompile(`\d{3}-\d{2}-\d{4}`)
		acct := regexp.MustCompile(`ACCT-\d+`)
		p, send := reached(t,
			RedactOptions{Patterns: map[string]*regexp.Regexp{"ssn": ssn}},
			RedactOptions{Patterns: map[string]*regexp.Regexp{"acct": acct}})
		send(ask("ssn 123-45-6789 and ACCT-99 for " + testEmail))
		got := p.Last().User
		mustNotContain(t, "the request", got, "123-45-6789", "ACCT-99", testEmail)
		if got != "ssn [redacted:ssn] and [redacted:acct] for [redacted:email]" {
			t.Errorf("the provider was sent %q", got)
		}
	})

	t.Run("a later rule of a name an earlier one has replaces it, and the rest are kept", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t,
			RedactOptions{NoBuiltins: true, Patterns: map[string]*regexp.Regexp{"id": regexp.MustCompile(`ID-\d+`), "other": regexp.MustCompile(`OTHER`)}},
			RedactOptions{Patterns: map[string]*regexp.Regexp{"id": regexp.MustCompile(`REF-\d+`)}})
		send(ask("ID-1 REF-2 OTHER"))
		if got := p.Last().User; got != "ID-1 [redacted:id] [redacted:other]" {
			t.Errorf("the provider was sent %q", got)
		}
	})

	t.Run("a later value that does not say NoBuiltins does not turn the built-ins back on", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t, RedactOptions{NoBuiltins: true}, RedactOptions{NoBuiltins: false})
		send(ask(testEmail))
		if got := p.Last().User; got != testEmail {
			t.Errorf("the provider was sent %q: the built-ins ran though the first value turned them off", got)
		}
	})

	t.Run("NoBuiltins in a later value is a configuration error that says where to set it", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t, RedactOptions{Marker: "<x>"}, RedactOptions{NoBuiltins: true})
		_, err := send(ask(testEmail))
		e := wantKind(t, err, schemaflux.ErrConfiguration)
		if e.Op != "mw.redact" || p.Calls() != 0 || !strings.Contains(e.Message, "first") {
			t.Errorf("Op %q, %d requests, message %q", e.Op, p.Calls(), e.Message)
		}
	})

	t.Run("the functions of every value are all run, the earlier first", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t,
			RedactOptions{NoBuiltins: true, Func: func(s string) string { return strings.ReplaceAll(s, "one", "1") }},
			RedactOptions{Func: func(s string) string { return strings.ReplaceAll(s, "1 two", "12") }})
		send(ask("one two"))
		if got := p.Last().User; got != "12" {
			t.Errorf("the provider was sent %q, want the first function's output given to the second", got)
		}
	})

	t.Run("a credential shape the doc names is removed", func(t *testing.T) {
		t.Parallel()
		for name, secret := range map[string]string{
			"a fine-grained GitHub token":              "github_pat_" + strings.Repeat("A1b2", 6) + "_" + strings.Repeat("Z9y8", 15),
			"a JWT":                                    "eyJ" + "hbGciOiJIUzI1NiJ9" + "." + "eyJ" + "zdWIiOiIxMjM0NTY3ODkwIn0" + "." + "dBjftJeZ4CVPmB92K27uhbUJU1p1r",
			"a live Stripe key":                        "sk_" + "live_" + strings.Repeat("a1B2", 6),
			"an address with an accent":                "jänë@example.com",
			"an address at a domain of its own script": "jane@bücher.de",
		} {
			p, send := reached(t)
			send(ask("here: " + secret + " ok"))
			if got := p.Last().User; strings.Contains(got, secret) || !strings.Contains(got, "[redacted:") {
				t.Errorf("%s: the provider was sent %q", name, got)
			}
		}
	})

	t.Run("an authorization header inside JSON is still JSON when it is sent", func(t *testing.T) {
		t.Parallel()
		p, send := reached(t)
		send(schemaflux.Request{Decision: yesNo(`{"Authorization": "Bearer abc123"}`, "is it?")})
		got := p.Last().Decision.State
		if got != `{"Authorization": "Bearer [redacted:bearer-header]"}` || !json.Valid([]byte(got)) {
			t.Errorf("the state was sent as %q", got)
		}
	})
}
