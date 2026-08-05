package types

import "fmt"

// ClassifyError represents an error during classification
type ClassifyError struct {
	Input      string
	Categories []string
	Reason     string
	Confidence float64
}

func (e ClassifyError) Error() string {
	return fmt.Sprintf("classification failed: %s (input: %q)", e.Reason, e.Input)
}

// ScoreError represents an error during scoring
type ScoreError struct {
	Input  any
	Reason string
}

func (e ScoreError) Error() string {
	return fmt.Sprintf("scoring failed: %s", e.Reason)
}

// CompareError represents an error during comparison
type CompareError struct {
	A      any
	B      any
	Reason string
}

func (e CompareError) Error() string {
	return fmt.Sprintf("comparison failed: %s", e.Reason)
}

// ChooseError represents an error during selection
type ChooseError struct {
	Options []any
	Reason  string
}

func (e ChooseError) Error() string {
	return fmt.Sprintf("selection failed: %s", e.Reason)
}

// FilterError represents an error during filtering
type FilterError struct {
	Items  []any
	Reason string
}

func (e FilterError) Error() string {
	return fmt.Sprintf("filtering failed: %s", e.Reason)
}

// SortError represents an error during sorting
type SortError struct {
	Items  []any
	Reason string
}

func (e SortError) Error() string {
	return fmt.Sprintf("sorting failed: %s", e.Reason)
}

// ExtractError represents an error during extraction
type ExtractError struct {
	Input      any
	TargetType string
	Reason     string
	RequestID  string
	Timestamp  any // Using any to avoid time import if not needed, or add time import
}

func (e ExtractError) Error() string {
	return fmt.Sprintf("extraction failed: %s", e.Reason)
}

// TransformError represents an error during transformation
type TransformError struct {
	Input      any
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
	Prompt     string
	TargetType string
	Reason     string
	RequestID  string
	Timestamp  any
}

func (e GenerateError) Error() string {
	return fmt.Sprintf("generation failed: %s", e.Reason)
}

// SummarizeError represents an error during summarization
type SummarizeError struct {
	Input  string
	Length int
	Reason string
}

func (e SummarizeError) Error() string {
	return fmt.Sprintf("summarization failed: %s", e.Reason)
}

// RewriteError represents an error during rewriting
type RewriteError struct {
	Input  string
	Reason string
}

func (e RewriteError) Error() string {
	return fmt.Sprintf("rewrite failed: %s", e.Reason)
}

// TranslateError represents an error during translation
type TranslateError struct {
	Input  string
	Reason string
}

func (e TranslateError) Error() string {
	return fmt.Sprintf("translation failed: %s", e.Reason)
}

// ExpandError represents an error during expansion
type ExpandError struct {
	Input  string
	Reason string
}

func (e ExpandError) Error() string {
	return fmt.Sprintf("expansion failed: %s", e.Reason)
}
