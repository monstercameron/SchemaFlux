package tests

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/telemetry"
)

// The target types and the controls shared by the call-path benchmarks in this
// directory (bench_operations_test.go, bench_contention_test.go,
// bench_batch_test.go and the others beside it).
//
// The coordinator's bench_callpath_test.go measures a three-field struct. The
// types here are larger, so the work that depends on the size of the target
// type (the schema, the prompt, the decode of the answer) can be told apart
// from the work that does not:
//
//	benchInvoice    three string fields (bench_callpath_test.go)
//	benchOrder      twelve fields, a nested struct, a slice of structs, a slice of strings
//	benchWideOrder  sixty fields of four kinds
//	benchOrderList  one field, a slice of twenty structs: the shape of "extract every line item"

type benchAddress struct {
	Street   string `json:"street"`
	City     string `json:"city"`
	Region   string `json:"region"`
	Postcode string `json:"postcode"`
	Country  string `json:"country"`
}

type benchLineItem struct {
	SKU         string  `json:"sku"`
	Description string  `json:"description"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
}

type benchOrder struct {
	OrderID  string          `json:"order_id"`
	Customer string          `json:"customer"`
	Email    string          `json:"email"`
	Currency string          `json:"currency"`
	Status   string          `json:"status"`
	PlacedOn string          `json:"placed_on"`
	Total    float64         `json:"total"`
	Gift     bool            `json:"gift"`
	Priority int             `json:"priority"`
	ShipTo   benchAddress    `json:"ship_to"`
	Items    []benchLineItem `json:"items"`
	Tags     []string        `json:"tags"`
}

type benchOrderList struct {
	Items []benchLineItem `json:"items"`
}

type benchWideOrder struct {
	Field01 string  `json:"field_01"`
	Field02 string  `json:"field_02"`
	Field03 string  `json:"field_03"`
	Field04 string  `json:"field_04"`
	Field05 string  `json:"field_05"`
	Field06 string  `json:"field_06"`
	Field07 string  `json:"field_07"`
	Field08 string  `json:"field_08"`
	Field09 string  `json:"field_09"`
	Field10 string  `json:"field_10"`
	Field11 string  `json:"field_11"`
	Field12 string  `json:"field_12"`
	Field13 string  `json:"field_13"`
	Field14 string  `json:"field_14"`
	Field15 string  `json:"field_15"`
	Field16 string  `json:"field_16"`
	Field17 string  `json:"field_17"`
	Field18 string  `json:"field_18"`
	Field19 string  `json:"field_19"`
	Field20 string  `json:"field_20"`
	Field21 string  `json:"field_21"`
	Field22 string  `json:"field_22"`
	Field23 string  `json:"field_23"`
	Field24 string  `json:"field_24"`
	Field25 string  `json:"field_25"`
	Field26 string  `json:"field_26"`
	Field27 string  `json:"field_27"`
	Field28 string  `json:"field_28"`
	Field29 string  `json:"field_29"`
	Field30 string  `json:"field_30"`
	Field31 string  `json:"field_31"`
	Field32 string  `json:"field_32"`
	Field33 string  `json:"field_33"`
	Field34 string  `json:"field_34"`
	Field35 string  `json:"field_35"`
	Field36 string  `json:"field_36"`
	Field37 string  `json:"field_37"`
	Field38 string  `json:"field_38"`
	Field39 string  `json:"field_39"`
	Field40 string  `json:"field_40"`
	Field41 int     `json:"field_41"`
	Field42 int     `json:"field_42"`
	Field43 int     `json:"field_43"`
	Field44 int     `json:"field_44"`
	Field45 int     `json:"field_45"`
	Field46 int     `json:"field_46"`
	Field47 int     `json:"field_47"`
	Field48 int     `json:"field_48"`
	Field49 int     `json:"field_49"`
	Field50 int     `json:"field_50"`
	Field51 float64 `json:"field_51"`
	Field52 float64 `json:"field_52"`
	Field53 float64 `json:"field_53"`
	Field54 float64 `json:"field_54"`
	Field55 float64 `json:"field_55"`
	Field56 bool    `json:"field_56"`
	Field57 bool    `json:"field_57"`
	Field58 bool    `json:"field_58"`
	Field59 bool    `json:"field_59"`
	Field60 bool    `json:"field_60"`
}

// benchOrderSummary is what Transform produces from a benchOrder.
type benchOrderSummary struct {
	Headline string   `json:"headline"`
	Total    float64  `json:"total"`
	Flags    []string `json:"flags"`
}

// benchLook is thirteen questions over three embedded structs, the shape the
// decision benchmarks in internal/ops use, so that an Assess through the whole
// call path can be set beside the one that stubs the library's caller.
type benchScreen struct {
	Spam         bool   `ask:"Is this message spam, a marketing blast or an automated notification, and not a customer writing to a support desk?"`
	Foreign      bool   `ask:"Is the body of the message written in a language other than English?"`
	Language     string `ask:"What language is the body of the message written in?" oneof:"English,Spanish,French,German,Portuguese,another language"`
	PersonalData bool   `ask:"Does the body of the message contain a payment card number, a password, a phone number, a street address or a government ID number? An email address, a name or an order number does not count."`
}

type benchTriage struct {
	Team       string `ask:"Which team should handle this message?" oneof:"billing,technical,shipping,account"`
	Intent     string `ask:"What is the customer doing?" oneof:"asking a question,making a complaint,requesting an action,giving feedback"`
	Urgency    int    `ask:"How urgent is it, where 1 can wait a week and 5 means the customer is blocked or losing money right now?" scale:"1-5"`
	Angry      bool   `ask:"Is the customer angry or threatening to leave?"`
	Refund     bool   `ask:"Is the customer asking for money back?"`
	Answerable bool   `ask:"Does the message say what the problem or the request is, specifically enough that the desk knows what to look into?"`
	NeedsHuman bool   `ask:"Is the customer demanding money back, or threatening a chargeback or legal action, so that a person has to approve the reply before it is sent?"`
}

type benchAfter struct {
	Next  string `ask:"What should the desk do after replying?" oneof:"close the ticket,wait for the customer,follow up tomorrow,escalate to a manager"`
	Again bool   `ask:"Is this customer likely to write to the desk again about the same matter within a week?"`
}

type benchLook struct {
	benchScreen
	benchTriage
	benchAfter
}

const benchLookAnswer = `{"q1":false,"q2":false,"q3":1,"q4":false,"q5":1,"q6":2,"q7":4,"q8":true,"q9":true,"q10":true,"q11":true,"q12":4,"q13":false}`

// benchSubject is a message of the size a support desk gets.
type benchSubject struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

var benchMessage = benchSubject{
	ID:      "M-1042",
	From:    "dana@example.com",
	Subject: "Charged twice for order 7731",
	Body:    "Hello, I was charged twice for order 7731 on the 3rd and nobody has answered my two earlier emails. I would like the second charge back on my card by Friday, or I will dispute it with my bank. Thank you, Dana.",
}

// benchCustomer is the record the judgments check.
type benchCustomer struct {
	Email    string `json:"email"`
	Country  string `json:"country"`
	Age      int    `json:"age"`
	Password string `json:"password"`
}

// benchSample is a value of the shape of t, written the way a model would
// write it, for a canned answer.
func benchSample(t reflect.Type, name string) any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "sample " + name + " text"
	case reflect.Int, reflect.Int32, reflect.Int64:
		return 42
	case reflect.Float32, reflect.Float64:
		return 12.5
	case reflect.Bool:
		return true
	case reflect.Slice:
		count := 2
		if t.Elem().Kind() == reflect.Struct {
			count = 20
		}
		items := make([]any, count)
		for i := range items {
			items[i] = benchSample(t.Elem(), name)
		}
		return items
	case reflect.Struct:
		object := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if key == "" {
				key = field.Name
			}
			object[key] = benchSample(field.Type, key)
		}
		return object
	}
	return nil
}

// benchAnswerOf is a canned answer of type T.
func benchAnswerOf[T any](tb testing.TB) string {
	tb.Helper()
	encoded, err := json.Marshal(benchSample(reflect.TypeFor[T](), "x"))
	if err != nil {
		tb.Fatal(err)
	}
	return string(encoded)
}

// benchSilenceLogging turns the library's logging off for a benchmark that is
// not measuring it, and puts the default configuration back afterwards.
//
// It is off here because at its default settings the library writes three lines
// to standard error for every Extract and keeps a copy of each in memory, which
// would be most of what these benchmarks measured. The benchmarks that measure
// the logging itself call benchDefaultLogging instead.
func benchSilenceLogging(tb testing.TB) {
	tb.Helper()
	schemaflux.ConfigureLogging(telemetry.LoggerConfig{Level: telemetry.ErrorLevel, DisableStderr: true})
	tb.Cleanup(func() { schemaflux.ConfigureLogging(telemetry.DefaultLoggerConfig()) })
}

// benchDefaultLogging gives the library the logging a caller who configured
// nothing gets: level Info, standard error, history capture on.
func benchDefaultLogging(tb testing.TB) {
	tb.Helper()
	schemaflux.ConfigureLogging(telemetry.DefaultLoggerConfig())
}
