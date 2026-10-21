package schemaflux

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var snakeName = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// fullRecord is a Record with every field set to something that is not its zero
// value, a duration among them.
func fullRecord() Record {
	return Record{
		Op: "extract", Start: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), Elapsed: 25850 * time.Microsecond,
		Err:      &Error{Kind: ErrRateLimited, Op: "extract", Provider: "p", Model: "m", RequestID: "r", Status: 429, Code: "slow", RetryAfter: 1500 * time.Millisecond, Message: "wait"},
		Provider: "p", Model: "m", RequestedModel: "rm", ServiceTier: "flex", RequestID: "r", CorrelationID: "c", SchemaID: "s",
		Requests: 2, Retries: 1, Repairs: 1, CacheHits: 1, Hedged: true,
		Tokens:    Usage{Input: 1, Output: 2, Total: 3, Cached: 4, CacheWrite: 5, Reasoning: 6},
		Cost:      Cost{Total: 1, Input: 2, Output: 3, Cached: 4, Reasoning: 5, Currency: "USD", Priced: true, Estimated: true, Source: "t"},
		Timing:    Timing{Calls: 1, Reused: true, Queued: 1500 * time.Microsecond, Lookup: 2 * time.Millisecond, Connect: 3 * time.Millisecond, Handshake: 4 * time.Millisecond, Send: 5 * time.Millisecond, Wait: 6 * time.Millisecond, Read: 7 * time.Millisecond, Network: 8 * time.Second},
		Requested: ContractEvidenceChecked, Delivered: ContractSchemaConstrained,
		Checks: []Check{{Name: "c", Passed: false, Detail: "d"}}, Strategy: "scoring",
		Provenance: &Provenance{ResultID: "x", ParentResultIDs: []string{"y"}, InputDigest: "i", SchemaDigest: "s", OperationVersion: "o", PromptVersion: "p",
			ResolvedModel: "m", NormalizersApplied: []string{"n"}, ItemRecoveryPath: "first attempt", CacheUsed: true, LibraryVersion: "l", AdapterVersion: "a"},
		AnsweredBy: "p", Typed: true, AskedFirst: "j", AskedFirstConfidence: 0.5, AskedFirstReported: true, Escalated: "unavailable",
		Asked: []Asked{{Question: "q", Options: []string{"a", "b"}, Low: 1, High: 5, Answer: "a"}}, Body: "b",
	}
}

// walkJSON calls visit with the path and the value of every member of a decoded
// JSON document, at every depth.
func walkJSON(path string, v any, visit func(path, key string, value any)) {
	switch typed := v.(type) {
	case map[string]any:
		for key, value := range typed {
			visit(path, key, value)
			walkJSON(path+"."+key, value, visit)
		}
	case []any:
		for _, value := range typed {
			walkJSON(path+"[]", value, visit)
		}
	}
}

func TestARecordIsWrittenInOneSchemaWithAnError(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(fullRecord())
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	durations := map[string]bool{"elapsed": true, "retry_after": true, "queued": true, "lookup": true, "connect": true, "handshake": true,
		"send": true, "wait": true, "read": true, "network": true}
	seen := map[string]bool{}
	walkJSON("", decoded, func(path, key string, value any) {
		seen[key] = true
		if !snakeName.MatchString(key) {
			t.Errorf("%s.%s is not a lower-case snake_case name", path, key)
		}
		if durations[key] {
			text, ok := value.(string)
			if !ok {
				t.Errorf("%s.%s = %v, want a duration string", path, key, value)
				return
			}
			if _, err := time.ParseDuration(text); err != nil {
				t.Errorf("%s.%s = %q is not a duration: %v", path, key, text, err)
			}
		}
	})
	for _, key := range []string{"elapsed", "retry_after", "queued", "network", "requested_contract", "delivered_contract", "cache_hits", "request_id", "asked_first_confidence"} {
		if !seen[key] {
			t.Errorf("the JSON of a full Record has no %q: %s", key, encoded)
		}
	}
	var top map[string]any
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatal(err)
	}
	if top["requested_contract"] != "evidence checked" || top["delivered_contract"] != "schema constrained" {
		t.Errorf("the contract levels are %v and %v, want their names", top["requested_contract"], top["delivered_contract"])
	}
	if top["elapsed"] != "25.85ms" {
		t.Errorf("elapsed = %v, want 25.85ms", top["elapsed"])
	}
}

func TestEveryFieldOfARecordAndItsPartsIsWritten(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(fullRecord())
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatal(err)
	}
	// Each field of Record, and of the types it holds, shows up as a member: a
	// field added without a name would show up under its Go name and fail the
	// snake_case test above, and one dropped by mistake fails here.
	count := func(typ reflect.Type) int { return typ.NumField() }
	if got, want := len(top), count(reflect.TypeOf(Record{})); got != want {
		t.Errorf("a full Record writes %d members and has %d fields", got, want)
	}
	for name, typ := range map[string]reflect.Type{"tokens": reflect.TypeOf(Usage{}), "cost": reflect.TypeOf(Cost{}), "timing": reflect.TypeOf(Timing{})} {
		part, ok := top[name].(map[string]any)
		if !ok {
			t.Fatalf("%s = %v, want an object", name, top[name])
		}
		if len(part) != count(typ) {
			keys := make([]string, 0, len(part))
			for key := range part {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			t.Errorf("%s writes %d members (%s) and the type has %d fields", name, len(part), strings.Join(keys, ", "), count(typ))
		}
	}
}

func TestAnErrorIsLoggedAndMarshalledUnderTheSameNamesAndUnits(t *testing.T) {
	t.Parallel()
	e := &Error{Kind: ErrRateLimited, Op: "extract", Provider: "p", Model: "m", RequestID: "r", Status: 429, Code: "slow", RetryAfter: 1500 * time.Millisecond, Message: "wait", Err: errors.New("hidden")}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Error("failed", "err", e)
	var line struct {
		Err map[string]any `json:"err"`
	}
	if err := json.Unmarshal(logged.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	marshalled, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var written map[string]any
	if err := json.Unmarshal(marshalled, &written); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(line.Err, written) {
		t.Errorf("logged as %v and marshalled as %v", line.Err, written)
	}
	if line.Err["retry_after"] != "1.5s" {
		t.Errorf("the wait is logged as %v, want the string 1.5s", line.Err["retry_after"])
	}
	if strings.Contains(logged.String(), "hidden") {
		t.Error("the log of an Error carries its cause")
	}
}
