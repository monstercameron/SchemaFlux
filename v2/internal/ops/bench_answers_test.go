package ops

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// BenchmarkDecodeAnswer reads three answers back with each reader there is: the
// decode alone (ParseJSON), decodeAnswer, and decodeCallerAnswer, which is for a
// caller's type. Scan is the walk over the text that decodeAnswer adds to the
// decode, on its own, and TokenWalk is what a check made of the decoder's tokens
// would cost before it had checked anything, which is the other way to build
// one.
func BenchmarkDecodeAnswer(b *testing.B) {
	for _, shape := range answerBudgetShapes() {
		plan := typePlanFor(reflect.TypeOf(shape.target())).answerShape()
		readers := []struct {
			name string
			run  func()
		}{
			{"ParseJSON", func() {
				if err := ParseJSON(shape.body, shape.target()); err != nil {
					b.Fatal(err)
				}
			}},
			{"decodeAnswer", func() {
				if err := decodeAnswer("bench", shape.body, shape.target()); err != nil {
					b.Fatal(err)
				}
			}},
			{"decodeCallerAnswer", func() {
				if err := decodeCallerAnswer("bench", shape.body, shape.target()); err != nil {
					b.Fatal(err)
				}
			}},
			{"Scan", func() {
				scan := answerScan{s: shape.body}
				if !scan.value(plan) {
					b.Fatal("refused")
				}
			}},
			{"TokenWalk", func() {
				decoder := json.NewDecoder(strings.NewReader(shape.body))
				for {
					if _, err := decoder.Token(); err == io.EOF {
						return
					} else if err != nil {
						b.Fatal(err)
					}
				}
			}},
		}
		for _, reader := range readers {
			b.Run(reader.name+"/"+shape.name, func(b *testing.B) {
				b.SetBytes(int64(len(shape.body)))
				b.ReportAllocs()
				for b.Loop() {
					reader.run()
				}
			})
		}
	}
}

// BenchmarkReadBatchAnswers reads the answer of a chunk of twenty items, as the
// id protocol and the merged protocol do: once complete, which is one decode, and
// once with a bad element in the middle, which is read again element by element.
func BenchmarkReadBatchAnswers(b *testing.B) {
	var ids, merged []string
	for i := range 20 {
		ids = append(ids, fmt.Sprintf(`{"id":"i-%06d","output":{"label":"label number %d","note":"some longer text for item %d"}}`, i+1, i, i))
		merged = append(merged, fmt.Sprintf(`{"index":%d,"data":{"Name":"Name number %d","Age":%d}}`, i, i, i))
	}
	badIDs := append([]string{}, ids...)
	badIDs[10] = `{"id":"i-000011"}`
	badMerged := append([]string{}, merged...)
	badMerged[10] = `{"index":10}`
	wrap := func(elements []string) string { return `{"items":[` + strings.Join(elements, ",") + `]}` }
	array := func(elements []string) string { return `[` + strings.Join(elements, ",") + `]` }

	runs := []struct {
		name string
		run  func()
	}{
		{"id protocol, complete", func() { _, _ = readIDBatchAnswers(wrap(ids)) }},
		{"id protocol, one bad element", func() { _, _ = readIDBatchAnswers(wrap(badIDs)) }},
		{"merged, complete", func() { parseMergedResponse[Person](array(merged), 20) }},
		{"merged, one bad element", func() { parseMergedResponse[Person](array(badMerged), 20) }},
	}
	for _, r := range runs {
		b.Run(r.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				r.run()
			}
		})
	}
}
