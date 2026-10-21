package types

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// privateAddressReason's remaining branches: the two hardcoded metadata
// hostnames and the .localhost suffix, none of which endpointpolicy_test.go
// exercises (it covers .internal, loopback, link-local, RFC1918, and
// unspecified).
func TestPrivateAddressReasonCoversMetadataNamesAndLocalhostSuffix(t *testing.T) {
	cases := []struct {
		hostname  string
		wantEmpty bool
	}{
		{"metadata", false},
		{"metadata.google.internal", false},
		{"box.localhost", false},
		{"api.example.com", true}, // a real public-looking name is not flagged
	}
	for _, tc := range cases {
		t.Run(tc.hostname, func(t *testing.T) {
			reason := privateAddressReason(tc.hostname)
			if (reason == "") != tc.wantEmpty {
				t.Errorf("privateAddressReason(%q) = %q, wantEmpty=%v", tc.hostname, reason, tc.wantEmpty)
			}
		})
	}
}

// The private-address rule holds for every spelling of an address, not only the
// ones net.ParseIP reads. An allowlisted host written another way used to pass.
func TestPrivateAddressReasonSeesThroughOtherSpellings(t *testing.T) {
	cases := []struct {
		hostname string
		private  bool
	}{
		{"127.0.0.1", true},
		{"localhost.", true},
		{"LOCALHOST", true},
		{"localhost", true},
		{"metadata.google.internal.", true},
		{"fe80::1%eth0", true},
		{"fe80::1", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"2130706433", true},
		{"0x7f000001", true},
		{"0x7f.1", true},
		{"0177.0.0.1", true},
		{"127.1", true},
		{"127.0.1", true},
		{"0", true},
		{"10.1", true},
		{"0xa.0.0.1", true},
		{"3232235777", true},
		{"169.254.169.254", true},
		{"2852039166", true},
		{"100.100.100.200", true},
		{"100.64.0.1", true},
		{"100.127.255.254", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"100.63.255.255", false},
		{"100.128.0.1", false},
		{"8.8.8.8", false},
		{"134744072", false},
		{"0x08080808", false},
		{"256.1.1.1", false},
		{"1.2.3.4.5", false},
		{"0x", false},
		{"08.8.8.8", false},
		{"api.example.com", false},
		{"example.com.", false},
		{"2001:4860:4860::8888", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.hostname, func(t *testing.T) {
			if got := privateAddressReason(tc.hostname) != ""; got != tc.private {
				t.Errorf("privateAddressReason(%q) private = %v, want %v", tc.hostname, got, tc.private)
			}
		})
	}
}

// The end-to-end refusal through ValidateEndpoint for the same two cases,
// so the policy-level behaviour (not just the helper) is proven.
func TestValidateEndpointRefusesMetadataHostnameAndLocalhostSuffix(t *testing.T) {
	policy := EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"metadata", "box.localhost"}}

	if err := policy.ValidateEndpoint("http://metadata/computeMetadata/v1/"); err == nil {
		t.Error("ValidateEndpoint allowed the cloud metadata hostname without AllowPrivateAddresses")
	}
	if err := policy.ValidateEndpoint("http://box.localhost:8080/v1"); err == nil {
		t.Error("ValidateEndpoint allowed a .localhost hostname without AllowPrivateAddresses")
	}
}

// An allowlisted host does not escape the rule by being spelled another way, and
// AllowPrivateAddresses still opts back in.
func TestValidateEndpointRefusesAnAllowlistedPrivateHostInAnySpelling(t *testing.T) {
	for _, host := range []string{"localhost.", "2130706433", "0x7f.1", "100.100.100.200", "[fe80::1%25eth0]"} {
		t.Run(host, func(t *testing.T) {
			name := strings.Trim(host, "[]")
			name = strings.Replace(name, "%25", "%", 1)
			closed := EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{name}}
			if err := closed.ValidateEndpoint("http://" + host + "/v1"); !errors.Is(err, ErrEndpointNotAllowed) {
				t.Errorf("ValidateEndpoint = %v, want a refusal", err)
			}
			open := closed
			open.AllowPrivateAddresses = true
			if err := open.ValidateEndpoint("http://" + host + "/v1"); err != nil {
				t.Errorf("AllowPrivateAddresses did not permit the host: %v", err)
			}
		})
	}
}

// The refusal describes the URL and does not repeat it: a BaseURL can hold a user
// name and password or a key in its query, and the error is logged.
func TestEndpointRefusalNeverRepeatsTheURL(t *testing.T) {
	policy := EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"api.example.com"}}
	secret := "sk-" + "live-0123456789abcdefghij"
	cases := []struct {
		name string
		raw  string
	}{
		{"credentials with a bad escape", "https://user:" + secret + "@host.example/%zz"},
		{"a key in the query and no scheme", "api.example.com:8080/v1?key=" + secret},
		{"credentials on a host that is not allowed", "https://user:" + secret + "@other.example.com/v1"},
		{"credentials on a scheme that is not allowed", "ftp://user:" + secret + "@api.example.com/v1"},
		{"a key in the query of a host not allowed", "https://other.example.com/v1?api_key=" + secret},
		{"a key in the path with no host", "https:///" + secret},
		{"a key in the fragment", "https://other.example.com/v1#" + secret},
		{"a key as the whole value", secret},
		{"a key in a malformed port", "https://api.example.com:" + secret + "/v1"},
		{"a key as the user of a private address", "http://" + secret + "@127.0.0.1/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := policy.ValidateEndpoint(tc.raw)
			if err == nil {
				t.Fatalf("ValidateEndpoint(%q) = nil, want a refusal", tc.raw)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "user:") {
				t.Errorf("the error repeats part of the URL: %s", err)
			}
			if !errors.Is(err, ErrEndpointNotAllowed) {
				t.Errorf("the error does not match ErrEndpointNotAllowed: %v", err)
			}
		})
	}
}

// The scheme in a refusal is cut to a length that is safe to log, as the host is.
func TestValidateEndpointDoesNotEchoAnUnboundedScheme(t *testing.T) {
	policy := EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"api.example.com"}}
	long := strings.Repeat("a", 500)
	err := policy.ValidateEndpoint(long + "://api.example.com/v1")
	if err == nil {
		t.Fatal("a scheme that is not on the allowlist was accepted")
	}
	if strings.Contains(err.Error(), long) {
		t.Errorf("the refusal repeats all 500 characters of the scheme (%d bytes of error)", len(err.Error()))
	}
}

// The refusal is a classified configuration error that still matches its own
// sentinel, so one taxonomy covers it.
func TestEndpointRefusalIsAClassifiedConfigurationError(t *testing.T) {
	err := EndpointPolicy{}.ValidateEndpoint("https://api.example.com/v1")
	if KindOf(err) != KindConfiguration || !errors.Is(err, ErrConfiguration) || !errors.Is(err, ErrEndpointNotAllowed) {
		t.Errorf("kind %v, ErrConfiguration %v, ErrEndpointNotAllowed %v", KindOf(err), errors.Is(err, ErrConfiguration), errors.Is(err, ErrEndpointNotAllowed))
	}
	if errors.Is(err, ErrPolicyViolation) {
		t.Error("the refusal also claims to be a data policy violation")
	}
}

func TestBoundedForErrorCutsAtACharacterBoundary(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		cut     bool
		keepLen int // the length of the kept prefix, when cut
	}{
		{"empty", "", false, 0},
		{"short ascii", "short", false, 0},
		{"exactly the limit", strings.Repeat("a", 100), false, 0},
		{"one over the limit", strings.Repeat("a", 101), true, 100},
		{"far over the limit", strings.Repeat("a", 100000), true, 100},
		{"two-byte characters", strings.Repeat("\u00e9", 200), true, 100},
		{"three-byte characters, cut inside one", strings.Repeat("\u20ac", 100), true, 99},
		{"four-byte characters, cut inside one", strings.Repeat("\U0001F600", 100), true, 100},
		{"a multibyte character straddling the limit", strings.Repeat("a", 99) + "\u00e9" + "tail", true, 99},
		{"multibyte text under the limit", strings.Repeat("\u00e9", 50), false, 0},
		{"multibyte text exactly at the limit", strings.Repeat("\u00e9", 50) + "", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := boundedForError(tc.in)
			if !utf8.ValidString(got) {
				t.Fatalf("the cut split a character: %q", got)
			}
			if !tc.cut {
				if got != tc.in {
					t.Errorf("a value within the limit was changed: %q", got)
				}
				return
			}
			if !strings.HasSuffix(got, "...(truncated)") {
				t.Errorf("a cut value does not say so: %q", got)
			}
			kept := strings.TrimSuffix(got, "...(truncated)")
			if len(kept) != tc.keepLen || !strings.HasPrefix(tc.in, kept) {
				t.Errorf("kept %d bytes, want %d, as a prefix of the input", len(kept), tc.keepLen)
			}
		})
	}
}
