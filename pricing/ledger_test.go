package pricing_test

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/pricing"
)

// These tests touch the process ledger. Each calls isolateLedger, which empties
// it and restores it afterwards, and none runs in parallel.

// epoch is an instant in the past, fixed for the run, so that the entries seeded
// at it and after it sit outside the current day and no budget is touched, and
// so that seeding the same data twice gives the same times.
var epoch = time.Now().Add(-72 * time.Hour).Truncate(time.Second)

// dataset puts four calls in the ledger, an hour apart from base:
//
//	a  m1  p1  extract   0.10
//	b  m1  p2  classify  0.20  tag team=x
//	c  m2  p1  extract   0.30  correlation job-1
//	d  m2  p1  rank      unpriced
func dataset() time.Time {
	at := epoch
	seed(
		call{id: "a", model: "m1", provider: "p1", operation: "extract", cost: 0.10, at: at,
			usage: schemaflux.Usage{Input: 100, Output: 10, Total: 110}},
		call{id: "b", model: "m1", provider: "p2", operation: "classify", cost: 0.20, at: at.Add(time.Hour),
			usage: schemaflux.Usage{Input: 200, Output: 20, Total: 220, Cached: 50}, tags: map[string]any{"team": "x"}},
		call{id: "c", model: "m2", provider: "p1", operation: "extract", cost: 0.30, at: at.Add(2 * time.Hour), correlation: "job-1",
			usage: schemaflux.Usage{Input: 300, Output: 30, Total: 330, Reasoning: 5}},
		call{id: "d", model: "m2", provider: "p1", operation: "rank", unpriced: true, at: at.Add(3 * time.Hour),
			usage: schemaflux.Usage{Input: 400, Output: 40, Total: 440}},
	)
	return at
}

func ids(entries []pricing.Entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.RequestID)
	}
	return b.String()
}

func TestSpentAddsUpWhatTheLedgerHolds(t *testing.T) {
	isolateLedger(t)
	at := dataset()

	cases := []struct {
		name     string
		since    time.Time
		filter   map[string]string
		usd      float64
		complete bool
	}{
		{"everything, with an unpriced call in it", time.Time{}, nil, 0.60, false},
		{"from the first entry on", at, nil, 0.60, false},
		{"an instant after the first entry", at.Add(time.Nanosecond), nil, 0.50, false},
		{"from the second entry", at.Add(time.Hour), nil, 0.50, false},
		{"from the third entry", at.Add(2 * time.Hour), nil, 0.30, false},
		{"from the unpriced entry", at.Add(3 * time.Hour), nil, 0, false},
		{"after the last entry", at.Add(4 * time.Hour), nil, 0, true},
		{"one model", time.Time{}, map[string]string{"model": "m1"}, 0.30, true},
		{"the model with an unpriced call", time.Time{}, map[string]string{"model": "m2"}, 0.30, false},
		{"one provider", time.Time{}, map[string]string{"provider": "p2"}, 0.20, true},
		{"one operation", time.Time{}, map[string]string{"operation": "extract"}, 0.40, true},
		{"one request id", time.Time{}, map[string]string{"request_id": "c"}, 0.30, true},
		{"one correlation id", time.Time{}, map[string]string{"correlation_id": "job-1"}, 0.30, true},
		{"one tag", time.Time{}, map[string]string{"team": "x"}, 0.20, true},
		{"every key must match", time.Time{}, map[string]string{"model": "m1", "operation": "extract"}, 0.10, true},
		{"keys that cannot both match", time.Time{}, map[string]string{"model": "m1", "provider": "p9"}, 0, true},
		{"a tag nobody has", time.Time{}, map[string]string{"team": "y"}, 0, true},
		{"a filter that excludes the unpriced call is complete", time.Time{}, map[string]string{"operation": "classify"}, 0.20, true},
		{"an empty filter is none", time.Time{}, map[string]string{}, 0.60, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			usd, complete := pricing.Spent(c.since, c.filter)
			if !near(usd, c.usd) || complete != c.complete {
				t.Fatalf("Spent = %v, %v; want %v, %v", usd, complete, c.usd, c.complete)
			}
		})
	}
}

func TestSpentOfAnEmptyLedgerIsZeroAndComplete(t *testing.T) {
	isolateLedger(t)
	if usd, complete := pricing.Spent(time.Time{}, nil); usd != 0 || !complete {
		t.Fatalf("Spent = %v, %v", usd, complete)
	}
}

func TestEntriesReturnsCopiesOldestFirst(t *testing.T) {
	isolateLedger(t)
	at := dataset()

	t.Run("every entry in the order recorded", func(t *testing.T) {
		if got := ids(pricing.Entries(time.Time{}, nil)); got != "abcd" {
			t.Fatalf("entries = %q", got)
		}
	})
	t.Run("since keeps the entries that ended at or after it", func(t *testing.T) {
		if got := ids(pricing.Entries(at.Add(time.Hour), nil)); got != "bcd" {
			t.Fatalf("entries = %q", got)
		}
	})
	t.Run("a filter selects by model", func(t *testing.T) {
		if got := ids(pricing.Entries(time.Time{}, map[string]string{"model": "m2"})); got != "cd" {
			t.Fatalf("entries = %q", got)
		}
	})
	t.Run("a filter selects by a tag", func(t *testing.T) {
		if got := ids(pricing.Entries(time.Time{}, map[string]string{"team": "x"})); got != "b" {
			t.Fatalf("entries = %q", got)
		}
	})
	t.Run("a filter and since together", func(t *testing.T) {
		if got := ids(pricing.Entries(at.Add(90*time.Minute), map[string]string{"provider": "p1"})); got != "cd" {
			t.Fatalf("entries = %q", got)
		}
	})
	t.Run("none matching is nil", func(t *testing.T) {
		if got := pricing.Entries(time.Time{}, map[string]string{"model": "nobody"}); got != nil {
			t.Fatalf("entries = %v, want nil", got)
		}
	})
	t.Run("an entry carries what the call recorded", func(t *testing.T) {
		got := pricing.Entries(time.Time{}, map[string]string{"request_id": "b"})
		if len(got) != 1 {
			t.Fatalf("entries = %v", got)
		}
		e := got[0]
		if !e.Time.Equal(at.Add(time.Hour)) || e.RequestID != "b" || e.Operation != "classify" || e.Model != "m1" || e.Provider != "p2" {
			t.Fatalf("entry = %+v", e)
		}
		if e.Usage != (schemaflux.Usage{Input: 200, Output: 20, Total: 220, Cached: 50}) {
			t.Fatalf("Usage = %+v", e.Usage)
		}
		if !e.Cost.Priced || !near(e.Cost.Total, 0.20) || e.Cost.Currency != "USD" || e.Cost.Source != "m1" {
			t.Fatalf("Cost = %+v", e.Cost)
		}
		if e.Tags["team"] != "x" {
			t.Fatalf("Tags = %v", e.Tags)
		}
	})
	t.Run("an entry on a model with no price is not priced", func(t *testing.T) {
		got := pricing.Entries(time.Time{}, map[string]string{"request_id": "d"})
		if len(got) != 1 || got[0].Cost.Priced || got[0].Cost.Total != 0 {
			t.Fatalf("entries = %+v", got)
		}
	})
	t.Run("a call with no tags has nil tags", func(t *testing.T) {
		got := pricing.Entries(time.Time{}, map[string]string{"request_id": "a"})
		if got[0].Tags != nil {
			t.Fatalf("Tags = %v", got[0].Tags)
		}
	})
	t.Run("a correlation id is carried", func(t *testing.T) {
		got := pricing.Entries(time.Time{}, map[string]string{"request_id": "c"})
		if got[0].CorrelationID != "job-1" {
			t.Fatalf("CorrelationID = %q", got[0].CorrelationID)
		}
	})
	t.Run("changing an entry does not change the ledger", func(t *testing.T) {
		got := pricing.Entries(time.Time{}, map[string]string{"request_id": "b"})
		got[0].Model = "changed"
		got[0].Tags["team"] = "changed"
		again := pricing.Entries(time.Time{}, map[string]string{"request_id": "b"})
		if len(again) != 1 || again[0].Model != "m1" || again[0].Tags["team"] != "x" {
			t.Fatalf("the ledger was changed through an entry: %+v", again)
		}
	})
	t.Run("a tag that is not text is read as the text it prints as", func(t *testing.T) {
		seed(call{id: "t", tags: map[string]any{"attempt": 3, "cached": true}})
		got := pricing.Entries(time.Time{}, map[string]string{"request_id": "t"})
		if got[0].Tags["attempt"] != "3" || got[0].Tags["cached"] != "true" {
			t.Fatalf("Tags = %v", got[0].Tags)
		}
	})
}

func TestForRequestFindsTheEntryOfARequest(t *testing.T) {
	isolateLedger(t)
	dataset()

	for _, id := range []string{"a", "b", "c", "d"} {
		t.Run("request "+id, func(t *testing.T) {
			e, ok := pricing.ForRequest(id)
			if !ok || e.RequestID != id {
				t.Fatalf("ForRequest = %+v, %v", e, ok)
			}
		})
	}
	t.Run("a request nobody made", func(t *testing.T) {
		if e, ok := pricing.ForRequest("nobody"); ok || e.RequestID != "" {
			t.Fatalf("ForRequest = %+v, %v", e, ok)
		}
	})
	t.Run("the empty id is never found", func(t *testing.T) {
		seed(call{id: ""})
		if _, ok := pricing.ForRequest(""); ok {
			t.Fatal("found an entry for the empty id")
		}
	})
	t.Run("an id is matched exactly", func(t *testing.T) {
		for _, id := range []string{"A", " a", "a ", "ab"} {
			if _, ok := pricing.ForRequest(id); ok {
				t.Errorf("ForRequest(%q) found an entry", id)
			}
		}
	})
	t.Run("an entry that was not priced is still found", func(t *testing.T) {
		e, ok := pricing.ForRequest("d")
		if !ok || e.Cost.Priced {
			t.Fatalf("ForRequest = %+v, %v", e, ok)
		}
	})
	t.Run("a repeated id is the most recent entry", func(t *testing.T) {
		seed(call{id: "retried", cost: 0.01, operation: "first"}, call{id: "retried", cost: 0.02, operation: "second"})
		e, _ := pricing.ForRequest("retried")
		if e.Operation != "second" {
			t.Fatalf("Operation = %q", e.Operation)
		}
	})
	t.Run("the entry is a copy", func(t *testing.T) {
		e, _ := pricing.ForRequest("b")
		e.Tags["team"] = "changed"
		again, _ := pricing.ForRequest("b")
		if again.Tags["team"] != "x" {
			t.Fatal("the ledger was changed through an entry")
		}
	})
	t.Run("it agrees with Entries", func(t *testing.T) {
		e, _ := pricing.ForRequest("c")
		listed := pricing.Entries(time.Time{}, map[string]string{"request_id": "c"})
		if fmt.Sprint(e) != fmt.Sprint(listed[0]) {
			t.Fatalf("%+v != %+v", e, listed[0])
		}
	})
	t.Run("an entry the ledger has dropped is not found", func(t *testing.T) {
		pricing.SetHistoryLimit(2)
		seed(call{id: "x1"}, call{id: "x2"})
		if _, ok := pricing.ForRequest("x1"); !ok {
			t.Fatal("a retained entry was not found")
		}
		seed(call{id: "x3"})
		if _, ok := pricing.ForRequest("x1"); ok {
			t.Fatal("an evicted entry was found")
		}
	})
	t.Run("after Reset nothing is found", func(t *testing.T) {
		pricing.Reset()
		if _, ok := pricing.ForRequest("c"); ok {
			t.Fatal("found an entry after Reset")
		}
	})
}

func TestSummarizeAddsUpEntries(t *testing.T) {
	isolateLedger(t)
	at := dataset()

	t.Run("an empty ledger is a summary of none that is complete", func(t *testing.T) {
		pricing.Reset()
		s := pricing.Summarize(time.Time{}, nil)
		if s.Requests != 0 || s.Unpriced != 0 || s.Cost.Total != 0 || !s.Cost.Priced || s.Cost.Currency != "USD" {
			t.Fatalf("Summary = %+v", s)
		}
		dataset()
	})
	t.Run("the count of entries", func(t *testing.T) {
		if got := pricing.Summarize(time.Time{}, nil).Requests; got != 4 {
			t.Fatalf("Requests = %d", got)
		}
	})
	t.Run("the count of unpriced entries", func(t *testing.T) {
		if got := pricing.Summarize(time.Time{}, nil).Unpriced; got != 1 {
			t.Fatalf("Unpriced = %d", got)
		}
	})
	t.Run("an unpriced entry makes the cost not priced", func(t *testing.T) {
		if pricing.Summarize(time.Time{}, nil).Cost.Priced {
			t.Fatal("a sum over an unpriced call is priced")
		}
	})
	t.Run("the cost is added up", func(t *testing.T) {
		if got := pricing.Summarize(time.Time{}, nil).Cost.Total; !near(got, 0.60) {
			t.Fatalf("Cost.Total = %v", got)
		}
	})
	t.Run("the parts of the cost are added up", func(t *testing.T) {
		c := pricing.Summarize(time.Time{}, nil).Cost
		if !near(c.Input, 0.30) || !near(c.Output, 0.30) {
			t.Fatalf("Cost = %+v", c)
		}
	})
	t.Run("the tokens are added up", func(t *testing.T) {
		want := schemaflux.Usage{Input: 1000, Output: 100, Total: 1100, Cached: 50, Reasoning: 5}
		if got := pricing.Summarize(time.Time{}, nil).Usage; got != want {
			t.Fatalf("Usage = %+v, want %+v", got, want)
		}
	})
	t.Run("a filter narrows it", func(t *testing.T) {
		s := pricing.Summarize(time.Time{}, map[string]string{"model": "m1"})
		if s.Requests != 2 || s.Unpriced != 0 || !s.Cost.Priced || !near(s.Cost.Total, 0.30) {
			t.Fatalf("Summary = %+v", s)
		}
	})
	t.Run("since narrows it", func(t *testing.T) {
		s := pricing.Summarize(at.Add(2*time.Hour), nil)
		if s.Requests != 2 || s.Unpriced != 1 {
			t.Fatalf("Summary = %+v", s)
		}
	})
	t.Run("the source is empty, since the entries have several", func(t *testing.T) {
		if got := pricing.Summarize(time.Time{}, nil).Cost.Source; got != "" {
			t.Fatalf("Source = %q", got)
		}
	})
	t.Run("a filter that matches nothing is a summary of none", func(t *testing.T) {
		s := pricing.Summarize(time.Time{}, map[string]string{"model": "nobody"})
		if s.Requests != 0 || !s.Cost.Priced {
			t.Fatalf("Summary = %+v", s)
		}
	})
}

func TestBreakdownSplitsTheCostByModelOperationAndProvider(t *testing.T) {
	isolateLedger(t)
	at := dataset()

	t.Run("by model", func(t *testing.T) {
		b := pricing.Breakdown(time.Time{})
		if !near(b.ByModel["m1"], 0.30) || !near(b.ByModel["m2"], 0.30) {
			t.Fatalf("breakdown = %+v", b)
		}
	})
	t.Run("by operation", func(t *testing.T) {
		b := pricing.Breakdown(time.Time{})
		if !near(b.ByOperation["extract"], 0.40) || !near(b.ByOperation["classify"], 0.20) {
			t.Fatalf("breakdown = %+v", b)
		}
	})
	t.Run("by provider", func(t *testing.T) {
		b := pricing.Breakdown(time.Time{})
		if !near(b.ByProvider["p1"], 0.40) || !near(b.ByProvider["p2"], 0.20) {
			t.Fatalf("breakdown = %+v", b)
		}
	})
	t.Run("the whole is the total", func(t *testing.T) {
		if got := pricing.Breakdown(time.Time{}).Total; !near(got, 0.60) {
			t.Fatalf("total = %v", got)
		}
	})
	t.Run("each split adds up to the total", func(t *testing.T) {
		b := pricing.Breakdown(time.Time{})
		for name, split := range map[string]map[string]float64{"model": b.ByModel, "operation": b.ByOperation, "provider": b.ByProvider} {
			sum := 0.0
			for _, v := range split {
				sum += v
			}
			if !near(sum, b.Total) {
				t.Errorf("the %s split adds up to %v, want %v", name, sum, b.Total)
			}
		}
	})
	t.Run("an entry with no price adds nothing", func(t *testing.T) {
		if got := pricing.Breakdown(time.Time{}).ByOperation["rank"]; got != 0 {
			t.Fatalf("rank = %v", got)
		}
	})
	t.Run("since narrows it", func(t *testing.T) {
		b := pricing.Breakdown(at.Add(time.Hour))
		if !near(b.Total, 0.50) || !near(b.ByModel["m1"], 0.20) {
			t.Fatalf("breakdown = %+v", b)
		}
	})
	t.Run("after the last entry it is empty and its maps are not nil", func(t *testing.T) {
		b := pricing.Breakdown(at.Add(10 * time.Hour))
		if b.Total != 0 || len(b.ByModel) != 0 || len(b.ByOperation) != 0 || len(b.ByProvider) != 0 {
			t.Fatalf("breakdown = %+v", b)
		}
		if b.ByModel == nil || b.ByOperation == nil || b.ByProvider == nil {
			t.Fatalf("a map is nil: %+v", b)
		}
	})
	t.Run("the maps are the caller's", func(t *testing.T) {
		b := pricing.Breakdown(time.Time{})
		b.Total = 99
		b.ByModel["m1"] = 99
		again := pricing.Breakdown(time.Time{})
		if again.Total == 99 || again.ByModel["m1"] == 99 {
			t.Fatal("the breakdown is shared")
		}
	})
	t.Run("an empty ledger has an empty breakdown", func(t *testing.T) {
		pricing.Reset()
		if b := pricing.Breakdown(time.Time{}); b.Total != 0 || len(b.ByModel) != 0 {
			t.Fatalf("breakdown = %+v", b)
		}
	})
	t.Run("a model with an awkward name is its own key", func(t *testing.T) {
		seed(call{id: "z", model: "a/b c", cost: 0.05})
		if got := pricing.Breakdown(time.Time{}).ByModel["a/b c"]; !near(got, 0.05) {
			t.Fatalf("a/b c = %v", got)
		}
	})
	t.Run("a model named like a total or a prefix collides with nothing", func(t *testing.T) {
		pricing.Reset()
		seed(call{id: "t1", model: "total", operation: "model_x", provider: "operation_y", cost: 0.25})
		seed(call{id: "t2", model: "x", operation: "total", provider: "p", cost: 0.50})
		b := pricing.Breakdown(time.Time{})
		if !near(b.Total, 0.75) || !near(b.ByModel["total"], 0.25) || !near(b.ByModel["x"], 0.50) ||
			!near(b.ByOperation["model_x"], 0.25) || !near(b.ByOperation["total"], 0.50) || !near(b.ByProvider["operation_y"], 0.25) {
			t.Fatalf("breakdown = %+v", b)
		}
	})
}

func TestExportWritesTheEntriesAsAReport(t *testing.T) {
	isolateLedger(t)
	at := dataset()

	records := func(t *testing.T, since time.Time) [][]string {
		t.Helper()
		out, err := pricing.Export(since, "csv")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil {
			t.Fatalf("the CSV does not parse: %v\n%s", err, out)
		}
		return rows
	}

	t.Run("the CSV has a header and a row for each entry", func(t *testing.T) {
		rows := records(t, time.Time{})
		if len(rows) != 5 {
			t.Fatalf("%d rows", len(rows))
		}
		want := "time,request_id,correlation_id,operation,model,provider,input_tokens,output_tokens,total_tokens,cost,priced"
		if got := strings.Join(rows[0], ","); got != want {
			t.Fatalf("header = %s", got)
		}
	})
	t.Run("a CSV row holds the entry", func(t *testing.T) {
		row := records(t, time.Time{})[3] // entry c
		want := []string{at.Add(2 * time.Hour).Format(time.RFC3339), "c", "job-1", "extract", "m2", "p1", "300", "30", "330", "0.3", "true"}
		if strings.Join(row, "|") != strings.Join(want, "|") {
			t.Fatalf("row = %v, want %v", row, want)
		}
	})
	t.Run("an unpriced entry has an empty cost in CSV and not zero", func(t *testing.T) {
		row := records(t, time.Time{})[4] // entry d
		if row[9] != "" || row[10] != "false" {
			t.Fatalf("cost = %q, priced = %q", row[9], row[10])
		}
	})
	t.Run("since narrows the CSV", func(t *testing.T) {
		if rows := records(t, at.Add(3*time.Hour)); len(rows) != 2 || rows[1][1] != "d" {
			t.Fatalf("rows = %v", rows)
		}
	})
	t.Run("an empty ledger is a header alone", func(t *testing.T) {
		if rows := records(t, at.Add(10*time.Hour)); len(rows) != 1 {
			t.Fatalf("rows = %v", rows)
		}
	})
	t.Run("the CSV is full precision for a cheap call", func(t *testing.T) {
		seed(call{id: "cheap", cost: 0.000012345})
		rows := records(t, time.Time{})
		last := rows[len(rows)-1]
		if last[9] != "0.000012345" {
			t.Fatalf("cost = %q", last[9])
		}
	})
	t.Run("text a spreadsheet would read as a formula is written as text", func(t *testing.T) {
		pricing.Reset()
		seed(call{id: "=cmd()", operation: "+x", model: "-y", provider: "@z"})
		row := records(t, time.Time{})[1]
		if row[1] != "'=cmd()" || row[3] != "'+x" || row[4] != "'-y" || row[5] != "'@z" {
			t.Fatalf("row = %v", row)
		}
	})
	t.Run("text with a comma, a quote or a line break survives", func(t *testing.T) {
		pricing.Reset()
		seed(call{id: "a,b", operation: `say "hi"`, model: "two\nlines"})
		row := records(t, time.Time{})[1]
		if row[1] != "a,b" || row[3] != `say "hi"` || row[4] != "two\nlines" {
			t.Fatalf("row = %q", row)
		}
		pricing.Reset()
		dataset()
	})

	jsonRows := func(t *testing.T, since time.Time) []map[string]any {
		t.Helper()
		out, err := pricing.Export(since, "json")
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("the JSON does not parse: %v\n%s", err, out)
		}
		return rows
	}

	t.Run("the JSON is an array with an object for each entry", func(t *testing.T) {
		rows := jsonRows(t, time.Time{})
		if len(rows) != 4 || rows[0]["request_id"] != "a" || rows[3]["request_id"] != "d" {
			t.Fatalf("rows = %v", rows)
		}
	})
	t.Run("a JSON object holds the entry", func(t *testing.T) {
		row := jsonRows(t, time.Time{})[2]
		if row["operation"] != "extract" || row["model"] != "m2" || row["provider"] != "p1" || row["correlation_id"] != "job-1" ||
			row["input_tokens"] != 300.0 || row["output_tokens"] != 30.0 || row["total_tokens"] != 330.0 || row["priced"] != true ||
			row["time"] != at.Add(2*time.Hour).Format(time.RFC3339) {
			t.Fatalf("row = %v", row)
		}
		if cost, ok := row["cost"].(float64); !ok || !near(cost, 0.3) {
			t.Fatalf("cost = %v", row["cost"])
		}
	})
	t.Run("an unpriced entry has a null cost in JSON and not zero", func(t *testing.T) {
		row := jsonRows(t, time.Time{})[3]
		if cost, present := row["cost"]; !present || cost != nil || row["priced"] != false {
			t.Fatalf("row = %v", row)
		}
	})
	t.Run("the JSON carries the tags of an entry", func(t *testing.T) {
		row := jsonRows(t, time.Time{})[1]
		tags, _ := row["tags"].(map[string]any)
		if tags["team"] != "x" {
			t.Fatalf("row = %v", row)
		}
		if _, has := jsonRows(t, time.Time{})[0]["tags"]; has {
			t.Fatal("an entry with no tags has a tags member")
		}
	})
	t.Run("an empty ledger is an empty JSON array", func(t *testing.T) {
		out, err := pricing.Export(at.Add(10*time.Hour), "json")
		if err != nil || strings.TrimSpace(out) != "[]" {
			t.Fatalf("out = %q, err = %v", out, err)
		}
	})
	t.Run("since narrows the JSON", func(t *testing.T) {
		if rows := jsonRows(t, at.Add(2*time.Hour)); len(rows) != 2 {
			t.Fatalf("rows = %v", rows)
		}
	})

	for _, format := range []string{"", "xml", "CSV", "Json", " csv", "csv ", "yaml", "text", "tsv", "json\n"} {
		t.Run(fmt.Sprintf("the format %q is refused", format), func(t *testing.T) {
			out, err := pricing.Export(time.Time{}, format)
			var e *schemaflux.Error
			if !errors.As(err, &e) || e.Kind != schemaflux.ErrInvalidRequest || e.Op != "pricing.export" {
				t.Fatalf("err = %#v", err)
			}
			if out != "" {
				t.Fatalf("out = %q beside an error", out)
			}
			if strings.Contains(e.Message, "xml") || strings.Contains(e.Error(), "yaml") {
				t.Fatalf("message repeats the format: %q", e.Message)
			}
		})
	}
}

func TestSetHistoryLimitBoundsTheEntriesKept(t *testing.T) {
	isolateLedger(t)

	t.Run("the oldest entries are dropped", func(t *testing.T) {
		pricing.SetHistoryLimit(3)
		seed(call{id: "1"}, call{id: "2"}, call{id: "3"}, call{id: "4"}, call{id: "5"})
		if got := ids(pricing.Entries(time.Time{}, nil)); got != "345" {
			t.Fatalf("entries = %q", got)
		}
	})
	t.Run("changing the limit discards the entries held", func(t *testing.T) {
		pricing.SetHistoryLimit(10)
		if got := pricing.Entries(time.Time{}, nil); got != nil {
			t.Fatalf("entries = %v", got)
		}
	})
	t.Run("a limit of one keeps the latest", func(t *testing.T) {
		pricing.SetHistoryLimit(1)
		seed(call{id: "1"}, call{id: "2"})
		if got := ids(pricing.Entries(time.Time{}, nil)); got != "2" {
			t.Fatalf("entries = %q", got)
		}
	})
	t.Run("spent is read from what is kept", func(t *testing.T) {
		pricing.SetHistoryLimit(2)
		seed(call{id: "1", cost: 1}, call{id: "2", cost: 2}, call{id: "3", cost: 4})
		if usd, _ := pricing.Spent(time.Time{}, nil); !near(usd, 6) {
			t.Fatalf("Spent = %v, want the last two entries only", usd)
		}
	})
	t.Run("a bigger limit holds more", func(t *testing.T) {
		pricing.SetHistoryLimit(500)
		for i := range 400 {
			seed(call{id: fmt.Sprint("n", i)})
		}
		if got := len(pricing.Entries(time.Time{}, nil)); got != 400 {
			t.Fatalf("%d entries", got)
		}
	})
	for _, limit := range []int{0, -1, -1000} {
		t.Run(fmt.Sprintf("a limit of %d is the default of 10000", limit), func(t *testing.T) {
			pricing.SetHistoryLimit(2)
			pricing.SetHistoryLimit(limit)
			for i := range 10_001 {
				seed(call{id: fmt.Sprint("d", i)})
			}
			entries := pricing.Entries(time.Time{}, nil)
			if len(entries) != 10_000 || entries[0].RequestID != "d1" {
				t.Fatalf("%d entries, the first %q", len(entries), entries[0].RequestID)
			}
		})
	}
	t.Run("an evicted entry is gone from ForRequest", func(t *testing.T) {
		pricing.SetHistoryLimit(1)
		seed(call{id: "old"}, call{id: "new"})
		if _, ok := pricing.ForRequest("old"); ok {
			t.Fatal("found an evicted entry")
		}
		if _, ok := pricing.ForRequest("new"); !ok {
			t.Fatal("did not find the kept entry")
		}
	})
	t.Run("the limits and the prices are left alone", func(t *testing.T) {
		restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		defer restore()
		seed(call{id: "spent", cost: 5})
		pricing.SetHistoryLimit(5)
		if got := pricing.Price(schemaflux.Usage{Input: 1_000_000}, "gpt-5", "openai"); !got.Priced {
			t.Fatal("the price table changed")
		}
	})
	t.Run("a limit applies to the entries that come after", func(t *testing.T) {
		pricing.SetHistoryLimit(2)
		seed(call{id: "1"})
		pricing.SetHistoryLimit(3)
		seed(call{id: "2"}, call{id: "3"}, call{id: "4"}, call{id: "5"})
		if got := ids(pricing.Entries(time.Time{}, nil)); got != "345" {
			t.Fatalf("entries = %q", got)
		}
	})
}

func TestResetEmptiesTheLedger(t *testing.T) {
	isolateLedger(t)

	t.Run("the entries go", func(t *testing.T) {
		dataset()
		pricing.Reset()
		if got := pricing.Entries(time.Time{}, nil); got != nil {
			t.Fatalf("entries = %v", got)
		}
	})
	t.Run("the spend is zero and complete", func(t *testing.T) {
		dataset()
		pricing.Reset()
		if usd, complete := pricing.Spent(time.Time{}, nil); usd != 0 || !complete {
			t.Fatalf("Spent = %v, %v", usd, complete)
		}
	})
	t.Run("an unpriced call is forgotten too", func(t *testing.T) {
		seed(call{id: "u", unpriced: true})
		pricing.Reset()
		if _, complete := pricing.Spent(time.Time{}, nil); !complete {
			t.Fatal("the figure is still incomplete after Reset")
		}
	})
	t.Run("the summary and the breakdown are empty", func(t *testing.T) {
		dataset()
		pricing.Reset()
		if pricing.Summarize(time.Time{}, nil).Requests != 0 || pricing.Breakdown(time.Time{}).Total != 0 {
			t.Fatal("something remains")
		}
	})
	t.Run("a Reset of an empty ledger is fine, twice", func(t *testing.T) {
		pricing.Reset()
		pricing.Reset()
	})
	t.Run("the ledger takes entries again", func(t *testing.T) {
		pricing.Reset()
		seed(call{id: "again", cost: 1})
		if usd, _ := pricing.Spent(time.Time{}, nil); !near(usd, 1) {
			t.Fatalf("Spent = %v", usd)
		}
	})
	t.Run("the price table is left alone", func(t *testing.T) {
		name := uniqueModel(t)
		_, _ = pricing.Register(pricing.Model{Provider: "acme", Name: name, Input: 1, Output: 1})
		pricing.Reset()
		if !pricing.Price(schemaflux.Usage{Input: 1_000_000}, name, "acme").Priced {
			t.Fatal("Reset removed a price")
		}
	})
	t.Run("the history limit is left alone", func(t *testing.T) {
		pricing.SetHistoryLimit(2)
		pricing.Reset()
		seed(call{id: "1"}, call{id: "2"}, call{id: "3"})
		if got := ids(pricing.Entries(time.Time{}, nil)); got != "23" {
			t.Fatalf("entries = %q", got)
		}
		pricing.SetHistoryLimit(0)
	})
	t.Run("the limits are left alone", func(t *testing.T) {
		restore := setLimits(t, pricing.Limits{Daily: 1, Enforce: true})
		defer restore()
		pricing.Reset()
		seed(call{id: "over", cost: 2})
		if err := checkBudget(); err == nil {
			t.Fatal("Reset removed a limit")
		}
	})
	t.Run("a threshold crossed after Reset alerts again", func(t *testing.T) {
		var alerts int
		restore := setLimits(t, pricing.Limits{Daily: 1, OnThreshold: func(float64, float64, string) { alerts++ }})
		defer restore()
		seed(call{id: "1", cost: 1})
		first := alerts
		pricing.Reset()
		seed(call{id: "2", cost: 1})
		if first != 2 || alerts != 4 {
			t.Fatalf("alerts = %d then %d, want 2 then 4", first, alerts)
		}
	})
	t.Run("the pricing read functions do not change the ledger", func(t *testing.T) {
		pricing.Reset()
		dataset()
		_ = pricing.Price(schemaflux.Usage{Input: 1}, "gpt-5", "openai")
		_, _ = pricing.Spent(time.Time{}, nil)
		_ = pricing.Entries(time.Time{}, nil)
		_ = pricing.Summarize(time.Time{}, nil)
		_ = pricing.Breakdown(time.Time{})
		_, _ = pricing.Export(time.Time{}, "csv")
		if got := len(pricing.Entries(time.Time{}, nil)); got != 4 {
			t.Fatalf("%d entries after reading", got)
		}
	})
}
