package mw

import (
	"strings"
	"testing"
)

// SEC-003. Fuzz target for RedactEgress's scrubbing pass -- the live
// outbound-request redaction point (see redact.go's package doc comment for
// why this list is a separate, deliberately narrow copy from
// schemafluxtest's and internal/types/diagnostics.go's). It runs entirely
// offline: (*redactingProvider).redact is pure regex substitution over a
// string, with no provider call anywhere on its path.
//
// Seeds are hand-chosen from builtinRedactPatterns' own shapes (one real
// example per pattern, so the fuzzer starts already knowing what a match
// looks like) plus adversarial shapes named in SEC-003: overlapping
// matches, a credential shape split across what would be a regex boundary,
// malformed UTF-8, and a very long input (this middleware runs on every
// egress call, so pathological slowdown here is a production availability
// concern, not just a correctness one).
// privateKeyBodySeed stands for the base64 body of a private key in the seeds.
const privateKeyBodySeed = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7"

func FuzzRedact(f *testing.F) {
	seeds := []string{
		"",
		"sk-proj-abcdefghijklmnopqrstuvwxyz012345",                                                              // secret-scan: allow
		"sk-ant-abcdefghijklmnopqrstuvwxyz012345",                                                               // secret-scan: allow
		"AIzaSyAbcdefghijklmnopqrstuvwxyz0123456",                                                               // secret-scan: allow
		"AKIAABCDEFGHIJKLMNOP",                                                                                  // secret-scan: allow
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",                                                              // secret-scan: allow
		"xoxb-1234567890-abcdefghij",                                                                            // secret-scan: allow
		"-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK...\n-----END RSA PRIVATE KEY-----",                     // secret-scan: allow
		"-----BEGIN PRIVATE KEY-----\n" + privateKeyBodySeed + "\n-----END PRIVATE KEY-----",                    // secret-scan: allow
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" + privateKeyBodySeed,                                            // a block with no END line // secret-scan: allow
		"before -----BEGIN EC PRIVATE KEY-----\n" + privateKeyBodySeed + "\n-----END EC PRIVATE KEY----- after", // secret-scan: allow
		"Authorization: Bearer abcdef0123456789",
		"contact jane.doe@example.com about the invoice",
		"two secrets: sk-proj-abcdefghijklmnopqrstuvwxyz and gh" + "p_abcdefghijklmnopqrstuvwxyz0123456789", // secret-scan: allow
		"an invoice for $1,284.50 due on 2026-08-07, order #ORD-99182",
		strings.Repeat("sk-proj-abcdefghijklmnopqrstuvwxyz01234 ", 500),                     // repeated matches, throughput case // secret-scan: allow
		"prefix \xff\xfe invalid utf-8 \x80 sk-ant-abcdefghijklmnopqrstuvwxyz012345 suffix", // secret-scan: allow
		"AUTHORIZATION:BEARER nospacebetweenheadervalue",
		"nested \"sk-proj-abcdefghijklmnopqrstuvwxyz\" inside quotes and a bearer token: Bearer xyz", // secret-scan: allow
	}
	for _, s := range seeds {
		f.Add(s)
	}

	provider := RedactEgress()(nil).(*redactingProvider)

	f.Fuzz(func(t *testing.T, input string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("redact panicked on %q: %v", input, r)
			}
		}()
		out := provider.redact(input)

		// The redacted output must never still contain a literal API-key
		// shaped substring the built-ins claim to catch -- run the same
		// patterns against the output and confirm none still match. This is
		// the actual correctness property (not just "did not panic"): a
		// scrub that only handles the first occurrence, or that a marker
		// substitution re-triggers a later pattern on, would fail this.
		for _, entry := range builtinRedactPatterns {
			if entry.pattern.MatchString(out) {
				t.Fatalf("redact(%q) = %q still matches pattern %q", input, out, entry.name)
			}
		}

		// The key material of a private key goes with its header. The seed
		// blocks carry a body the other properties cannot see: a header-only
		// match leaves it behind and still satisfies the check above.
		if strings.Contains(input, privateKeyBodySeed) && strings.Contains(out, privateKeyBodySeed) {
			t.Fatalf("redact(%q) = %q left the key material in the text", input, out)
		}
	})
}
