package types

import (
	"errors"
	"strings"
	"testing"
)

func TestEndpointPolicyValidateEndpoint(t *testing.T) {
	openPolicy := EndpointPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"api.openai.com", "api.example.com"},
	}
	privatePolicy := EndpointPolicy{
		AllowedSchemes:        []string{"http", "https"},
		AllowedHosts:          []string{"127.0.0.1", "internal-gateway.svc"},
		AllowPrivateAddresses: true,
	}

	cases := []struct {
		name    string
		policy  EndpointPolicy
		url     string
		wantErr bool
	}{
		{
			name:    "allowed https host passes",
			policy:  openPolicy,
			url:     "https://api.openai.com/v1",
			wantErr: false,
		},
		{
			name:    "empty URL passes (no custom endpoint configured)",
			policy:  openPolicy,
			url:     "",
			wantErr: false,
		},
		{
			name:    "whitespace-only URL passes, same as empty",
			policy:  openPolicy,
			url:     "   ",
			wantErr: false,
		},
		{
			name:    "disallowed scheme is refused",
			policy:  openPolicy,
			url:     "http://api.openai.com/v1",
			wantErr: true,
		},
		{
			name:    "disallowed host is refused",
			policy:  openPolicy,
			url:     "https://evil.example.com/v1",
			wantErr: true,
		},
		{
			name:    "an undeclared allowlist refuses everything, not open by default",
			policy:  EndpointPolicy{},
			url:     "https://api.openai.com/v1",
			wantErr: true,
		},
		{
			name:    "loopback IP literal is refused without AllowPrivateAddresses",
			policy:  EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"127.0.0.1"}},
			url:     "http://127.0.0.1:8080/v1",
			wantErr: true,
		},
		{
			name:    "localhost hostname is refused without AllowPrivateAddresses",
			policy:  EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"localhost"}},
			url:     "http://localhost:11434/v1",
			wantErr: true,
		},
		{
			name:    "link-local metadata address is refused without AllowPrivateAddresses",
			policy:  EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"169.254.169.254"}},
			url:     "http://169.254.169.254/latest/meta-data/",
			wantErr: true,
		},
		{
			name:    "an .internal suffix hostname is refused without AllowPrivateAddresses",
			policy:  EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"gateway.internal"}},
			url:     "https://gateway.internal/v1",
			wantErr: true,
		},
		{
			name:    "private RFC1918 range is refused without AllowPrivateAddresses",
			policy:  EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"10.0.0.5"}},
			url:     "http://10.0.0.5:8080/v1",
			wantErr: true,
		},
		{
			name:    "loopback IP literal passes WITH AllowPrivateAddresses and a matching allowlist entry",
			policy:  privatePolicy,
			url:     "http://127.0.0.1:8080/v1",
			wantErr: false,
		},
		{
			name:    "malformed URL is refused",
			policy:  openPolicy,
			url:     "https://%zz",
			wantErr: true,
		},
		{
			name:    "a URL with no host is refused",
			policy:  openPolicy,
			url:     "https:///path-only",
			wantErr: true,
		},
		{
			name:    "public host allowed even when AllowPrivateAddresses is set on a different policy",
			policy:  privatePolicy,
			url:     "http://evil.example.com/v1",
			wantErr: true, // not on privatePolicy's AllowedHosts
		},
		{
			name:    "IPv6 loopback is refused without AllowPrivateAddresses",
			policy:  EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"::1"}},
			url:     "http://[::1]:8080/v1",
			wantErr: true,
		},
		{
			name:    "unspecified address 0.0.0.0 is refused without AllowPrivateAddresses",
			policy:  EndpointPolicy{AllowedSchemes: []string{"http"}, AllowedHosts: []string{"0.0.0.0"}},
			url:     "http://0.0.0.0:9000/v1",
			wantErr: true,
		},
		{
			name:    "scheme comparison is case-insensitive",
			policy:  EndpointPolicy{AllowedSchemes: []string{"HTTPS"}, AllowedHosts: []string{"api.example.com"}},
			url:     "https://api.example.com/v1",
			wantErr: false,
		},
		{
			name:    "host comparison is case-insensitive",
			policy:  EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"API.EXAMPLE.COM"}},
			url:     "https://api.example.com/v1",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.ValidateEndpoint(tc.url)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateEndpoint(%q) = nil, want an error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateEndpoint(%q) = %v, want nil", tc.url, err)
			}
			if tc.wantErr && !errors.Is(err, ErrEndpointNotAllowed) {
				t.Fatalf("ValidateEndpoint(%q) error %v does not wrap ErrEndpointNotAllowed", tc.url, err)
			}
		})
	}
}

// A refusal never echoes the full raw URL unbounded -- redactURLForError
// bounds it. This is a narrower guarantee than "no content in an error"
// (a configured BaseURL is not caller request content), but the same
// AGENTS.md instinct applies: bound what an error string can carry.
func TestEndpointPolicyErrorBoundsLongInput(t *testing.T) {
	policy := EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"api.example.com"}}
	huge := "https://" + strings.Repeat("a", 10000) + ".example.com/v1"

	err := policy.ValidateEndpoint(huge)
	if err == nil {
		t.Fatalf("expected an error for a disallowed huge host")
	}
	if len(err.Error()) > 400 {
		t.Fatalf("error string is %d bytes, want it bounded: %.60s...", len(err.Error()), err.Error())
	}
}

// A malformed "URL" that is actually pages of unrelated text must not panic
// url.Parse or ValidateEndpoint, and must still refuse.
func TestEndpointPolicyRejectsGarbageWithoutPanicking(t *testing.T) {
	policy := EndpointPolicy{AllowedSchemes: []string{"https"}, AllowedHosts: []string{"api.example.com"}}
	garbage := []string{
		"\x00\x01\x02not a url",
		strings.Repeat("not-a-url ", 5000),
		"://missing-scheme",
		"https://[::1",
	}
	for _, g := range garbage {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ValidateEndpoint panicked on %.40q: %v", g, r)
				}
			}()
			if err := policy.ValidateEndpoint(g); err == nil {
				t.Fatalf("expected an error for garbage input %.40q", g)
			}
		}()
	}
}
