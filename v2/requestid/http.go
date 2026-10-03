package requestid

import (
	"context"
	"net/http"
	"strings"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
)

// Inject writes the ids ctx carries onto h, under the configured header names
// (X-Request-ID and X-Correlation-ID unless Configure set others), replacing any
// value h holds under those names. An id that ctx does not carry leaves its
// header as it was, and an id that breaks the rule With applies is not written,
// whatever put it on the context. Inject makes no id.
//
// A nil h, and a nil ctx, are left alone: nothing is written.
func Inject(ctx context.Context, h http.Header) {
	if h == nil || ctx == nil {
		return
	}
	carried := requesttracking.FromContext(ctx)
	names := requesttracking.GetConfig()
	if id := bounded(carried.RequestID); id != "" {
		h.Set(names.RequestIDHeader, id)
	}
	if id := bounded(carried.CorrelationID); id != "" {
		h.Set(names.CorrelationIDHeader, id)
	}
}

// Extract returns a copy of ctx that carries the ids found in h, under the
// configured header names, which are matched without regard to case. A header
// whose id fails the rule With applies, or that is absent or empty, gives no
// id, and the id ctx already carries for it stays in force; an id that is found
// is nearer than the one in ctx and replaces it. Where a header holds several
// values the first is the one read. If h is nil, or gives no id, Extract
// returns ctx itself.
//
// Extract panics if ctx is nil.
func Extract(ctx context.Context, h http.Header) context.Context {
	mustHaveContext(ctx)
	if h == nil {
		return ctx
	}
	names := requesttracking.GetConfig()
	return requesttracking.WithMetadata(ctx, requesttracking.Metadata{
		RequestID:     bounded(firstValue(h, names.RequestIDHeader)),
		CorrelationID: bounded(firstValue(h, names.CorrelationIDHeader)),
	})
}

// firstValue returns the first value of the header called name. The canonical
// spelling of the name is looked up first, which is the key http.Header.Set and
// the server's reader use. A map built by hand may hold the name under another
// case, so if the canonical key has no value the keys are searched without
// regard to case, and when several match the smallest key is taken so that the
// answer does not depend on map order.
func firstValue(h http.Header, name string) string {
	if values := h[http.CanonicalHeaderKey(name)]; len(values) > 0 {
		return values[0]
	}
	var chosen string
	var found bool
	for key, values := range h {
		if len(values) == 0 || !strings.EqualFold(key, name) {
			continue
		}
		if !found || key < chosen {
			chosen, found = key, true
		}
	}
	if !found {
		return ""
	}
	return h[chosen][0]
}
