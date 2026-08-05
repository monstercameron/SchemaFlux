package types

import (
	"fmt"
	"reflect"
	"strings"
)

// DescribeValue summarises a value without reproducing it: its kind and size,
// never its contents. Error structs used to retain the operation's input, so
// every log line, every wrapped error, and every crash report carried a copy of
// the caller's payload. They keep a description instead.
func DescribeValue(value any) string {
	if value == nil {
		return "nil"
	}

	switch typed := value.(type) {
	case string:
		return fmt.Sprintf("%s, %d bytes", describeText(typed), len(typed))
	case []byte:
		return fmt.Sprintf("%s, %d bytes", describeText(string(typed)), len(typed))
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return fmt.Sprintf("%s of %d", reflected.Type().String(), reflected.Len())
	default:
		return reflected.Type().String()
	}
}

// describeText classifies a string by shape alone.
func describeText(text string) string {
	trimmed := strings.TrimSpace(text)
	switch {
	case trimmed == "":
		return "empty"
	case strings.HasPrefix(trimmed, "```"):
		return "fenced block"
	case strings.HasPrefix(trimmed, "{"):
		return "json object"
	case strings.HasPrefix(trimmed, "["):
		return "json array"
	case strings.HasPrefix(trimmed, "<"):
		return "markup"
	default:
		return "text"
	}
}

// ClassifyError represents an error during classification
type ClassifyError struct {
	// InputShape describes the input without reproducing it.
	InputShape string
	Categories []string
	Reason     string
	Confidence float64
}

func (e ClassifyError) Error() string {
	return fmt.Sprintf("classification failed: %s (input was %s)", e.Reason, e.InputShape)
}

// ScoreError represents an error during scoring
type ScoreError struct {
	InputShape string
	Reason     string
}

func (e ScoreError) Error() string {
	return fmt.Sprintf("scoring failed: %s", e.Reason)
}

// CompareError represents an error during comparison
type CompareError struct {
	AShape string
	BShape string
	Reason string
}

func (e CompareError) Error() string {
	return fmt.Sprintf("comparison failed: %s", e.Reason)
}

// ChooseError represents an error during selection
type ChooseError struct {
	OptionCount int
	Reason      string
}

func (e ChooseError) Error() string {
	return fmt.Sprintf("selection failed: %s", e.Reason)
}

// FilterError represents an error during filtering
type FilterError struct {
	ItemCount int
	Reason    string
}

func (e FilterError) Error() string {
	return fmt.Sprintf("filtering failed: %s", e.Reason)
}

// SortError represents an error during sorting
type SortError struct {
	ItemCount int
	Reason    string
}

func (e SortError) Error() string {
	return fmt.Sprintf("sorting failed: %s", e.Reason)
}

// ExtractError represents an error during extraction
type ExtractError struct {
	InputShape string
	TargetType string
	Reason     string
	RequestID  string
	Timestamp  any // Using any to avoid time import if not needed, or add time import

	// Err is the underlying cause, so errors.Is and errors.As reach it.
	Err error
}

func (e ExtractError) Error() string {
	return fmt.Sprintf("extraction failed: %s", e.Reason)
}

// Unwrap exposes the cause. Without it, a sentinel such as ErrNoProvider is
// unreachable behind the wrapper and callers resort to string matching.
func (e ExtractError) Unwrap() error { return e.Err }

// TransformError represents an error during transformation
type TransformError struct {
	InputShape string
	FromType   string
	ToType     string
	Reason     string
	Confidence float64
	RequestID  string
	Timestamp  any
}

func (e TransformError) Error() string {
	return fmt.Sprintf("transformation failed: %s", e.Reason)
}

// GenerateError represents an error during generation
type GenerateError struct {
	PromptShape string
	TargetType  string
	Reason      string
	RequestID   string
	Timestamp   any
}

func (e GenerateError) Error() string {
	return fmt.Sprintf("generation failed: %s", e.Reason)
}

// SummarizeError represents an error during summarization
type SummarizeError struct {
	InputShape string
	Length     int
	Reason     string
}

func (e SummarizeError) Error() string {
	return fmt.Sprintf("summarization failed: %s", e.Reason)
}

// RewriteError represents an error during rewriting
type RewriteError struct {
	InputShape string
	Reason     string
}

func (e RewriteError) Error() string {
	return fmt.Sprintf("rewrite failed: %s", e.Reason)
}

// TranslateError represents an error during translation
type TranslateError struct {
	InputShape string
	Reason     string
}

func (e TranslateError) Error() string {
	return fmt.Sprintf("translation failed: %s", e.Reason)
}

// ExpandError represents an error during expansion
type ExpandError struct {
	InputShape string
	Reason     string
}

func (e ExpandError) Error() string {
	return fmt.Sprintf("expansion failed: %s", e.Reason)
}
