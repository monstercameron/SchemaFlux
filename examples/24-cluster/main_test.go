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
		`{"clusters":[{"name":"Programming","description":"Programming languages","indices":[0,2,4,6,8],"keywords":["languages"]},{"name":"AI","description":"Artificial intelligence","indices":[1,3,5,7,9],"keywords":["learning"]}],"outlier_indices":[],"quality":0.9}`,
		`{"clusters":[{"name":"Account access","description":"Login problems","indices":[0,3,6],"keywords":["login"]},{"name":"Performance","description":"Slow or failing reports","indices":[1,4,7],"keywords":["reports"]},{"name":"Billing","description":"Payments and refunds","indices":[2,5],"keywords":["payment"]}],"outlier_indices":[],"quality":0.88}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	for _, want := range []string{
		"--- Example 1: Article Clustering ---",
		"Output - Created 2 clusters:",
		"Cluster: Programming",
		"--- Example 2: Support Ticket Clustering ---",
		"Output - Auto-detected 3 ticket clusters:",
		"Category: Account access",
		"--- Example 3: Outlier Detection ---",
		"No outliers detected - all items fit into clusters",
		"=== Cluster Example Complete ===",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q", want)
		}
	}
	if model.Calls() != 2 {
		t.Errorf("provider calls = %d, want 2", model.Calls())
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	providerErr := errors.New("offline provider")
	model.Fail(providerErr)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err == nil || !strings.Contains(err.Error(), "cluster articles") {
		t.Fatalf("run error = %v, want wrapped article clustering failure", err)
	}
}
