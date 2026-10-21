package desk

import (
	"fmt"
	"reflect"
	"strings"
)

// question is the question a field of T asks, as written in its `ask` tag.
// HandleOneAtATime puts the questions one at a time that Handle puts together,
// and reads them from the same place so that both ask the same thing.
//
// A field T does not have asks a blank question, which the library refuses
// when it is put; a test names every field the flows read.
func question[T any](field string) string {
	asked, _ := reflect.TypeFor[T]().FieldByName(field)
	return asked.Tag.Get("ask")
}

// options is the list a field of T chooses from, as written in its `oneof`
// tag. Each label is trimmed, as the library trims them when it reads the same
// tag in Assess, so that a tag written with spaces after its commas offers the
// same labels in every flow.
func options[T any](field string) []string {
	asked, _ := reflect.TypeFor[T]().FieldByName(field)
	labels := strings.Split(asked.Tag.Get("oneof"), ",")
	for i := range labels {
		labels[i] = strings.TrimSpace(labels[i])
	}
	return labels
}

// scale is the two ends of the rating a field of T takes, as written in its
// `scale` tag: "1-5" is 1 and 5. A tag that is not two whole numbers with a
// dash between them is an error.
func scale[T any](field string) (low, high int, err error) {
	asked, _ := reflect.TypeFor[T]().FieldByName(field)
	tag := asked.Tag.Get("scale")
	_, err = fmt.Sscanf(tag, "%d-%d", &low, &high)
	if err != nil {
		return 0, 0, fmt.Errorf("desk: the scale tag of %s is %q, which is not two numbers with a dash between them", field, tag)
	}
	return low, high, nil
}
