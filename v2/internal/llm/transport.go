package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// LAT-002. Connections.

// The idle connections the shared transport keeps to one host. A program that
// asks a dozen things at once and then a dozen more should find its
// connections still open. Over HTTP/2 one connection carries them all and
// this does not matter; over HTTP/1.1, which a local server and some gateways
// speak, Go's default of two meant every burst but the first two calls opened
// a connection and closed it again.
const (
	sharedIdleConnsPerHost = 128
	sharedIdleConns        = 512
)

// warmTimeout bounds a warming request whose context has no deadline of
// its own. A server that accepts the connection and never answers would
// otherwise hold the caller for as long as the provider's client allows, which
// for a client with no timeout is for ever. It is a variable so that a test can
// shorten it.
var warmTimeout = 10 * time.Second

// sharedTransport is the transport of every provider whose caller supplied no
// HTTP client. It is one transport, so that two clients of the same provider
// share connections, and it is this library's own copy: http.DefaultTransport
// belongs to the whole process, and its limits are not this library's to set.
//
// It is a copy of http.DefaultTransport as that stands when a provider is
// built. A program that has put its own transport there, a proxy or a recorder
// or a test's guard, before building its providers is followed, and the copy is
// made again when the default is found to have been replaced by another value.
// A default that is changed in place afterwards, the same pointer with a new
// Proxy or TLSClientConfig, is not followed by a provider that was built
// already, nor by one built later: the copy is not refreshed because the
// default compares equal to the one it was made from.
//
// The copy keeps every setting of the default. It raises the idle connections
// kept to this library's own where the default keeps fewer, and leaves them
// where the default keeps more: a program that tuned its transport for its own
// network, a shorter idle timeout for a NAT that drops quiet connections say,
// meant it, and a longer one would make a dead pooled connection, which costs a
// retry, more likely.
func sharedTransport() http.RoundTripper {
	shared.Lock()
	defer shared.Unlock()
	if shared.tuned != nil && sameTransport(shared.from, http.DefaultTransport) {
		return shared.tuned
	}
	shared.from = http.DefaultTransport
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// Not a transport this package can copy and tune. It is used as it
		// is.
		shared.tuned = http.DefaultTransport
		return shared.tuned
	}
	tuned := base.Clone()
	// Zero is Go's own default for the count per host, which is two. For the
	// total it is no limit at all, which is already more than this library's.
	if tuned.MaxIdleConnsPerHost < sharedIdleConnsPerHost {
		tuned.MaxIdleConnsPerHost = sharedIdleConnsPerHost
	}
	if tuned.MaxIdleConns != 0 && tuned.MaxIdleConns < sharedIdleConns {
		tuned.MaxIdleConns = sharedIdleConns
	}
	shared.tuned = tuned
	return shared.tuned
}

// sameTransport reports whether two transports are the same value. A
// RoundTripper can be a function type, or a struct holding a slice or a map,
// and comparing two interfaces that hold the same uncomparable type panics:
// the second provider built after a test had installed such a stub as the
// default took the process down. Such a value is reported as not the same, which
// makes sharedTransport take it as the new default and use it as it is.
func sameTransport(a, b http.RoundTripper) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

var shared struct {
	sync.Mutex
	from  http.RoundTripper
	tuned http.RoundTripper
}

// Warmer is a provider that can open its connection ahead of its first call.
type Warmer interface {
	// Warm opens a connection to the provider and leaves it ready for the
	// next call. It sends no prompt and costs nothing.
	Warm(ctx context.Context) error
}

// providerUnwrapper is a provider that wraps another, such as the handlers of the mw
// package, and says which.
type providerUnwrapper interface {
	Unwrap() Provider
}

// Warm opens a provider's connection ahead of its first call, when the
// provider can: the name lookup, the connection and the handshake are paid
// here and not by the first request. It reports whether there was anything to
// warm. A provider with no connection to open, one that runs in the process
// or one this library does not know how to warm, is not an error.
//
// A provider that wraps another, as a middleware does, is looked through
// (Unwrap() Provider) until one that can warm is found, so that warming a
// chain warms the provider at the bottom of it.
func Warm(ctx context.Context, provider Provider) (bool, error) {
	// A bound, so that a wrapper that returns itself cannot hold this for ever.
	for depth := 0; provider != nil && depth < 64; depth++ {
		if warmer, ok := provider.(Warmer); ok {
			if err := warmer.Warm(ctx); err != nil {
				return false, err
			}
			return true, nil
		}
		wrapper, ok := provider.(providerUnwrapper)
		if !ok {
			return false, nil
		}
		provider = wrapper.Unwrap()
	}
	return false, nil
}

// The most that is read from a response body that was decoded and is being
// closed, and the longest that is spent reading it.
const drainLimitBytes = 64 << 10

// drainTimeout is a variable so that a test can lengthen it.
var drainTimeout = 100 * time.Millisecond

// drainAndClose reads what is left of a response body and closes it. A decoder
// stops at the end of the value it reads, so a body of no stated length, whose
// last chunk arrives after the JSON, was closed before its end was seen, and an
// HTTP/1.1 connection that was closed that way is not put back in the pool: the
// next call paid for a new one. What is read is bounded in size and in time, so
// that a server that sends the answer and then holds the response open cannot
// hold the caller.
func drainAndClose(body io.ReadCloser) {
	// Closing a body that is being read is what stops the read.
	timer := time.AfterFunc(drainTimeout, func() { _ = body.Close() })
	_, _ = io.Copy(io.Discard, io.LimitReader(body, drainLimitBytes))
	timer.Stop()
	_ = body.Close()
}

// warmingClient is the client a warming request goes through: the provider's
// own transport, so that the connection it opens is the one the next call
// finds in the pool, without anything the provider's calls add to a request,
// and without following a redirect.
//
// The headers a provider is configured with (ExtraHeaders can hold a key, an
// Azure-style api-key) are added by a transport this library wraps around the
// caller's, and the request goes to the one under it. A transport the caller
// supplied that adds its own credentials is theirs and is used as it is: it is
// the one the calls use, and what it adds is not this library's to strip. A
// redirect is not followed, since a response that sends the client elsewhere is
// as good as any other for opening the connection, and following it would send
// whatever headers there are to a host nobody named.
func warmingClient(client *http.Client) *http.Client {
	transport := client.Transport
	if wrapped, ok := transport.(*customTransport); ok {
		transport = wrapped.transport
	}
	return &http.Client{
		Transport: transport,
		Timeout:   client.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// warmConnection opens a connection to an endpoint by asking for its headers.
// Any answer will do, a 404 or a 401 or a redirect as well as a 200: what is
// wanted is the connection, which is back in the pool once the answer has been
// read. The request carries no key, no header the provider was configured with
// and no content, and is given a bound of its own when the context has none.
func warmConnection(ctx context.Context, client *http.Client, name, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("%s: no base URL configured", name)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, warmTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%s: warming the connection: %w", name, err)
	}
	resp, err := warmingClient(client).Do(req)
	if err != nil {
		return fmt.Errorf("%s: warming the connection: %w", name, err)
	}
	// Read to the end and close, or the connection is not kept.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.Body.Close()
}
