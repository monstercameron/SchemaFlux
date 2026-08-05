package tools

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	texttemplate "text/template"
)

// TemplateTool renders Go templates
var TemplateTool = &Tool{
	Name:        "template",
	Description: "Render Go text/html templates with provided data.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"template": StringParam("Go template string"),
		"data":     {Type: "object", Description: "Data to pass to template"},
		"html":     BoolParam("Use HTML escaping (default: false)"),
	}, []string{"template", "data"}),
	Execute: executeTemplate,
}

func executeTemplate(ctx context.Context, params map[string]any) (Result, error) {
	tmplStr, _ := params["template"].(string)
	data := params["data"]
	useHTML, _ := params["html"].(bool)

	if tmplStr == "" {
		return ErrorResultFromError(fmt.Errorf("template is required")), nil
	}

	var buf bytes.Buffer
	var err error

	if useHTML {
		tmpl, parseErr := template.New("template").Parse(tmplStr)
		if parseErr != nil {
			return ErrorResultFromError(fmt.Errorf("template parse error: %w", parseErr)), nil
		}
		err = tmpl.Execute(&buf, data)
	} else {
		tmpl, parseErr := texttemplate.New("template").Parse(tmplStr)
		if parseErr != nil {
			return ErrorResultFromError(fmt.Errorf("template parse error: %w", parseErr)), nil
		}
		err = tmpl.Execute(&buf, data)
	}

	if err != nil {
		return ErrorResultFromError(fmt.Errorf("template execution error: %w", err)), nil
	}

	return NewResultWithMeta(buf.String(), map[string]any{"html": useHTML}), nil
}

// StringTemplateTool performs simple string interpolation
var StringTemplateTool = &Tool{
	Name:        "string_template",
	Description: "Simple string interpolation using {{key}} syntax.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"template": StringParam("Template string with {{key}} placeholders"),
		"values":   {Type: "object", Description: "Key-value pairs for interpolation"},
	}, []string{"template", "values"}),
	Execute: executeStringTemplate,
}

func executeStringTemplate(ctx context.Context, params map[string]any) (Result, error) {
	tmplStr, _ := params["template"].(string)
	values, _ := params["values"].(map[string]any)

	if tmplStr == "" {
		return ErrorResultFromError(fmt.Errorf("template is required")), nil
	}
	if values == nil {
		values = make(map[string]any)
	}

	result := tmplStr
	replaced := 0

	// Replace {{key}} with values
	re := regexp.MustCompile(`\{\{(\w+)\}\}`)
	result = re.ReplaceAllStringFunc(result, func(match string) string {
		key := match[2 : len(match)-2] // Remove {{ and }}
		if val, ok := values[key]; ok {
			replaced++
			return fmt.Sprint(val)
		}
		return match // Keep original if no value
	})

	return NewResultWithMeta(result, map[string]any{
		"replacements": replaced,
	}), nil
}

// MarkdownTool converts markdown to HTML or plain text
var MarkdownTool = &Tool{
	Name:        "markdown",
	Description: "Basic markdown to HTML conversion.",
	Category:    CategoryData,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"markdown": StringParam("Markdown text to convert"),
		"format":   EnumParam("Output format", []string{"html", "text"}),
	}, []string{"markdown"}),
	Execute: executeMarkdown,
}

func executeMarkdown(ctx context.Context, params map[string]any) (Result, error) {
	md, _ := params["markdown"].(string)
	format := "html"
	if f, ok := params["format"].(string); ok {
		format = f
	}

	if md == "" {
		return ErrorResultFromError(fmt.Errorf("markdown is required")), nil
	}

	var result string
	if format == "text" {
		// Simple markdown to plain text (strip formatting)
		result = stripMarkdown(md)
	} else {
		// Simple markdown to HTML conversion
		result = markdownToHTML(md)
	}

	return NewResultWithMeta(result, map[string]any{"format": format}), nil
}

// markdownToHTML does basic markdown to HTML conversion
func markdownToHTML(md string) string {
	lines := strings.Split(md, "\n")
	var result strings.Builder
	inList := false
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Code blocks
		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				result.WriteString("</code></pre>\n")
				inCodeBlock = false
			} else {
				result.WriteString("<pre><code>")
				inCodeBlock = true
			}
			continue
		}
		if inCodeBlock {
			result.WriteString(template.HTMLEscapeString(line) + "\n")
			continue
		}

		// Empty lines
		if trimmed == "" {
			if inList {
				result.WriteString("</ul>\n")
				inList = false
			}
			result.WriteString("\n")
			continue
		}

		// Headers
		if strings.HasPrefix(trimmed, "# ") {
			result.WriteString(fmt.Sprintf("<h1>%s</h1>\n", template.HTMLEscapeString(trimmed[2:])))
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			result.WriteString(fmt.Sprintf("<h2>%s</h2>\n", template.HTMLEscapeString(trimmed[3:])))
			continue
		}
		if strings.HasPrefix(trimmed, "### ") {
			result.WriteString(fmt.Sprintf("<h3>%s</h3>\n", template.HTMLEscapeString(trimmed[4:])))
			continue
		}

		// Lists
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			if !inList {
				result.WriteString("<ul>\n")
				inList = true
			}
			content := processInlineMarkdown(trimmed[2:])
			result.WriteString(fmt.Sprintf("  <li>%s</li>\n", content))
			continue
		}

		// Regular paragraph
		if inList {
			result.WriteString("</ul>\n")
			inList = false
		}
		content := processInlineMarkdown(trimmed)
		result.WriteString(fmt.Sprintf("<p>%s</p>\n", content))
	}

	if inList {
		result.WriteString("</ul>\n")
	}
	if inCodeBlock {
		result.WriteString("</code></pre>\n")
	}

	return result.String()
}

// processInlineMarkdown handles bold, italic, code, and links
func processInlineMarkdown(text string) string {
	// Escape HTML first
	text = template.HTMLEscapeString(text)

	// Bold: **text** or __text__
	boldRe := regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	text = boldRe.ReplaceAllStringFunc(text, func(match string) string {
		content := match[2 : len(match)-2]
		return "<strong>" + content + "</strong>"
	})

	// Italic: *text* or _text_
	italicRe := regexp.MustCompile(`\*(.+?)\*|_(.+?)_`)
	text = italicRe.ReplaceAllStringFunc(text, func(match string) string {
		content := match[1 : len(match)-1]
		return "<em>" + content + "</em>"
	})

	// Inline code: `code`
	codeRe := regexp.MustCompile("`([^`]+)`")
	text = codeRe.ReplaceAllString(text, "<code>$1</code>")

	// Links: [text](url)
	linkRe := regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	text = linkRe.ReplaceAllString(text, `<a href="$2">$1</a>`)

	return text
}

// stripMarkdown removes markdown formatting for plain text
func stripMarkdown(md string) string {
	// Remove code blocks
	codeBlockRe := regexp.MustCompile("(?s)```.*?```")
	md = codeBlockRe.ReplaceAllString(md, "")

	// Remove headers
	md = regexp.MustCompile(`^#{1,6}\s+`).ReplaceAllString(md, "")

	// Remove bold/italic
	md = regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(md, "$1")
	md = regexp.MustCompile(`\*(.+?)\*`).ReplaceAllString(md, "$1")
	md = regexp.MustCompile(`__(.+?)__`).ReplaceAllString(md, "$1")
	md = regexp.MustCompile(`_(.+?)_`).ReplaceAllString(md, "$1")

	// Remove inline code
	md = regexp.MustCompile("`([^`]+)`").ReplaceAllString(md, "$1")

	// Remove links, keep text
	md = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`).ReplaceAllString(md, "$1")

	// Remove list markers
	md = regexp.MustCompile(`(?m)^[\-\*]\s+`).ReplaceAllString(md, "")

	return strings.TrimSpace(md)
}

func init() {
	mustRegister(TemplateTool)
	mustRegister(StringTemplateTool)
	mustRegister(MarkdownTool)
}
