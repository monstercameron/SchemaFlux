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

func TestRunNormalizesAddressAndProduct(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(
		`{"normalized":{"street":"123 Main Street","city":"New York City","state":"NY","country":"United States"},"changes":[{"field":"street","original":"123 Main St.","normalized":"123 Main Street","reason":"expand abbreviation"},{"field":"city","original":"new york city","normalized":"New York City","reason":"canonical spelling"},{"field":"country","original":"USA","normalized":"United States","reason":"canonical country"}],"warnings":[]}`,
		`{"normalized":{"name":"Wireless Mouse","category":"Electronics > Computer Accessories > Mice"},"changes":[{"field":"category","original":"comp accessories / mice","normalized":"Electronics > Computer Accessories > Mice","reason":"standard taxonomy"}],"warnings":[]}`,
	)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"123 Main Street", "New York City", "United States", "Electronics > Computer Accessories > Mice", "Changed address fields:", "Changed product fields:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q: %s", want, out.String())
		}
	}
	if got := model.Calls(); got != 2 {
		t.Errorf("provider calls = %d, want 2", got)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(errors.New("offline failure"))
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model, Retries: -1})
	if err := run(ctx, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "normalize address") {
		t.Errorf("run error = %v, want address failure", err)
	}
}
