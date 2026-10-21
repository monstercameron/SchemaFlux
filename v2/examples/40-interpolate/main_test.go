package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func TestRunFillsThreeExplicitGaps(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"complete":[{"timestamp":"10:00","temp_celsius":22.5},{"timestamp":"11:00","temp_celsius":23.3},{"timestamp":"12:00","temp_celsius":24.1}],"filled":[{"index":1,"method":"trend","based_on":[0,2],"reasoning":"between","confidence":0.8}],"gap_count":1,"method":"trend","average_confidence":0.8}`,
		`{"complete":[{"date":"2024-01-08","close":186.4},{"date":"2024-01-09","close":188.6},{"date":"2024-01-10","close":190.8}],"filled":[{"index":1,"method":"pattern","based_on":[0,2],"reasoning":"between","confidence":0.7}],"gap_count":1,"method":"pattern","average_confidence":0.7}`,
		`{"complete":[{"question_num":1,"response":"Very satisfied","score":5},{"question_num":2,"response":"Satisfied with support","score":4},{"question_num":3,"response":"Would recommend","score":5}],"filled":[{"index":1,"method":"semantic","based_on":[0,2],"reasoning":"pattern","confidence":0.6}],"gap_count":1,"method":"semantic","average_confidence":0.6}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"23.3°C", "$188.60 (not market data)", "Satisfied with support"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if model.Calls() != 3 {
		t.Errorf("calls=%d, want 3", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "interpolate sensor") {
		t.Errorf("error=%v", err)
	}
}
