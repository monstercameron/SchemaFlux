package pricing

import (
	"encoding/csv"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/monstercameron/schemaflux/internal/bind"
	"github.com/monstercameron/schemaflux/internal/setting"
	inner "github.com/monstercameron/schemaflux/pricing"
	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// An Entry is what one call to a model cost, as the process ledger holds it.
type Entry struct {
	// Time is when the call ended. The zero value means the call did not say.
	Time time.Time

	// RequestID is the id of the request, as in the call's Record. Empty means
	// the call had none, and ForRequest cannot find it.
	RequestID string

	// CorrelationID groups the requests of one piece of work, as in the call's
	// Record. Empty means none.
	CorrelationID string

	// Operation is the operation that made the call, as in the Record's Op.
	// Empty means the call did not say.
	Operation string

	// Model is the model that answered. Empty means the call did not say.
	Model string

	// Provider is the provider the call went to. Empty means the call did not
	// say.
	Provider string

	// Usage is the token count the provider reported. The zero value means it
	// reported none, which is not a count of zero.
	Usage schemaflux.Usage

	// Cost is what the call came to. A Cost that is not Priced is a call on a
	// model with no price: its Total of zero means unknown, not free.
	Cost schemaflux.Cost

	// Tags are the call's own labels, as text, which a filter can select on.
	// Nil means the call had none.
	Tags map[string]string
}

// A Summary is the sum of a set of entries.
type Summary struct {
	// Requests is how many entries the summary covers. Zero means none matched.
	Requests int

	// Unpriced is how many of them were calls on a model with no price. When it
	// is not zero every figure of Cost is a floor.
	Unpriced int

	// Usage is the token counts added up, over the entries that reported them.
	Usage schemaflux.Usage

	// Cost is the costs added up, in US dollars. Its Priced is true when every
	// entry was priced, and so also for a summary of none. Its Source is empty,
	// since the entries have several.
	Cost schemaflux.Cost
}

// Spent returns what the process has spent, in US dollars, over the entries
// that ended at or after since and match filter, and whether that figure is
// complete. It is not complete when any entry in the range was on a model with
// no price: the figure is then a floor under what was spent, not the total. See
// the package comment for the filter and for a zero since.
func Spent(since time.Time, filter map[string]string) (usd float64, complete bool) {
	return inner.GetTotalSpend(since, filter)
}

// Entries returns the ledger's entries that ended at or after since and match
// filter, oldest first. It returns nil when none match. Each entry is a copy.
func Entries(since time.Time, filter map[string]string) []Entry {
	records := inner.GetRequestCosts(since, filter)
	if len(records) == 0 {
		return nil
	}
	entries := make([]Entry, len(records))
	for i, record := range records {
		entries[i] = entryFromInternal(record)
	}
	return entries
}

// ForRequest returns the entry for a request id, and false when the ledger holds
// none: the request was never recorded, was recorded with no id, or was evicted
// to make room (see SetHistoryLimit). When several entries have the id, as after
// a retry, it is the most recent. The entry is a copy.
func ForRequest(requestID string) (Entry, bool) {
	record, ok := inner.GetRequestCost(requestID)
	if !ok {
		return Entry{}, false
	}
	return entryFromInternal(record), true
}

// Summarize adds up the ledger's entries that ended at or after since and match
// filter.
func Summarize(since time.Time, filter map[string]string) Summary {
	var s Summary
	s.Cost.Currency = "USD"
	for _, entry := range Entries(since, filter) {
		s.Requests++
		if !entry.Cost.Priced {
			s.Unpriced++
		}
		s.Usage.Input = bind.SumInt(s.Usage.Input, entry.Usage.Input)
		s.Usage.Output = bind.SumInt(s.Usage.Output, entry.Usage.Output)
		s.Usage.Total = bind.SumInt(s.Usage.Total, entry.Usage.Total)
		s.Usage.Cached = bind.SumInt(s.Usage.Cached, entry.Usage.Cached)
		s.Usage.CacheWrite = bind.SumInt(s.Usage.CacheWrite, entry.Usage.CacheWrite)
		s.Usage.Reasoning = bind.SumInt(s.Usage.Reasoning, entry.Usage.Reasoning)
		s.Cost.Total += entry.Cost.Total
		s.Cost.Input += entry.Cost.Input
		s.Cost.Output += entry.Cost.Output
		s.Cost.Cached += entry.Cost.Cached
		s.Cost.Reasoning += entry.Cost.Reasoning
	}
	s.Cost.Priced = s.Unpriced == 0
	return s
}

// Spending is what a set of entries cost, in US dollars, split three ways. Each
// split adds up to Total.
type Spending struct {
	// ByModel is the cost of the entries by the model that answered. An entry that
	// did not say its model is under the empty name.
	ByModel map[string]float64

	// ByOperation is the cost of the entries by the operation that made the call.
	ByOperation map[string]float64

	// ByProvider is the cost of the entries by the provider the call went to.
	ByProvider map[string]float64

	// Total is the cost of every entry. An entry on a model with no price adds
	// nothing to it, so it is a floor whenever Spent says its figure is not
	// complete.
	Total float64
}

// Breakdown returns what the entries that ended at or after since cost, in US
// dollars, by model, by operation and by provider, and in all. An entry on a model
// with no price adds nothing to any figure, and still has its name in the maps. The
// maps are not nil, and are the caller's own.
func Breakdown(since time.Time) Spending {
	spending := Spending{
		ByModel:     map[string]float64{},
		ByOperation: map[string]float64{},
		ByProvider:  map[string]float64{},
	}
	for _, entry := range Entries(since, nil) {
		spending.ByModel[entry.Model] += entry.Cost.Total
		spending.ByOperation[entry.Operation] += entry.Cost.Total
		spending.ByProvider[entry.Provider] += entry.Cost.Total
		spending.Total += entry.Cost.Total
	}
	return spending
}

// Export writes the entries that ended at or after since as a report, in the
// format "csv" or "json", oldest first, and returns it.
//
// Both formats name the same fields, in lower case with underscores: time (in
// RFC 3339, and empty for a call that did not say when it ended), request_id, correlation_id, operation, model, provider,
// input_tokens, output_tokens, total_tokens, cost and priced. The cost is
// written in full precision, because a call on a cheap model costs a small
// fraction of a cent. For an entry that is not priced the cost is empty in CSV
// and null in JSON, and never 0. The JSON also carries an entry's tags, when it
// has any. In CSV, text that holds a comma, a quote or a line break is quoted,
// and text that begins with =, +, - or @ is written with a leading apostrophe so
// that a spreadsheet reads it as text and not as a formula.
//
// A format that is neither is an *Error of kind ErrInvalidRequest.
func Export(since time.Time, format string) (string, error) {
	switch format {
	case "csv":
		return exportCSV(Entries(since, nil))
	case "json":
		return exportJSON(Entries(since, nil))
	}
	return "", &schemaflux.Error{Kind: schemaflux.ErrInvalidRequest, Op: "pricing.export", Message: `the format is not "csv" or "json"`}
}

// exportCSV writes entries as the CSV report.
func exportCSV(entries []Entry) (string, error) {
	var out strings.Builder
	w := csv.NewWriter(&out)
	_ = w.Write([]string{"time", "request_id", "correlation_id", "operation", "model", "provider",
		"input_tokens", "output_tokens", "total_tokens", "cost", "priced"})
	for _, e := range entries {
		cost := ""
		if e.Cost.Priced {
			cost = strconv.FormatFloat(e.Cost.Total, 'f', -1, 64)
		}
		_ = w.Write([]string{
			reportTime(e.Time),
			spreadsheetSafe(e.RequestID),
			spreadsheetSafe(e.CorrelationID),
			spreadsheetSafe(e.Operation),
			spreadsheetSafe(e.Model),
			spreadsheetSafe(e.Provider),
			strconv.Itoa(e.Usage.Input),
			strconv.Itoa(e.Usage.Output),
			strconv.Itoa(e.Usage.Total),
			cost,
			strconv.FormatBool(e.Cost.Priced),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", &schemaflux.Error{Kind: schemaflux.ErrInternal, Op: "pricing.export", Message: "the report could not be written", Err: err}
	}
	return out.String(), nil
}

// reportTime is a time as the reports write it: in RFC 3339, and empty for the
// zero time, which an Entry uses for a call that did not say when it ended.
func reportTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// exportRow is an entry as the JSON report writes it.
type exportRow struct {
	Time          string            `json:"time"`
	RequestID     string            `json:"request_id"`
	CorrelationID string            `json:"correlation_id"`
	Operation     string            `json:"operation"`
	Model         string            `json:"model"`
	Provider      string            `json:"provider"`
	InputTokens   int               `json:"input_tokens"`
	OutputTokens  int               `json:"output_tokens"`
	TotalTokens   int               `json:"total_tokens"`
	Cost          *float64          `json:"cost"`
	Priced        bool              `json:"priced"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// exportJSON writes entries as the JSON report: an array, empty when there are
// no entries.
func exportJSON(entries []Entry) (string, error) {
	rows := make([]exportRow, len(entries))
	for i, e := range entries {
		rows[i] = exportRow{
			Time:          reportTime(e.Time),
			RequestID:     e.RequestID,
			CorrelationID: e.CorrelationID,
			Operation:     e.Operation,
			Model:         e.Model,
			Provider:      e.Provider,
			InputTokens:   e.Usage.Input,
			OutputTokens:  e.Usage.Output,
			TotalTokens:   e.Usage.Total,
			Priced:        e.Cost.Priced,
			Tags:          e.Tags,
		}
		if e.Cost.Priced {
			total := e.Cost.Total
			rows[i].Cost = &total
		}
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "", &schemaflux.Error{Kind: schemaflux.ErrInternal, Op: "pricing.export", Message: "the report could not be written", Err: err}
	}
	return string(data), nil
}

// spreadsheetSafe is text unchanged unless a spreadsheet would read it as a
// formula, in which case it has a leading apostrophe.
func spreadsheetSafe(text string) string {
	if text != "" && strings.ContainsRune("=+-@\t\r", rune(text[0])) {
		return "'" + text
	}
	return text
}

// historyLayers holds the history limits that calls to SetHistoryLimit have put
// in force. The limit before the first call is the default, which is what the
// ledger starts with.
var historyLayers = setting.Layers[int]{
	Apply: inner.SetCostHistoryLimit,
	Base:  func() int { return inner.DefaultCostHistoryLimit },
}

// SetHistoryLimit sets how many entries the ledger keeps, the oldest being
// dropped to make room for the newest. A limit of zero or less is the default,
// 10,000, which is a few megabytes and covers any interactive session: a
// service that wants more should Export the entries, not keep them here. The
// ledger takes room for the entries as they arrive and not for the limit, so any
// number, math.MaxInt included, is a limit that is never reached.
//
// It changes process state: the entries held now are discarded, as the totals
// that Spent reads come from them, and so are the entries held when the limit is
// put back. Limits and the price table are not touched.
//
// The returned function undoes this call: it puts back what was in force when
// the call was made, unless something has been set since, in which case that
// later setting stays in force until it is restored too, and then what is in
// force is the newest setting not yet restored, or the original if there is none.
// It does nothing when called a second time, may be called in any order relative
// to the restore functions of other calls, and may be called from several
// goroutines.
func SetHistoryLimit(n int) (restore func()) {
	return historyLayers.Set(n)
}

// Reset empties the ledger: every entry, so that Spent, Entries, Summarize and
// Breakdown start again from nothing, and the record of which budget alerts have
// been sent, so that a limit crossed after a Reset alerts again. It changes
// process state and has no undo. It leaves the limits (SetLimits), the history
// limit (SetHistoryLimit) and the price table as they are.
func Reset() {
	inner.ResetCostTracking()
}

// entryFromInternal is the Entry for an internal record.
func entryFromInternal(r inner.CostRecord) Entry {
	cost := r.Cost
	return Entry{
		Time:          r.Timestamp,
		RequestID:     r.RequestID,
		CorrelationID: r.CorrelationID,
		Operation:     r.Operation,
		Model:         r.Model,
		Provider:      r.Provider,
		Usage:         bind.UsageFromInternal[schemaflux.Usage](r.TokenUsage),
		Cost:          costOf(&cost),
		Tags:          r.Tags,
	}
}
