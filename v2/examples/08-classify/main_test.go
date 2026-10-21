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

func TestRun(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"category":"positive","confidence":0.95,"reasoning":"praise","alternatives":[]}`,
		`{"category":"negative","confidence":0.90,"reasoning":"complaint","alternatives":[]}`,
		`{"category":"neutral","confidence":0.75,"reasoning":"mixed but average","alternatives":[]}`,
		`{"category":"positive","confidence":0.92,"reasoning":"strong praise","alternatives":[]}`,
		`{"category":"negative","confidence":0.88,"reasoning":"damaged product","alternatives":[]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Review #1 by Sarah M. 😊", "Review #2 by Mike T. 😞", "Review #3 by Jessica L. 😐",
		"Review #4 by Tom K. 😊", "Review #5 by Rachel P. 😞",
		"😊 Positive Reviews: 2", "😞 Negative Reviews: 2", "😐 Neutral Reviews: 1",
		"Positive: 40%", "Negative: 40%", "Neutral:  20%", "All reviews classified",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if model.Calls() != 5 {
		t.Errorf("provider got %d calls, want 5", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline scripted provider failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil {
		t.Fatal("run swallowed the provider failure")
	}
	if strings.Contains(out.String(), "Classification Summary") {
		t.Fatal("run printed a summary after classification failed")
	}
}
