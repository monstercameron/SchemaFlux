package requestid

import (
	"context"
	"strings"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
)

// IDs are the ids a context carries.
type IDs struct {
	// Request identifies one request, job or trace. It is empty when the context
	// carries none.
	Request string

	// Correlation groups the requests that belong to one larger piece of work.
	// It is empty when the context carries none.
	Correlation string
}

// maxIDLength is the longest id that is stored, in bytes. The same limit is
// internal/requesttracking's maxExtractedIDLength, which bounds an id read from
// a carrier on the library's own call path; the two are held together by
// TestWritersApplyTheRuleTheTrackingCodeAppliesToHeaders.
const maxIDLength = 128

// bounded returns id with surrounding white space trimmed if what is left is 1
// to maxIDLength bytes of printable ASCII without spaces, and "" otherwise. An
// id that fails is refused whole: a shortened or cleaned id would be a different
// id, and would join with nothing.
func bounded(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxIDLength {
		return ""
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return ""
		}
	}
	return id
}

// mustHaveContext panics on a nil context, as context.WithValue does.
func mustHaveContext(ctx context.Context) {
	if ctx == nil {
		panic("requestid: nil Context")
	}
}

// With returns a copy of ctx that carries id as its request id. The id nearest
// the call wins, so an id set on a context replaces the one its parent carries
// for that context and the contexts derived from it.
//
// An id that is empty or blank, longer than 128 bytes once surrounding white
// space is trimmed, or holds a byte outside the printable ASCII range 0x21 to
// 0x7e (a space, a control character, a byte of a multi-byte character) is not
// stored: With returns ctx itself, so the id ctx already carries stays in force.
//
// With panics if ctx is nil.
func With(ctx context.Context, id string) context.Context {
	mustHaveContext(ctx)
	id = bounded(id)
	if id == "" {
		return ctx
	}
	return requesttracking.WithMetadata(ctx, requesttracking.Metadata{RequestID: id})
}

// WithCorrelation returns a copy of ctx that carries id as its correlation id,
// under the same rule as With for which ids are stored, which id wins, and a nil
// ctx. It leaves the request id alone.
func WithCorrelation(ctx context.Context, id string) context.Context {
	mustHaveContext(ctx)
	id = bounded(id)
	if id == "" {
		return ctx
	}
	return requesttracking.WithMetadata(ctx, requesttracking.Metadata{CorrelationID: id})
}

// From returns the ids ctx carries, or the zero IDs for a context that carries
// none, and for a nil ctx. It returns what was stored and makes no id: the ids a
// call runs under when ctx carries none are made by the call, and come back in
// its Record and its Error.
func From(ctx context.Context) IDs {
	carried := requesttracking.FromContext(ctx)
	return IDs{Request: carried.RequestID, Correlation: carried.CorrelationID}
}
