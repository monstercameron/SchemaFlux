package tools

import (
	"context"
	"strings"
	"testing"
)

// A template that parses but fails at execution -- referencing a field a
// struct does not have -- is reported as a failure in both the text and
// HTML modes, not just the parse-error path the other tests already cover.
func TestExecuteTemplateReportsAnExecutionErrorTextMode(t *testing.T) {
	type onlyName struct{ Name string }

	result, err := TemplateTool.Execute(context.Background(), map[string]any{
		"template": "{{.Missing.Deeper}}",
		"data":     onlyName{Name: "x"},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("a template referencing a field the data does not have must fail")
	}
}

func TestExecuteTemplateReportsAParseErrorHTMLMode(t *testing.T) {
	result, err := TemplateTool.Execute(context.Background(), map[string]any{
		"template": "{{.Broken",
		"data":     map[string]any{},
		"html":     true,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an unparsable HTML template must fail")
	}
}

func TestExecuteTemplateReportsAnExecutionErrorHTMLMode(t *testing.T) {
	type onlyName struct{ Name string }

	result, err := TemplateTool.Execute(context.Background(), map[string]any{
		"template": "{{.Missing.Deeper}}",
		"data":     onlyName{Name: "x"},
		"html":     true,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("an HTML template referencing a missing field must fail")
	}
}

// StringTemplateTool refuses a missing template, and works when values is
// entirely absent rather than an empty object.
func TestExecuteStringTemplateRequiresATemplate(t *testing.T) {
	result, err := StringTemplateTool.Execute(context.Background(), map[string]any{
		"values": map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("a missing template string must be refused")
	}
}

func TestExecuteStringTemplateWithNoValuesLeavesPlaceholders(t *testing.T) {
	result, err := StringTemplateTool.Execute(context.Background(), map[string]any{
		"template": "Hello, {{name}}!",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.Data.(string) != "Hello, {{name}}!" {
		t.Errorf("got %q", result.Data)
	}
}

// MarkdownTool refuses empty input and supports the plain-text output mode,
// which is what stripMarkdown exists for.
func TestExecuteMarkdownRequiresInput(t *testing.T) {
	result, err := MarkdownTool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Success {
		t.Error("empty markdown must be refused")
	}
}

func TestExecuteMarkdownTextFormatStripsFormatting(t *testing.T) {
	result, err := MarkdownTool.Execute(context.Background(), map[string]any{
		"markdown": "# Title\n\nSome **bold** and _italic_ and `code`.",
		"format":   "text",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	out := result.Data.(string)
	for _, marker := range []string{"#", "**", "_", "`"} {
		if strings.Contains(out, marker) {
			t.Errorf("stripMarkdown left %q in the output: %q", marker, out)
		}
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "bold") {
		t.Errorf("stripMarkdown removed the content, not just the formatting: %q", out)
	}
}

// stripMarkdown directly, for the shapes markdownToHTML does not exercise:
// code blocks, links, and list markers.
func TestStripMarkdown(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"code block", "```\ncode here\n```", ""},
		{"header", "# Title", "Title"},
		{"bold", "**bold**", "bold"},
		{"italic", "*italic*", "italic"},
		{"underscore bold", "__bold__", "bold"},
		{"underscore italic", "_italic_", "italic"},
		{"inline code", "`code`", "code"},
		{"link", "[text](https://example.com)", "text"},
		{"list dash", "- item", "item"},
		{"list star", "* item", "item"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMarkdown(tc.input)
			if got != tc.want {
				t.Errorf("stripMarkdown(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// markdownToHTML's code-block path escapes content rather than trusting it,
// and closes an open list or code block that reaches end of input without a
// closing marker.
func TestMarkdownToHTMLCodeBlockAndUnclosedList(t *testing.T) {
	html := markdownToHTML("```\n<script>x</script>\n```")
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("code block content was not escaped: %q", html)
	}

	// A list with no trailing blank line still gets closed.
	html = markdownToHTML("- one\n- two")
	if !strings.Contains(html, "</ul>") {
		t.Errorf("an unclosed list at end of input must still be closed: %q", html)
	}

	// A code block with no closing fence still gets closed.
	html = markdownToHTML("```\nunclosed")
	if !strings.Contains(html, "</code></pre>") {
		t.Errorf("an unclosed code block at end of input must still be closed: %q", html)
	}
}

// processInlineMarkdown handles the inline shapes markdownToHTML's own test
// only exercises through headers and list items: links, inline code, and
// mixed emphasis together.
func TestProcessInlineMarkdown(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"bold", "**bold**", "<strong>bold</strong>"},
		{"underscore bold", "__bold__", "<strong>bold</strong>"},
		{"code", "`code`", "<code>code</code>"},
		{"link", "[text](https://example.com)", `<a href="https://example.com">text</a>`},
		{"escapes html", "<b>raw</b>", "&lt;b&gt;raw&lt;/b&gt;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := processInlineMarkdown(tc.input)
			if !strings.Contains(got, tc.want) {
				t.Errorf("processInlineMarkdown(%q) = %q, want it to contain %q", tc.input, got, tc.want)
			}
		})
	}
}
