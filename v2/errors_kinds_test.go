package schemaflux

import (
	"errors"
	"testing"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/types"
)

// internalKindTable is what every internal kind is, with no mark on its error.
// It is written out here and read nowhere else: the code under test holds the
// same table, and a table that is the other's copy proves nothing.
var internalKindTable = map[types.ErrorKind]Kind{
	types.KindConfiguration:          ErrConfiguration,
	types.KindAuthentication:         ErrUnauthorized,
	types.KindPermission:             ErrUnauthorized,
	types.KindInvalidRequest:         ErrInvalidRequest,
	types.KindPolicyViolation:        ErrPolicy,
	types.KindBudgetExceeded:         ErrBudgetExceeded,
	types.KindAdmissionRejected:      ErrBusy,
	types.KindRateLimited:            ErrRateLimited,
	types.KindProviderUnavailable:    ErrUnavailable,
	types.KindTimeout:                ErrTimeout,
	types.KindCanceled:               ErrCanceled,
	types.KindCircuitOpen:            ErrUnavailable,
	types.KindShutdown:               ErrClosed,
	types.KindContextTooLarge:        ErrTooLarge,
	types.KindOutputTruncated:        ErrTruncated,
	types.KindMalformedOutput:        ErrInvalidAnswer,
	types.KindSchemaViolation:        ErrInvalidAnswer,
	types.KindBatchProtocolViolation: ErrInvalidAnswer,
	types.KindInvariantViolation:     ErrInvalidAnswer,
	types.KindEvidenceViolation:      ErrInvalidAnswer,
	types.KindUnsupportedCapability:  ErrUnsupported,
	types.KindRepairExhausted:        ErrInvalidAnswer,
	types.KindReviewRequired:         ErrReviewRequired,
}

var narrowingTable = []struct {
	name string
	mark types.Narrowing
	// kind is the kind the mark decides, or zero when the kind of the error does.
	kind Kind
}{
	{"no mark", types.NarrowNone, 0},
	{"the refusal mark", types.NarrowRefusal, ErrRefused},
	{"the panic mark", types.NarrowPanic, ErrInternal},
}

// TestEveryInternalKindAndMarkIsReadOneWayAtEveryEntryPoint reads each internal
// kind with each mark through every function of this package that reads one, and
// through the hooks the packages beside it read them with. A policy violation
// with no mark is a policy, and only the mark makes a refusal.
func TestEveryInternalKindAndMarkIsReadOneWayAtEveryEntryPoint(t *testing.T) {
	t.Parallel()
	if len(internalKindTable) != int(types.KindReviewRequired) {
		t.Fatalf("the table holds %d kinds and types has %d: a kind was added", len(internalKindTable), types.KindReviewRequired)
	}
	for kind := types.KindConfiguration; kind <= types.KindReviewRequired; kind++ {
		for _, mark := range narrowingTable {
			want := internalKindTable[kind]
			if mark.kind != 0 {
				want = mark.kind
			}
			t.Run(kind.String()+"/"+mark.name, func(t *testing.T) {
				t.Parallel()
				err := &types.OperationError{Kind: kind, Narrow: mark.mark, Op: "x", Provider: "p", Model: "m"}

				if got, ok := kindFromInternal(kind, mark.mark); !ok || got != want {
					t.Errorf("kindFromInternal = %v (%v), want %v", got, ok, want)
				}
				if got := Kind(bind.KindOf(kind, mark.mark)); got != want {
					t.Errorf("bind.KindOf = %v, want %v", got, want)
				}
				if got, known := bind.KindOfError(err); !known || Kind(got) != want {
					t.Errorf("bind.KindOfError = %v (%v), want %v", Kind(got), known, want)
				}
				for how, got := range map[string]error{
					"convertError":         convertError("op", err),
					"bind.ConvertError":    bind.ConvertError("op", err),
					"fromProviderError":    fromProviderError(err),
					"bind.FromProvider":    bind.FromProvider(err),
					"wrapped in the chain": convertError("op", errors.Join(errors.New("context"), err)),
				} {
					var e *Error
					if !errors.As(got, &e) || e.Kind != want {
						t.Errorf("%s gave %v, want kind %v", how, got, want)
					}
				}
			})
		}
	}
}

// TestEveryKindTravelsTheCallPathAndComesBackTheSame sends each kind through the
// internal form the call path carries it as and reads it back, the way a
// provider's failure goes out and comes back.
func TestEveryKindTravelsTheCallPathAndComesBackTheSame(t *testing.T) {
	t.Parallel()
	for kind := ErrConfiguration; kind <= ErrInternal; kind++ {
		t.Run(kind.Error(), func(t *testing.T) {
			t.Parallel()
			internal, mark := kindToInternal(kind)
			if back, ok := kindFromInternal(internal, mark); !ok || back != kind {
				t.Errorf("%v went to %v (%v) and came back as %v (%v)", kind, internal, mark, back, ok)
			}
			hooked, hookedMark := bind.InternalKind(uint8(kind))
			if hooked != internal || hookedMark != mark {
				t.Errorf("bind.InternalKind(%v) = %v (%v), want %v (%v)", kind, hooked, hookedMark, internal, mark)
			}
			// Through a provider's failure and the converter, as a call returns it.
			failed := providerFailure(&Error{Kind: kind, Message: "m"}, "p", "model")
			var e *Error
			if !errors.As(convertError("op", failed), &e) || e.Kind != kind {
				t.Errorf("a failure of kind %v came back as %v", kind, convertError("op", failed))
			}
		})
	}
	t.Run("a number that is not a kind is an unavailable provider", func(t *testing.T) {
		t.Parallel()
		for _, number := range []uint8{0, 19, 100, 255} {
			if kind, mark := bind.InternalKind(number); kind != types.KindProviderUnavailable || mark != types.NarrowNone {
				t.Errorf("bind.InternalKind(%d) = %v (%v)", number, kind, mark)
			}
		}
	})
	t.Run("an error nothing knows is unknown to the hook and internal to the converter", func(t *testing.T) {
		t.Parallel()
		odd := errors.New("something odd")
		if _, known := bind.KindOfError(odd); known {
			t.Error("bind.KindOfError claims to know an error nothing classified")
		}
		var e *Error
		if !errors.As(convertError("op", odd), &e) || e.Kind != ErrInternal {
			t.Errorf("convertError = %v, want ErrInternal", convertError("op", odd))
		}
		if !errors.As(fromProviderError(odd), &e) || e.Kind != ErrUnavailable {
			t.Errorf("fromProviderError = %v, want ErrUnavailable", fromProviderError(odd))
		}
	})
}
