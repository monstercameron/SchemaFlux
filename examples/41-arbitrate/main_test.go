package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

func answer() string {
	rules := func(score float64) map[string]any {
		return map[string]any{"r1": map[string]any{"passed": true, "score": score, "reasoning": "checked"}, "r2": map[string]any{"passed": true, "score": score, "reasoning": "checked"}}
	}
	data := map[string]any{"options": map[string]any{"o1": rules(0.9), "o2": rules(0.6)}, "reasoning": "compare rules", "confidence": 0.7}
	b, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("marshal test answer: %v", err))
	}
	return string(b)
}

func TestRunArbitratesThreeReviewQueues(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(answer(), answer(), answer())
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Acme Manufacturing", "Cloud A", "CLM-001", "human review"} {
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
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "arbitrate vendors") {
		t.Errorf("error=%v", err)
	}
}
