package types

import (
	"errors"
	"testing"
)

// SEC-003/SEC-004. Fuzz target for EndpointPolicy.ValidateEndpoint: pure URL
// parsing and string comparison, no network call on its path (see
// ValidateEndpoint's own doc comment for why it deliberately never resolves
// a hostname).
func FuzzValidateEndpoint(f *testing.F) {
	seeds := []string{
		"",
		"https://api.openai.com/v1",
		"http://127.0.0.1:8080/v1",
		"http://localhost/v1",
		"http://169.254.169.254/latest/meta-data/",
		"https://gateway.internal/v1",
		"https://%zz",
		"https:///no-host",
		"://missing-scheme",
		"http://[::1]:8080/v1",
		"http://[::1",
		"https://user:pass@api.example.com/v1",
		"https://api.example.com:99999/v1", // out-of-range port
		"file:///etc/passwd",
		"https://" + string([]byte{0xff, 0xfe}) + ".example.com/v1",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	policy := EndpointPolicy{
		AllowedSchemes: []string{"https"},
		AllowedHosts:   []string{"api.openai.com", "api.example.com"},
	}

	f.Fuzz(func(t *testing.T, rawURL string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ValidateEndpoint panicked on %q: %v", rawURL, r)
			}
		}()
		// The only property checked offline: never panics, and a returned
		// error always wraps ErrEndpointNotAllowed (the typed-refusal
		// contract SEC-004 asks for) rather than some other error shape
		// leaking out of url.Parse unwrapped.
		if err := policy.ValidateEndpoint(rawURL); err != nil {
			if !errors.Is(err, ErrEndpointNotAllowed) {
				t.Fatalf("ValidateEndpoint(%q) returned an error not wrapping ErrEndpointNotAllowed: %v", rawURL, err)
			}
		}
	})
}
