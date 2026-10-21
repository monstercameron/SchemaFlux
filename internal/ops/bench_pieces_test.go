package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// Measured allocations, per operation, median over the runs of the baseline
// of 2026-10-03: allocations and bytes of one operation, exact where the
// times are not. bench_baseline_summary.txt has the nanoseconds, the time each
// package was run, and the tree it ran against.
//
//	benchmark                                         allocs/op       B/op
//	ExtractBuild/Preflight/flat3                              8        128
//	ExtractBuild/Preflight/nested12                          64       1328
//	ExtractBuild/Preflight/wide60                           179       2864
//	ExtractBuild/ClassifyType/flat3                           8        128
//	ExtractBuild/ClassifyType/nested12                       64       1328
//	ExtractBuild/ClassifyType/wide60                        179       2864
//	ExtractBuild/Prose/flat3                                 24        656
//	ExtractBuild/Prose/nested12                             164       6330
//	ExtractBuild/Prose/wide60                               370      12998
//	ExtractBuild/JSONSchema/flat3                            20       1912
//	ExtractBuild/JSONSchema/nested12                        114      11280
//	ExtractBuild/JSONSchema/wide60                          261      33968
//	ExtractBuild/Describe/flat3                              66       5257
//	ExtractBuild/Describe/nested12                          328      29428
//	ExtractBuild/Describe/wide60                            708      79537
//	ExtractBuild/SchemaName/flat3                             2         24
//	ExtractBuild/SchemaName/nested12                          2         24
//	ExtractBuild/SchemaName/wide60                            2         24
//	ExtractBuild/CacheKey/flat3                               1         64
//	ExtractBuild/CacheKey/nested12                            1         80
//	ExtractBuild/CacheKey/wide60                              1         64
//	ExtractBuild/SystemPrompt/flat3                           3        880
//	ExtractBuild/SystemPrompt/nested12                        3       2064
//	ExtractBuild/SystemPrompt/wide60                          3       4626
//	ExtractBuildAll/flat3                                   122       8904
//	ExtractBuildAll/flat3/parallel                          122       8906
//	ExtractBuildAll/nested12                                674      50580
//	ExtractBuildAll/nested12/parallel                       674      50680
//	ExtractBuildAll/wide60                                 1522     134350
//	ExtractBuildAll/wide60/parallel                        1523     134914
//	NormalizeInput/string                                     0          0
//	NormalizeInput/struct                                     3        688
//	NormalizeInput/string100KB                                0          0
//	NormalizeInput/struct100KB                                3     215014
//	OptionsResolve/Validate/no_timeout                        6        600
//	OptionsResolve/toOpOptions/no_timeout                     5        152
//	OptionsResolve/mergeEmbeddedOpOptions/no_timeout          5        152
//	OptionsResolve/ExtractResultHelpers/no_timeout           20        712
//	OptionsResolve/Validate/timeout_30s                       6        600
//	OptionsResolve/toOpOptions/timeout_30s                   10        552
//	OptionsResolve/mergeEmbeddedOpOptions/timeout_30s        10        548
//	OptionsResolve/ExtractResultHelpers/timeout_30s          35       2226
//	ApplyDefaults/0_options                                   1        240
//	ApplyDefaults/0_options/parallel                          1        240
//	ApplyDefaults/1_option                                    2        480
//	ApplyDefaults/1_option/parallel                           2        480
//	ApplyDefaults/3_options                                   4        960
//	ApplyDefaults/3_options/parallel                          4        960
//	Decode/Floor/record_200B                                 10        560
//	Decode/Floor/list_5KB                                   151      14880
//	Decode/Floor/list_100KB                                2608     348928
//	Decode/Floor/array_5KB                                  149      14816
//	Decode/Floor/array_100KB                               2606     348864
//	Decode/ParseJSON/record_200B                             10        560
//	Decode/ParseJSON/list_5KB                               151      14880
//	Decode/ParseJSON/list_100KB                            2608     348928
//	Decode/ParseJSON/array_5KB                              149      14816
//	Decode/ParseJSON/array_100KB                           2606     348864
//	Decode/ParseJSONStrict/record_200B                       47       1968
//	Decode/ParseJSONStrict/list_5KB                         166      26560
//	Decode/ParseJSONStrict/list_100KB                      2623     562848
//	Decode/ParseJSONStrict/array_5KB                        149      14816
//	Decode/ParseJSONStrict/array_100KB                     2606     348864
//	Decode/RequireRecognisedField/record_200B                38       1504
//	Decode/RequireRecognisedField/list_5KB                   16      11704
//	Decode/RequireRecognisedField/list_100KB                 16     213944
//	Decode/RequireRecognisedField/array_5KB                   1         24
//	Decode/RequireRecognisedField/array_100KB                 1         24
//	Decode/DecodeExact/record_200B                          207       8121
//	Decode/DecodeExact/list_5KB                            6347     222376
//	Decode/DecodeExact/list_100KB                        120012    4226334
//	Decode/DecodeExact/array_5KB                           6321     212820
//	Decode/DecodeExact/array_100KB                       119984    4045282
//	Decode/NumericFidelity/record_200B                       92       4048
//	Decode/ValidateExtractedData/record_200B                 14        224
//	Decode/NumericFidelity/list_5KB                        2811     124472
//	Decode/ValidateExtractedData/list_5KB                   784      13473
//	Decode/NumericFidelity/list_100KB                     53378    2339704
//	Decode/ValidateExtractedData/list_100KB               15274     270580
//	Decode/NumericFidelity/array_5KB                       2800     115486
//	Decode/ValidateExtractedData/array_5KB                  748      10881
//	Decode/NumericFidelity/array_100KB                    53367    2180885
//	Decode/ValidateExtractedData/array_100KB              14625     218942
//	Decode/Floor/flat3_60B                                    9        352
//	Decode/ParseJSON/flat3_60B                                9        352
//	Decode/ParseJSONStrict/flat3_60B                         30       1408
//	Decode/RequireRecognisedField/flat3_60B                  22       1104
//	Decode/DecodeExact/flat3_60B                            101       5848
//	Decode/NumericFidelity/flat3_60B                         40       2880
//	Decode/ValidateExtractedData/flat3_60B                    6         96
//	BatchMerge/200items/Merge                              1846     142924
//	BatchMerge/200items/Encode                              206      36632
//	AuditBuildRequest/0_policies/schema                      39       4208
//	AuditBuildRequest/3_policies/schema                      89       9462
//	AuditBuildRequest/10_policies/schema                    118      13454
//	AuditBuildRequest/subject_shape                          30        792
//	ValidateRules/rules/without_regex                        40       2224
//	ValidateRules/rules/with_regex                          105       7273
//	ValidateRules/answer_schema/no_correction                35       4704
//	ValidateRules/answer_schema/with_correction              74       7568

// What the parts of one call cost, each by itself, so that a later change to
// one of them can be attributed to it and to nothing else.
//
// The older benchmarks in this package stub the library's own caller, so they
// skip everything CallLLM does; these take the work that is repeated on every
// call (the schema of the target type, the options, the decode of the answer,
// the rules and schemas of the judgments) and run each of them alone, over
// three target types of increasing size:
//
//	flat3    three string fields (the coordinator's benchInvoice)
//	nested12 twelve fields, one nested struct, a slice of structs, a slice of strings
//	wide60   sixty fields of four kinds
//
// The allocations are in the table at the top of this file and the
// nanoseconds in bench_baseline_summary.txt; they are exact where the nanoseconds are not, and they are the
// figure to hold a later change to. Nothing here asserts a budget yet.
//
//	BenchmarkExtractBuild/<piece>/<type>       the schema and prompt work of one Extract, a piece at a time
//	BenchmarkExtractBuildAll/<type>            all of it, in the order core.go does it, serial and parallel
//	BenchmarkNormalizeInput/<input>            turning the caller's input into prompt text
//	BenchmarkOptionsResolve/<piece>/<timeout>  validating and merging the options, which Extract does more than once
//	BenchmarkApplyDefaults/<n options>         the reflective merge every decision pays
//	BenchmarkDecode/<function>/<answer>        parsing and checking an answer of 60 B, 200 B, 5 KB, 100 KB
//	BenchmarkBatchMerge/200items               resolving a 200-item batch answer by id
//	BenchmarkAuditBuildRequest/<n policies>    the declared schema of an audit
//	BenchmarkValidateRules/<rules>             the deterministic rules of a validation, with and without a regex
//
// Nothing here is logged or sent: these functions are called directly and
// build or read, and the logger is not involved, so no benchmark in this file
// silences it.

// --- The target types.

type benchFlat struct {
	OrderID string `json:"order_id"`
	Product string `json:"product"`
	Amount  string `json:"amount"`
}

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

type benchNested struct {
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

type benchWide struct {
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

// benchBuildTypes are the three sizes the schema work is measured on.
var benchBuildTypes = []struct {
	name string
	typ  reflect.Type
}{
	{"flat3", reflect.TypeFor[benchFlat]()},
	{"nested12", reflect.TypeFor[benchNested]()},
	{"wide60", reflect.TypeFor[benchWide]()},
}

// benchSampleValue is a value of the shape of t, for building a canned answer.
func benchSampleValue(t reflect.Type, name string) any {
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
		return []any{benchSampleValue(t.Elem(), name), benchSampleValue(t.Elem(), name)}
	case reflect.Struct:
		object := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if key == "" {
				key = field.Name
			}
			object[key] = benchSampleValue(field.Type, key)
		}
		return object
	}
	return nil
}

// benchAnswerFor is a canned answer for t, the way a model would write it.
func benchAnswerFor(tb testing.TB, t reflect.Type) string {
	tb.Helper()
	encoded, err := json.Marshal(benchSampleValue(t, "x"))
	if err != nil {
		tb.Fatal(err)
	}
	return string(encoded)
}

// --- The schema and prompt work of one Extract.

func BenchmarkExtractBuild(b *testing.B) {
	pieces := []struct {
		name string
		run  func(t reflect.Type, prose string, descriptor SchemaDescriptor)
	}{
		{"Preflight", func(t reflect.Type, _ string, _ SchemaDescriptor) { _ = PreflightType(t) }},
		{"ClassifyType", func(t reflect.Type, _ string, _ SchemaDescriptor) { _ = ClassifyType(t) }},
		{"Prose", func(t reflect.Type, _ string, _ SchemaDescriptor) { _ = GenerateTypeSchema(t) }},
		{"JSONSchema", func(t reflect.Type, _ string, _ SchemaDescriptor) { _ = GenerateJSONSchema(t) }},
		{"Describe", func(t reflect.Type, _ string, _ SchemaDescriptor) { _ = DescribeSchema(t) }},
		{"SchemaName", func(t reflect.Type, _ string, _ SchemaDescriptor) { _ = schemaNameFor(t) }},
		{"CacheKey", func(_ reflect.Type, _ string, d SchemaDescriptor) { _ = SchemaCacheKey("extract", "v1", d) }},
		{"SystemPrompt", func(_ reflect.Type, prose string, _ SchemaDescriptor) {
			_ = BuildExtractSystemPrompt(prose, types.TransformMode)
		}},
	}
	for _, piece := range pieces {
		for _, target := range benchBuildTypes {
			b.Run(piece.name+"/"+target.name, func(b *testing.B) {
				prose := GenerateTypeSchema(target.typ)
				descriptor := DescribeSchema(target.typ)
				b.ReportAllocs()
				for b.Loop() {
					piece.run(target.typ, prose, descriptor)
				}
			})
		}
	}
}

// benchBuildAll is the work Extract does before it has a request to send, in
// the order core.go does it, as of the tree this was written against.
func benchBuildAll(t reflect.Type) {
	_ = PreflightType(t)
	prose := GenerateTypeSchema(t)
	_ = GenerateJSONSchema(t)
	descriptor := DescribeSchema(t)
	_ = SchemaCacheKey("extract", "v1", descriptor)
	_ = BuildExtractSystemPrompt(prose, types.TransformMode)
}

func BenchmarkExtractBuildAll(b *testing.B) {
	for _, target := range benchBuildTypes {
		b.Run(target.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchBuildAll(target.typ)
			}
		})
		b.Run(target.name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					benchBuildAll(target.typ)
				}
			})
		})
	}
}

// benchLargeOrder is an order whose JSON is about size bytes.
func benchLargeOrder(size int) benchNested {
	order := benchNested{OrderID: "A-2207", Customer: "Dana Whitfield", Email: "dana@example.com"}
	item := benchLineItem{SKU: "T-1001", Description: "Team plan, annual seat", Quantity: 3, UnitPrice: 49}
	encoded, _ := json.Marshal(item)
	for n := 0; n*(len(encoded)+1) < size; n++ {
		order.Items = append(order.Items, item)
	}
	return order
}

func BenchmarkNormalizeInput(b *testing.B) {
	order := benchNested{OrderID: "A-2207", Customer: "Dana Whitfield", Email: "dana@example.com",
		Items: []benchLineItem{{SKU: "T-1", Description: "Team plan", Quantity: 3, UnitPrice: 49}}}
	inputs := []struct {
		name  string
		value any
	}{
		{"string", "Why is my October invoice for order A-2207 $49 and not $39?"},
		{"struct", order},
		{"string100KB", strings.Repeat("a line of a long document. ", 4000)},
		{"struct100KB", benchLargeOrder(100 << 10)},
	}
	for _, input := range inputs {
		b.Run(input.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := NormalizeInput(input.value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// --- The options, which Extract resolves more than once.

func BenchmarkOptionsResolve(b *testing.B) {
	for _, timeout := range []time.Duration{0, 30 * time.Second} {
		label := "no timeout"
		if timeout > 0 {
			label = "timeout 30s"
		}
		opts := NewExtractOptions()
		opts.Timeout = timeout
		pieces := []struct {
			name string
			run  func()
		}{
			{"Validate", func() { _ = opts.Validate() }},
			{"toOpOptions", func() { _ = opts.toOpOptions() }},
			{"mergeEmbeddedOpOptions", func() { _ = mergeEmbeddedOpOptions(opts.CommonOptions, opts.OpOptions) }},
			{"ExtractResultHelpers", func() {
				_ = requestedContractFor(opts)
				_, _ = deliveredContractFor(opts, nil)
				_ = extractProvenance("input", opts, types.Meta{})
			}},
		}
		for _, piece := range pieces {
			b.Run(piece.name+"/"+label, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					piece.run()
				}
			})
		}
	}
}

func BenchmarkApplyDefaults(b *testing.B) {
	one := types.OpOptions{Mode: types.Strict, Intelligence: types.Smart}
	two := types.OpOptions{Steering: "be brief", Threshold: 0.7, Model: "gpt-5.4"}
	three := types.OpOptions{Context: context.Background(), RequestID: "req_0123456789abcdef", MaxOutputTokens: 4096}
	cases := []struct {
		name string
		opts []types.OpOptions
	}{
		{"0 options", nil},
		{"1 option", []types.OpOptions{one}},
		{"3 options", []types.OpOptions{one, two, three}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = applyDefaults(tc.opts...)
			}
		})
		b.Run(tc.name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = applyDefaults(tc.opts...)
				}
			})
		})
	}
}

// --- The answer.

type benchRecord struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Email  string  `json:"email"`
	Note   string  `json:"note"`
	Amount float64 `json:"amount"`
	Count  int     `json:"count"`
	Active bool    `json:"active"`
}

type benchRecordList struct {
	Records []benchRecord `json:"records"`
}

func benchRecordJSON(i int) string {
	return fmt.Sprintf(`{"id":"rec-%05d","name":"Dana Whitfield %d","email":"dana%d@example.com","note":"A short note about this record.","amount":%d.25,"count":%d,"active":true}`,
		i, i, i, i, i%97+1)
}

// benchRecordsOfSize is a body of at least size bytes: a list of records,
// wrapped in an object (wrapped) or bare.
func benchRecordsOfSize(size int, wrapped bool) string {
	var records []string
	total := 0
	for i := 0; total < size || i == 0; i++ {
		record := benchRecordJSON(i)
		records = append(records, record)
		total += len(record) + 1
		if size <= 0 {
			break
		}
	}
	list := "[" + strings.Join(records, ",") + "]"
	if wrapped {
		return `{"records":` + list + `}`
	}
	return list
}

type benchAnswer struct {
	name   string
	body   string
	target func() any // a fresh pointer to the type the answer is read into
}

func benchAnswers() []benchAnswer {
	return []benchAnswer{
		{"flat3_60B", `{"order_id":"A-2207","product":"Team plan","amount":"$49"}`, func() any { return new(benchFlat) }},
		{"record_200B", benchRecordJSON(1), func() any { return new(benchRecord) }},
		{"list_5KB", benchRecordsOfSize(5<<10, true), func() any { return new(benchRecordList) }},
		{"list_100KB", benchRecordsOfSize(100<<10, true), func() any { return new(benchRecordList) }},
		{"array_5KB", benchRecordsOfSize(5<<10, false), func() any { return new([]benchRecord) }},
		{"array_100KB", benchRecordsOfSize(100<<10, false), func() any { return new([]benchRecord) }},
	}
}

// BenchmarkDecode reads an answer back, one function at a time. Floor is
// encoding/json alone, which is the least any of them could cost.
func BenchmarkDecode(b *testing.B) {
	functions := []struct {
		name string
		run  func(tb testing.TB, answer benchAnswer)
	}{
		{"Floor", func(tb testing.TB, a benchAnswer) {
			if err := json.Unmarshal([]byte(a.body), a.target()); err != nil {
				tb.Fatal(err)
			}
		}},
		{"ParseJSON", func(tb testing.TB, a benchAnswer) {
			if err := ParseJSON(a.body, a.target()); err != nil {
				tb.Fatal(err)
			}
		}},
		{"decodeCallerAnswer", func(tb testing.TB, a benchAnswer) {
			if err := decodeCallerAnswer("bench", a.body, a.target()); err != nil {
				tb.Fatal(err)
			}
		}},
		{"DecodeExact", func(tb testing.TB, a benchAnswer) {
			if err := DecodeExact(a.body, a.target(), DefaultDecodeLimits()); err != nil {
				tb.Fatal(err)
			}
		}},
	}
	for _, function := range functions {
		for _, answer := range benchAnswers() {
			b.Run(function.name+"/"+answer.name, func(b *testing.B) {
				b.SetBytes(int64(len(answer.body)))
				b.ReportAllocs()
				for b.Loop() {
					function.run(b, answer)
				}
			})
		}
	}

	// The two checks that run on a value already decoded.
	for _, answer := range benchAnswers() {
		decoded := answer.target()
		if err := json.Unmarshal([]byte(answer.body), decoded); err != nil {
			b.Fatal(err)
		}
		b.Run("NumericFidelity/"+answer.name, func(b *testing.B) {
			b.SetBytes(int64(len(answer.body)))
			b.ReportAllocs()
			for b.Loop() {
				if err := CheckNumericFidelity(answer.body, decoded); err != nil {
					b.Fatal(err)
				}
			}
		})
		value := reflect.ValueOf(decoded).Elem().Interface()
		b.Run("ValidateExtractedData/"+answer.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := ValidateExtractedData(value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkNumericFidelityShapes is the numeric check for the answers it meets: one
// that reads back as written, one whose members the model wrote in another
// order (a model held to a schema writes them in the order of its properties,
// which are sorted by name), and one it cannot clear by reading the two texts
// side by side, here because a key is written with an escape, so that both are
// decoded into `any` and compared.
//
// On Go 1.27.1 the first two make 1 allocation, the text json.Marshal returns,
// and the third about 2,100 for the list of 5 KB, which is what the check made
// for every answer before it read them side by side.
func BenchmarkNumericFidelityShapes(b *testing.B) {
	for _, answer := range benchAnswers() {
		if answer.name != "record_200B" && answer.name != "list_5KB" {
			continue
		}
		decoded := answer.target()
		if err := json.Unmarshal([]byte(answer.body), decoded); err != nil {
			b.Fatal(err)
		}
		var generic any
		decoder := json.NewDecoder(strings.NewReader(answer.body))
		decoder.UseNumber()
		if err := decoder.Decode(&generic); err != nil {
			b.Fatal(err)
		}
		sorted, err := json.Marshal(generic)
		if err != nil {
			b.Fatal(err)
		}
		escaped := strings.ReplaceAll(answer.body, `"note"`, `"n`+jsonEscape+`u006fte"`)

		for _, shape := range []struct{ name, body string }{
			{"as_written", answer.body},
			{"another_order", string(sorted)},
			{"decoded_and_compared", escaped},
		} {
			b.Run(shape.name+"/"+answer.name, func(b *testing.B) {
				if err := CheckNumericFidelity(shape.body, decoded); err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(shape.body)))
				b.ReportAllocs()
				for b.Loop() {
					if err := CheckNumericFidelity(shape.body, decoded); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkBatchMerge resolves a 200-item batch answer by id, as every batched
// chunk does when it comes back.
func BenchmarkBatchMerge(b *testing.B) {
	type widget struct {
		Name string `json:"name"`
	}
	type widgetLength struct {
		Length int `json:"length"`
	}
	const count = 200
	items := make([]widget, count)
	var body strings.Builder
	body.WriteString(`{"items":[`)
	for i := range items {
		items[i] = widget{Name: fmt.Sprintf("widget-%03d", i)}
		if i > 0 {
			body.WriteString(",")
		}
		fmt.Fprintf(&body, `{"id":%q,"output":{"length":%d}}`, itemID(i), len(items[i].Name))
	}
	body.WriteString(`]}`)

	algebra := NewIDBatchAlgebra[widget, widgetLength](types.BatchItemwise, types.AlgebraIndependent,
		"Summarize each widget's name length.",
		BatchItemCodec[widget, widgetLength]{
			DecodeItem: func(raw json.RawMessage) (widgetLength, error) {
				var out widgetLength
				err := json.Unmarshal(raw, &out)
				return out, err
			},
		})
	b.Run("200items/Merge", func(b *testing.B) {
		b.SetBytes(int64(body.Len()))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := algebra.Merge(body.String(), items); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("200items/Encode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := algebra.Encode(items, types.OpOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// --- The judgments.

type benchAuditSubject struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Plan     string `json:"plan"`
}

// BenchmarkAuditBuildRequest is what an audit builds before it sends: the
// declared schema of the answer, which depends on the policies, and the prose
// shape of the subject.
func BenchmarkAuditBuildRequest(b *testing.B) {
	policyTexts := func(n int) []string {
		policies := make([]string, n)
		for i := range policies {
			policies[i] = fmt.Sprintf("Policy %d: a rule the data must follow, written as a sentence.", i+1)
		}
		return policies
	}
	for _, n := range []int{0, 3, 10} {
		policies := policyTexts(n)
		b.Run(fmt.Sprintf("%d_policies/schema", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = auditResponseSchema(policies, nil)
			}
		})
	}
	b.Run("subject_shape", func(b *testing.B) {
		subject := reflect.TypeOf(benchAuditSubject{})
		b.ReportAllocs()
		for b.Loop() {
			_ = GenerateTypeSchema(subject)
		}
	})
}

// BenchmarkValidateRules is the deterministic part of a validation: the field
// rules Go decides without a model, and the schema of the model's answer.
func BenchmarkValidateRules(b *testing.B) {
	type customer struct {
		Email   string `json:"email"`
		Country string `json:"country"`
		Age     int    `json:"age"`
		Code    string `json:"code"`
	}
	data := customer{Email: "a@b.com", Country: "GB", Age: 44, Code: "ABC-1234"}
	plain := map[string]string{"email": "email", "country": "iso3166-alpha2", "age": "min:18"}
	withRegex := map[string]string{"email": "email", "country": "iso3166-alpha2", "age": "min:18", "code": `regex:^[A-Z]{3}-\d{4}$`}

	b.Run("rules/without_regex", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = applyDeterministicRules(data, plain)
		}
	})
	b.Run("rules/with_regex", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = applyDeterministicRules(data, withRegex)
		}
	})
	b.Run("answer_schema/no_correction", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = validateSchema(true, false, nil)
		}
	})
	b.Run("answer_schema/with_correction", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = validateSchema(true, true, validateCorrectedSchema[customer]())
		}
	})
}
