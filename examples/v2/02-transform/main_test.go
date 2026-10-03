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

func TestRunPrintsTransformedResume(t *testing.T) {
	var model schemafluxtest.Provider
	model.Reply(`{"content":"# Jane Developer\n\n**Contact:** jane.dev@email.com | +1-555-0123\n\n## Skills\n- Go, Python, JavaScript, Docker, Kubernetes, AWS\n\n## Experience\n### Senior Engineer at Tech Corp (2020-2024)\n### Full Stack Developer at StartupXYZ (2018-2020)\n\n## Education\nBS Computer Science, MIT, 2018"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}
	for _, line := range []string{
		"📄 Transform Example - Resume to CV",
		"📥 Input Resume (Structured):",
		"  Name:   Jane Developer",
		"  Email:  jane.dev@email.com",
		"  Skills: [Go Python JavaScript Docker Kubernetes AWS]",
		"✅ Transformed CV (Markdown):",
		"# Jane Developer",
		"### Senior Engineer at Tech Corp (2020-2024)",
		"✨ Success! Structured data → Formatted document",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output missing %q\noutput:\n%s", line, out.String())
		}
	}
	if calls := model.Calls(); calls != 1 {
		t.Errorf("provider received %d calls, want 1", calls)
	}
}

func TestRunReturnsProviderFailure(t *testing.T) {
	var model schemafluxtest.Provider
	model.Fail(&schemaflux.Error{Kind: schemaflux.ErrUnavailable, Message: "scripted provider failure"})
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	var out bytes.Buffer
	err := run(ctx, &out)
	if err == nil {
		t.Fatal("run returned nil error after provider failure")
	}
	if !errors.Is(err, schemaflux.ErrUnavailable) {
		t.Errorf("run error = %v, want ErrUnavailable", err)
	}
	if strings.Contains(out.String(), "Transformed CV") {
		t.Errorf("run printed success after provider failure:\n%s", out.String())
	}
}
