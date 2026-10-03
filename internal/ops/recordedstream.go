package ops

import (
	"context"
	"time"

	"github.com/monstercameron/schemaflux/internal/requesttracking"
	"github.com/monstercameron/schemaflux/internal/types"
)

// RecordedStream opens a stream under a call log and reports the record of the
// whole stream when it ends. It is Recorded for an answer that arrives over
// time, and it is for the caller outside this package that has to finish its
// own record of the call (a recorder's Outcome) when the stream is over and
// has no other way to know: TextStream's own ending is not exported.
//
// operation is the name the record and the telemetry carry. establishes is the
// contract level the stream delivers; a floor it cannot meet refuses the call
// before open is called, and nothing is sent. open opens the stream from the
// ctx it is given, which carries the call log and the end callback, so the
// stream has to be opened from that context (an operation's options built from
// it, as for Recorded) for the record to hold what the stream cost and for end
// to be called.
//
// end is called once, when the stream's outcome is settled: from the goroutine
// that produces the stream, after the stream's call record is published and
// before its last item reaches a reader, or, for a stream that ends with no
// last item (the reader left, Cancel, the context ended), when that goroutine
// returns. meta is the record of the whole stream: the usage and cost the
// provider reported, the identity of the answer, the request and correlation
// ids, Elapsed from the call of RecordedStream to the end, and the contract
// levels as Recorded sets them (the level on a stream that completed, and only
// the one requested on a stream that did not). A stream that sent its request
// counts it however it ended: one attempt, the provider and model that were
// asked (or that answered, when the provider's last chunk arrived), and the
// usage the provider reported or, when it reported none, a cost marked unknown.
// A stream refused before anything was sent has no end and no request.
//
// err is the error the stream ended with, which every reader of the stream is
// told as well; nil for a stream that was read to its end. Neither holds the
// answer's text. end may be nil, which leaves the observers' operation scope as
// the only thing the call reports.
//
// When RecordedStream returns an error no stream was opened and end is not
// called: the caller ends its record with that error. An observer that is
// installed sees one operation, started before open and finished when the
// stream ends or when open fails.
func RecordedStream(ctx context.Context, operation string, establishes types.ContractLevel,
	open func(ctx context.Context) (*TextStream, error),
	end func(meta types.Meta, err error)) (*TextStream, error) {

	started := time.Now()
	// The client the stream runs on is fixed before the call log is made, so the
	// floor, the request and the record are all of one client.
	ctx, _ = PinExec(ctx)
	ctx, records := withCallRecording(WithOperationName(ctx, operation))
	ctx, observed := observeOperation(ctx, operation)
	ctx, tracking := requesttracking.Ensure(ctx, "", "")

	if floorErr := checkContractFloor(ctx, establishes); floorErr != nil {
		observed.end(&floorErr)
		return nil, floorErr
	}

	ctx = withStreamEnd(ctx, func(s *TextStream) {
		_, err := s.Outcome()
		meta := records.meta(operation)
		if tracking.RequestID != "" {
			meta.RequestID = tracking.RequestID
		}
		if tracking.CorrelationID != "" {
			meta.CorrelationID = tracking.CorrelationID
		}
		meta.Elapsed = time.Since(started)
		meta.RequestedContract = establishes
		if err == nil {
			meta.DeliveredContract = establishes
		}
		observed.end(&err)
		if end != nil {
			end(meta, err)
		}
	})

	stream, err := open(ctx)
	if err != nil {
		observed.end(&err)
		return nil, err
	}
	return stream, nil
}
