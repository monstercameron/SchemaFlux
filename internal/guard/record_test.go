package guard_test

import (
	"errors"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// recordScenarios are the calls whose Record is read: one that succeeds and one
// of each way a call ends that a Record has to report.
func recordScenarios() []scenario {
	pick := map[string]bool{"provider unavailable": true, "malformed answer": true, "empty answer": true,
		"unusable option": true, "cancelled context": true, "floor the operation cannot meet": true}
	out := []scenario{{name: "success", sends: true}}
	for _, sc := range failureScenarios {
		if pick[sc.name] {
			out = append(out, sc)
		}
	}
	return out
}

func TestEveryOperationAddsOneRecord(t *testing.T) {
	for _, r := range allRows() {
		for _, st := range append(append([]state{}, recordedStates...), stateTwo) {
			for _, sc := range recordScenarios() {
				if st.second && sc.name != "success" {
					continue
				}
				t.Run(r.id()+"/"+st.name+"/"+sc.name, func(t *testing.T) {
					if ok, why := applies(&r, sc); !ok {
						t.Skip(why)
					}
					stt := st
					if sc.floor != 0 {
						stt = stateFloor
					}
					o := run(t, &r, stt, sc, neutral())
					wantKind := sc.kind
					refused := sc.name != "success" && !sc.sends
					if sc.name == "success" && stt.floor > schemaflux.ContractPromptOnly && !meets(&r) {
						wantKind, refused = schemaflux.ErrPolicy, true
					}
					checkRecords(t, &r, o, wantKind, refused, o.rig.rec)
					if stt.second {
						checkRecords(t, &r, o, wantKind, refused, o.rig.rec2)
					}
				})
			}
		}
	}
}

// checkRecords holds a recorder to the one Record the call owes it.
func checkRecords(t *testing.T, r *row, o outcome, wantKind schemaflux.Kind, refused bool, rec *schemaflux.Recorder) {
	t.Helper()
	records := rec.Records()
	if len(records) != 1 {
		t.Fatalf("the recorder holds %d Records after the call, want exactly 1 (error: %v)", len(records), o.err)
	}
	got := records[0]
	if got.Op != r.op {
		t.Errorf("the Record's Op is %q, want %q (table A3)", got.Op, r.op)
	}
	if wantKind == 0 {
		if o.err != nil {
			t.Fatalf("the call failed where the scenario succeeds: %v", o.err)
		}
		if got.Err != nil {
			t.Errorf("the call succeeded and its Record carries an error: %v", got.Err)
		}
		return
	}
	if o.err == nil {
		t.Fatalf("the call returned no error, want one of kind %v", wantKind)
	}
	if got.Err == nil {
		t.Fatalf("the call failed with %v and its Record carries no error", o.err)
	}
	var fromCall, fromRecord *schemaflux.Error
	if !errors.As(o.err, &fromCall) || !errors.As(got.Err, &fromRecord) {
		t.Fatalf("the call's error is %T and the Record's is %T, want an *Error in both", o.err, got.Err)
	}
	if fromCall.Kind != fromRecord.Kind || fromCall.Op != fromRecord.Op {
		t.Errorf("the Record's error is %v/%q and the call's is %v/%q", fromRecord.Kind, fromRecord.Op, fromCall.Kind, fromCall.Op)
	}
	if refused {
		if got.Requests != 0 || o.rig.model.Calls() != 0 {
			t.Errorf("the call was refused before anything was sent, yet the Record says %d requests and the provider received %d", got.Requests, o.rig.model.Calls())
		}
	}
}

// TestRecordsDeliverTheLevelOfTheTable holds the Record of a successful call to the
// level of the contract table: a static row is delivered at exactly its level and a
// computed one at or above the table's lowest.
func TestRecordsDeliverTheLevelOfTheTable(t *testing.T) {
	for _, r := range allRows() {
		t.Run(r.id(), func(t *testing.T) {
			o := run(t, &r, stateRecorder, scenario{name: "success", sends: true}, neutral())
			if o.err != nil {
				t.Fatalf("the call failed: %v", o.err)
			}
			rec := onlyRecordOf(t, o)
			switch {
			case !r.computed && rec.Delivered != r.level:
				t.Errorf("the Record says %v was delivered, the contract table says %v", rec.Delivered, r.level)
			case r.computed && rec.Delivered < r.level:
				t.Errorf("the Record says %v was delivered, below the %v this instantiation is meant to deliver", rec.Delivered, r.level)
			}
			if rec.Requested < rec.Delivered && !r.computed {
				t.Errorf("the Record says %v was requested and %v delivered: a call is never delivered above what it asked", rec.Requested, rec.Delivered)
			}
		})
	}
}

func onlyRecordOf(t *testing.T, o outcome) schemaflux.Record {
	t.Helper()
	records := o.rig.records()
	if len(records) != 1 {
		t.Fatalf("%d Records, want 1", len(records))
	}
	return records[0]
}
