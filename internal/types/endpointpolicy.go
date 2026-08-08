package types

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// SEC-004. Custom endpoint policy.
//
// The library accepts a caller- or configuration-supplied BaseURL for every
// OpenAI-compatible provider (internal/llm/provider.go's ProviderConfig.
// BaseURL, consumed by chatcompletions.go, embeddings.go, and provider.go's
// request builders) and validates nothing about it today -- a config value
// or a caller option can point a request at loopback, link-local, or a cloud
// metadata service exactly as easily as at a real provider.
//
// EndpointPolicy and ValidateEndpoint are the check. They are NOT wired into
// internal/llm/provider.go, chatcompletions.go, embeddings.go, client.go, or
// internal/ops/configvalidate.go here: every one of those is outside this
// task's edit scope (the task's own file list permits internal/telemetry/
// logger.go, internal/types/policy.go, and new files under internal/types/,
// internal/ops/, or mw/ -- provider.go and client.go are explicitly excluded,
// and chatcompletions.go/embeddings.go/configvalidate.go are not on the
// permitted list either). ValidateEndpoint is written so wiring it in is one
// call at whichever of those layers eventually owns "reject a BaseURL before
// using it" -- see the doc comment on ValidateEndpoint for the call shape --
// but that call does not exist yet. This is a real limitation, not a
// completed task: SEC-004 asks for a policy that stops requests from
// reaching internal infrastructure, and until something calls
// ValidateEndpoint on the way to a request, nothing does.
//
// # Never fail open
//
// Consistent with DataPolicy's AllowsProvider/AllowsRegion convention
// (policy.go): an UNDECLARED AllowedSchemes or AllowedHosts list makes every
// scheme or host ineligible, not unrestricted. A policy that names nothing
// as allowed has not "left it open" -- it has refused everything, and a
// caller who wants a specific custom endpoint permitted must say so.
//
// The private-address rule is separate from, and runs in addition to, the
// host allowlist: an operator who accidentally allowlists "127.0.0.1" (or a
// hostname that happens to resolve there) does not thereby get to point
// requests at loopback. AllowPrivateAddresses is the one, explicit,
// separately-named override for that -- allowlisting the host is not enough
// by itself.
type EndpointPolicy struct {
	// AllowedSchemes is the exhaustive list of URL schemes a custom endpoint
	// may use, compared case-insensitively (e.g. "https"). Empty means
	// undeclared -- every scheme is refused. There is no implicit "https is
	// always fine": a policy that wants to allow it says so.
	AllowedSchemes []string

	// AllowedHosts is the exhaustive list of hostnames (no port, no
	// scheme, no path) a custom endpoint may target, compared
	// case-insensitively. Empty means undeclared -- every host is refused.
	//
	// This is an exact-match allowlist, not a suffix or wildcard match:
	// "api.openai.com" does not implicitly permit "evil.api.openai.com.
	// attacker.example", which a suffix match would.
	AllowedHosts []string

	// AllowPrivateAddresses opts back into a host that ValidateEndpoint
	// would otherwise refuse under the private-address rule -- loopback,
	// link-local (including the 169.254.169.254 cloud metadata address),
	// unspecified, and RFC 1918/4193 private ranges. False (the zero value)
	// is the safe default: a policy nobody configured refuses all of these,
	// even if the exact address also appears in AllowedHosts.
	AllowPrivateAddresses bool
}

// ErrEndpointNotAllowed is the typed configuration error ValidateEndpoint
// returns for every refusal. A caller (or a future call site that wires this
// in) can distinguish "this endpoint is not permitted" from a malformed-URL
// parse failure via errors.Is, without string-matching a message -- the same
// discipline AGENTS.md requires of internal/llm/classify.go's error
// taxonomy, applied to this policy's one failure mode.
var ErrEndpointNotAllowed = errors.New("endpoint not allowed by policy")

// ValidateEndpoint reports whether rawURL is permitted under p, returning
// ErrEndpointNotAllowed (wrapped with the specific reason) when it is not.
//
// It performs no DNS resolution and makes no network call: a hostname like
// "internal-metadata.example" is judged only by AllowedHosts membership, not
// by resolving it to an address and inspecting that -- doing otherwise would
// make endpoint validation itself a network operation, and AGENTS.md's "a
// plain go test ./... must make zero network calls" applies to this check
// exactly as much as to a provider call. An IP-literal host (rawURL's host
// is already an address, e.g. "http://127.0.0.1:8080/v1") is judged
// directly against the private-address ranges below, since no resolution is
// needed to know what it is.
//
// The intended call shape at whichever layer eventually wires this in is:
//
//	if err := policy.ValidateEndpoint(config.BaseURL); err != nil {
//	    return nil, fmt.Errorf("configuring provider %q: %w", name, err)
//	}
//
// before config.BaseURL is used to build any request.
func (p EndpointPolicy) ValidateEndpoint(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		// An empty BaseURL means "use the provider's default", not a
		// caller-supplied custom endpoint -- there is nothing here for this
		// policy to judge, and refusing it would make every provider that
		// does not set a custom endpoint fail configuration, the opposite of
		// what SEC-004 is for.
		return nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: %q does not parse as a URL", ErrEndpointNotAllowed, redactURLForError(trimmed))
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: %q has no host", ErrEndpointNotAllowed, redactURLForError(trimmed))
	}

	if !containsFold(p.AllowedSchemes, parsed.Scheme) {
		return fmt.Errorf("%w: scheme %q is not on the allowlist", ErrEndpointNotAllowed, redactURLForError(parsed.Scheme))
	}

	hostname := parsed.Hostname()
	if !containsFold(p.AllowedHosts, hostname) {
		return fmt.Errorf("%w: host %q is not on the allowlist", ErrEndpointNotAllowed, redactURLForError(hostname))
	}

	if !p.AllowPrivateAddresses {
		if reason := privateAddressReason(hostname); reason != "" {
			return fmt.Errorf("%w: host %q %s (set AllowPrivateAddresses to permit it)", ErrEndpointNotAllowed, redactURLForError(hostname), reason)
		}
	}

	return nil
}

// cloudMetadataAddresses are the well-known cloud-provider metadata service
// addresses -- 169.254.169.254 answers on AWS, Azure, and GCP alike, and
// fd00:ec2::254 is AWS's IMDSv2 IPv6 equivalent. These are link-local
// addresses net.IP.IsLinkLocalUnicast already flags, named separately only
// so the refusal reason says "cloud metadata service" instead of the more
// generic "link-local address" when it is specifically this address --
// SEC-004 names the metadata service as its own concern, not an incidental
// case of the link-local rule.
var cloudMetadataAddresses = map[string]bool{
	"169.254.169.254": true,
	"fd00:ec2::254":   true,
}

// privateAddressReason reports why hostname (already an IP literal, or a
// name this function recognises without resolving it) falls under the
// private-address rule, or "" if it does not. See ValidateEndpoint's doc
// comment for why this never resolves a name via DNS.
func privateAddressReason(hostname string) string {
	lower := strings.ToLower(strings.TrimSpace(hostname))

	// Names ValidateEndpoint refuses without resolution: not real DNS
	// results, but well-known local/internal conventions a policy should
	// not have to enumerate in AllowedHosts to be safe against.
	switch lower {
	case "localhost":
		return "is the loopback hostname"
	case "metadata", "metadata.google.internal":
		return "is a cloud metadata hostname"
	}
	if strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".localhost") {
		return "uses an internal-only domain suffix"
	}

	ip := net.ParseIP(hostname)
	if ip == nil {
		// Not an IP literal and not one of the recognised names above --
		// nothing more to check without a DNS lookup this function does not
		// perform.
		return ""
	}

	if cloudMetadataAddresses[ip.String()] {
		return "is a cloud metadata service address"
	}
	switch {
	case ip.IsLoopback():
		return "is a loopback address"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "is a link-local address"
	case ip.IsUnspecified():
		return "is the unspecified address"
	case ip.IsPrivate():
		return "is a private-range address"
	}
	return ""
}

// redactURLForError returns raw truncated to a bounded length for inclusion
// in an error string. A URL a caller configured is not the same class of
// secret CaptureDiagnostic guards against (it is configuration, not request
// content), but AGENTS.md's "no request or response body in an error string"
// still argues for bounding it rather than echoing an arbitrarily long value
// verbatim -- a malformed "URL" could be megabytes of unrelated text handed
// to url.Parse by mistake.
func redactURLForError(raw string) string {
	const maxLen = 200
	if len(raw) <= maxLen {
		return raw
	}
	return raw[:maxLen] + "...(truncated)"
}
