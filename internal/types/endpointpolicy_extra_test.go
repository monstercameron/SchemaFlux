package types

import "testing"

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
