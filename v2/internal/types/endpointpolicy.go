package types

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SEC-004. Custom endpoint policy.
//
// The library accepts a caller- or configuration-supplied BaseURL for every
// OpenAI-compatible provider (internal/llm/provider.go's ProviderConfig.
// BaseURL), and a config value or a caller option can point a request at
// loopback, link-local, or a cloud metadata service exactly as easily as at a
// real provider. EndpointPolicy and ValidateEndpoint are the check: a provider
// built with ProviderConfig.EndpointPolicy set calls ValidateEndpoint on its
// BaseURL before it builds any client. A provider built with none enforces
// nothing, which is the default.
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

// refuseEndpoint builds ValidateEndpoint's refusal: a configuration error, of kind
// KindConfiguration, that errors.Is also matches to ErrEndpointNotAllowed.
func refuseEndpoint(format string, args ...any) error {
	return &OperationError{
		Kind:    KindConfiguration,
		Op:      "endpoint policy",
		Message: fmt.Sprintf(format, args...),
		Cause:   ErrEndpointNotAllowed,
	}
}

// ValidateEndpoint reports whether rawURL is permitted under p, returning a
// configuration error that wraps ErrEndpointNotAllowed, with the specific
// reason, when it is not.
//
// The error describes the URL and does not repeat it. A BaseURL may carry a
// user name and password, or a key in its query, and the error is logged by
// whoever configured the provider: it names the scheme and the host, which the
// policy judged, and for a value that does not parse, or has no host, only its
// size.
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
// The call shape is:
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
		return refuseEndpoint("the value (%d bytes) does not parse as a URL", len(trimmed))
	}
	if parsed.Host == "" {
		return refuseEndpoint("the value (%d bytes) has no host", len(trimmed))
	}

	if !containsFold(p.AllowedSchemes, parsed.Scheme) {
		return refuseEndpoint("scheme %q is not on the allowlist", boundedForError(parsed.Scheme))
	}

	hostname := parsed.Hostname()
	if !containsFold(p.AllowedHosts, hostname) {
		return refuseEndpoint("host %q is not on the allowlist", boundedForError(hostname))
	}

	if !p.AllowPrivateAddresses {
		if reason := privateAddressReason(hostname); reason != "" {
			return refuseEndpoint("host %q %s (set AllowPrivateAddresses to permit it)", boundedForError(hostname), reason)
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
//
// The host is normalised first, so a spelling of an address that net.ParseIP
// does not read as one is not a way round the rule: a trailing dot
// ("localhost."), a zone ("fe80::1%eth0"), and the numeric forms an IPv4 address
// has in a URL ("2130706433", "0x7f.1", "0177.0.0.1" are all 127.0.0.1).
func privateAddressReason(hostname string) string {
	lower := strings.ToLower(strings.TrimSpace(hostname))
	lower = strings.TrimSuffix(lower, ".")

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

	ip := parseHostIP(lower)
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
	case sharedAddressSpace.Contains(ip):
		return "is a shared (carrier NAT) address"
	}
	return ""
}

// sharedAddressSpace is 100.64.0.0/10, the range carriers use for NAT. It is not
// private in RFC 1918's sense and is not reachable from the internet, and a
// cloud provider's metadata service answers in it (100.100.100.200).
var sharedAddressSpace = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// parseHostIP reads host as an IP address, or returns nil. It reads what
// net.ParseIP reads, with a zone removed, and the other numeric spellings of an
// IPv4 address that the platform's resolvers accept: one to four parts, each
// decimal, octal (leading 0) or hexadecimal (leading 0x), the last part filling
// the remaining bytes.
func parseHostIP(host string) net.IP {
	if percent := strings.IndexByte(host, '%'); percent >= 0 {
		host = host[:percent]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}

	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil
	}
	var values [4]uint64
	for i, part := range parts {
		if part == "" {
			return nil
		}
		value, err := strconv.ParseUint(part, 0, 32)
		if err != nil {
			return nil
		}
		values[i] = value
	}

	// All but the last part are one byte each; the last fills the rest.
	var address uint64
	for i := 0; i < len(parts)-1; i++ {
		if values[i] > 255 {
			return nil
		}
		address |= values[i] << (8 * uint(3-i))
	}
	remaining := uint(4 - (len(parts) - 1))
	if remaining < 4 && values[len(parts)-1] >= 1<<(8*remaining) {
		return nil
	}
	address |= values[len(parts)-1]
	return net.IPv4(byte(address>>24), byte(address>>16), byte(address>>8), byte(address))
}

// boundedForError returns text cut to a length that is safe to put in an error,
// at a character boundary. A scheme or a host comes from a parsed URL, but what
// was handed to the parser may have been megabytes of unrelated text.
func boundedForError(text string) string {
	const maxLen = 100
	if len(text) <= maxLen {
		return text
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "...(truncated)"
}
