// package ops - Parse operation for intelligent data parsing
package ops

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/monstercameron/schemaflux/v2/internal/config"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// parseOpName is the operation name carried by every error Parse returns.
const parseOpName = "parse"

// parseRefuse builds the classified error Parse returns. The message says which
// field, column, position, or option is at fault and never what the caller's
// data held: every caller logs the errors it gets back.
func parseRefuse(kind types.ErrorKind, format string, args ...any) error {
	return types.Newf(kind, parseOpName, format, args...)
}

// ParseResult contains the parsed data and metadata
type ParseResult[T any] struct {
	Data   T      `json:"data"`   // The parsed data
	Format string `json:"format"` // Detected format (json, xml, csv, yaml, custom, etc.)
}

// ParseOptions configures the Parse operation
type ParseOptions struct {
	types.OpOptions
	// AllowLLMFallback lets Parse ask the model when the local parsers cannot
	// read the input. Nothing is sent to a model without it.
	AllowLLMFallback bool
	// AutoFix changes what the model is asked to do in the fallback: repair
	// malformed or incomplete data rather than only reading it. It is read
	// only when the fallback runs, so it does nothing unless AllowLLMFallback
	// is also set; no local repair is attempted.
	AutoFix bool
	// FormatHints are the caller's hints. An entry that names a format ("json",
	// "xml", "csv", "yaml", "yml") selects that parser. An entry that contains
	// "|" ("name|age|job") names the fields of delimited columns in order, and
	// every name in it has to match a field of the target. Other entries are
	// passed to the model as they are.
	FormatHints []string
	// CustomDelimiters are tried, in order, on input that none of the local
	// parsers recognise. A delimiter that does not occur in the input is
	// skipped. They never replace the delimiter of a format that was
	// recognised.
	CustomDelimiters []string
}

// NewParseOptions creates ParseOptions with defaults
func NewParseOptions() ParseOptions {
	return ParseOptions{
		OpOptions: types.OpOptions{
			Mode:         types.TransformMode,
			Intelligence: types.Fast,
		},
		AllowLLMFallback: false,
		AutoFix:          false,
	}
}

// WithAllowLLMFallback enables LLM fallback for complex parsing
func (opts ParseOptions) WithAllowLLMFallback(allow bool) ParseOptions {
	opts.AllowLLMFallback = allow
	return opts
}

// WithAutoFix asks the model, in the LLM fallback, to repair malformed data.
// It has no effect unless AllowLLMFallback is also set.
func (opts ParseOptions) WithAutoFix(autoFix bool) ParseOptions {
	opts.AutoFix = autoFix
	return opts
}

// WithFormatHints provides hints about expected formats
func (opts ParseOptions) WithFormatHints(hints []string) ParseOptions {
	opts.FormatHints = hints
	return opts
}

// WithCustomDelimiters sets custom delimiters for parsing
func (opts ParseOptions) WithCustomDelimiters(delimiters []string) ParseOptions {
	opts.CustomDelimiters = delimiters
	return opts
}

// WithIntelligence sets the intelligence level for LLM fallback
func (opts ParseOptions) WithIntelligence(intelligence types.Speed) ParseOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// Validate refuses an empty custom delimiter, which would split every line
// into single characters.
func (opts ParseOptions) Validate() error {
	for i, delimiter := range opts.CustomDelimiters {
		if delimiter == "" {
			return parseRefuse(types.KindConfiguration, "custom delimiter %d is empty", i+1)
		}
	}
	return nil
}

// toOpOptions converts ParseOptions to types.OpOptions
func (opts ParseOptions) toOpOptions() types.OpOptions {
	return opts.OpOptions
}

// Parse intelligently parses data from various formats into strongly-typed Go structs.
// It uses traditional parsing algorithms for standard formats and can fall back to LLM
// for malformed data recovery and custom format parsing.
//
// Type parameter T specifies the target type to parse into.
//
// Examples:
//
//	// Parse standard JSON
//	result, err := Parse[Person](`{"name":"John","age":30}`, NewParseOptions())
//
//	// Parse a JSON array
//	result, err := Parse[[]Person](`[{"name":"John"},{"name":"Jane"}]`, NewParseOptions())
//
//	// Ask the model to repair malformed data. AutoFix only changes what the
//	// fallback asks for, so AllowLLMFallback has to be set too.
//	result, err := Parse[Person](`{"name":"John","age":30`, NewParseOptions().
//	    WithAllowLLMFallback(true).
//	    WithAutoFix(true))
//
//	// Parse custom format with LLM fallback
//	result, err := Parse[Person]("JOHN|30|ENGINEER", NewParseOptions().
//	    WithAllowLLMFallback(true).
//	    WithFormatHints([]string{"pipe-delimited", "name|age|job"}))
//
//	// Parse mixed format data
//	result, err := Parse[Config](`{"database": "host=localhost\nport=5432"}`, NewParseOptions())
//
// The operation detects the format and refuses what it cannot read: blank
// input, a number that does not fit its field, a hint that names no field, a
// delimited record that sets no field. A refusal returns the zero result, and
// its message names a field or a position, never a value from the input.
func Parse[T any](input any, opts ParseOptions) (ParseResult[T], error) {
	return parseImpl[T](input, opts)
}

func parseImpl[T any](input any, opts ParseOptions) (ParseResult[T], error) {
	log := loggerFor(opts.OpOptions.Context)
	log.Debug("Starting parse operation", "requestID", opts.RequestID, "inputType", fmt.Sprintf("%T", input))

	var result ParseResult[T]

	// Validate options
	if err := opts.Validate(); err != nil {
		log.Error("Parse operation validation failed", "requestID", opts.RequestID, "error", loggedError(err))
		return result, types.Classified(types.KindConfiguration, fmt.Errorf("invalid options: %w", err))
	}

	// Convert input to string
	inputStr, err := NormalizeInput(input)
	if err != nil {
		log.Error("Parse operation input normalization failed", "requestID", opts.RequestID)
		return result, parseRefuse(types.KindInvalidRequest, "there is no input to parse")
	}
	// A blank input holds no data. Every format reads it as an empty value, and
	// the model would be paid to answer a question about nothing.
	if strings.TrimSpace(inputStr) == "" {
		log.Error("Parse operation input is blank", "requestID", opts.RequestID)
		return result, parseRefuse(types.KindInvalidRequest, "the input is blank")
	}

	// Detect format using fast algorithms
	format := detectFormat(inputStr, opts.FormatHints)

	// Try traditional parsing first
	parsedData, err := parseWithAlgorithm[T](inputStr, format, opts)
	if err == nil {
		result.Data = parsedData
		result.Format = format
		log.Debug("Parse operation succeeded with algorithm", "requestID", opts.RequestID, "format", format)
		return result, nil
	}
	// A hint that names no field is the caller's configuration being wrong. The
	// model cannot fix that, and falling back to it would pay for an answer to
	// a question the caller asked wrongly.
	if types.KindOf(err) == types.KindConfiguration {
		log.Error("Parse operation configuration refused", "requestID", opts.RequestID, "error", loggedError(err))
		return result, err
	}

	// If traditional parsing failed but custom delimiters are provided, try
	// delimited parsing with each of them that the input actually contains.
	if format == "unknown" {
		for _, delimiter := range opts.CustomDelimiters {
			if !strings.Contains(inputStr, delimiter) {
				continue
			}
			parsedData, delimErr := parseDelimited[T](inputStr, delimiter, opts)
			if delimErr == nil {
				result.Data = parsedData
				result.Format = "custom-delimited"
				log.Debug("Parse operation succeeded with custom delimiters", "requestID", opts.RequestID)
				return result, nil
			}
			if types.KindOf(delimErr) == types.KindConfiguration {
				log.Error("Parse operation configuration refused", "requestID", opts.RequestID, "error", loggedError(delimErr))
				return result, delimErr
			}
		}
	}

	// If traditional parsing failed and LLM fallback is enabled, try LLM
	if opts.AllowLLMFallback {
		llmResult, llmErr := parseWithLLM[T](inputStr, format, opts)
		if llmErr != nil {
			log.Error("Parse operation LLM fallback failed", "requestID", opts.RequestID, "error", loggedError(llmErr))
			return result, llmErr
		}
		log.Debug("Parse operation succeeded with LLM", "requestID", opts.RequestID)
		return llmResult, nil
	}

	// Return the algorithm error if no fallback
	log.Error("Parse operation failed", "requestID", opts.RequestID, "error", loggedError(err))
	return result, fmt.Errorf("parsing failed: %w (consider enabling AllowLLMFallback)", err)
}

// detectFormat uses fast algorithms to detect data format
func detectFormat(input string, hints []string) string {
	input = strings.TrimSpace(input)

	// Check for explicit hints first
	for _, hint := range hints {
		switch strings.ToLower(hint) {
		case "json":
			return "json"
		case "xml":
			return "xml"
		case "csv":
			return "csv"
		case "yaml", "yml":
			return "yaml"
		}
	}

	// Fast format detection. A JSON document is an object or an array; an array
	// is what Parse[[]T] is given.
	if strings.HasPrefix(input, "{") && strings.HasSuffix(input, "}") {
		return "json"
	}
	if strings.HasPrefix(input, "[") && strings.HasSuffix(input, "]") {
		return "json"
	}
	if strings.HasPrefix(input, "<") && strings.HasSuffix(input, ">") {
		return "xml"
	}
	if strings.Contains(input, "\n") && strings.Contains(input, ",") {
		// Simple CSV detection
		lines := strings.Split(input, "\n")
		if len(lines) >= 2 {
			firstLine := strings.Split(lines[0], ",")
			secondLine := strings.Split(lines[1], ",")
			if len(firstLine) == len(secondLine) && len(firstLine) > 1 {
				return "csv"
			}
		}
	}
	// YAML and pipe-delimited used to be decided by a substring: any input
	// containing ": " was YAML, and any input containing "|" was
	// pipe-delimited. So "Dear Alice: please review the attached" was routed to
	// a YAML parser, and any sentence with a vertical bar in it to a delimited
	// one. Both then failed with an error about the syntax of a format the
	// input never claimed to be in.
	//
	// The rule now is structural: the shape has to hold across lines, which is
	// what distinguishes a document from a sentence that happens to contain a
	// character.
	if looksLikeYAML(input) {
		return "yaml"
	}
	if looksLikeDelimited(input, "|") {
		return "pipe-delimited"
	}
	if looksLikeDelimited(input, "\t") {
		return "tsv"
	}

	return "unknown"
}

// looksLikeYAML requires most non-empty lines to be `key: value` or a list
// item. One colon in a paragraph is not a document.
func looksLikeYAML(input string) bool {
	if strings.Contains(input, "{") || strings.Contains(input, "<") {
		return false
	}

	lines := strings.Split(input, "\n")
	var considered, matched int
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		considered++

		if strings.HasPrefix(trimmed, "- ") {
			matched++
			continue
		}

		colon := strings.Index(trimmed, ":")
		if colon <= 0 {
			continue
		}
		key := trimmed[:colon]
		// A YAML key is one token. "Dear Alice: please review" has a space in
		// what would have to be the key, and prose almost always does.
		if strings.ContainsAny(key, " \t") {
			continue
		}
		matched++
	}

	// A single line is not a document. "Note: the invoice was paid" satisfies
	// key: value and is a sentence; requiring the shape to repeat is what
	// separates the two, and a one-pair YAML document is rare enough that
	// misreading prose is the worse error.
	if considered < 2 {
		return false
	}
	return matched == considered
}

// looksLikeDelimited requires the separator to appear on most lines and to
// produce the same number of fields on each -- a table, not a sentence with a
// bar in it.
func looksLikeDelimited(input, separator string) bool {
	if strings.Contains(input, "{") || strings.Contains(input, "<") {
		return false
	}

	lines := strings.Split(input, "\n")
	var rows []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		return false
	}

	fields := strings.Split(rows[0], separator)
	if len(fields) < 2 {
		return false
	}

	// A single row is a legitimate delimited record -- "Alice|28|Developer" is
	// the documented example -- so it is judged on its fields rather than
	// rejected for being alone. Prose split on a bar produces long fields; a
	// record produces short ones. That is the difference between
	// "Alice|28|Developer" and "See the attached report | it needs review".
	if len(rows) == 1 {
		for _, field := range fields {
			trimmed := strings.TrimSpace(field)
			// A record field is a value: short, and a word or two. A prose
			// fragment is neither. "Alice", "28", and "John Smith" pass; "Use
			// the staging server" and "the production one is locked" do not.
			if trimmed == "" || len(trimmed) > 32 || len(strings.Fields(trimmed)) > 3 {
				return false
			}
		}
		return true
	}

	// Several rows: the shape has to hold across them, which is a much stronger
	// signal than any single line can give.
	for _, row := range rows[1:] {
		if len(strings.Split(row, separator)) != len(fields) {
			return false
		}
	}
	return true
}

// parseWithAlgorithm uses traditional parsing algorithms for standard formats.
// It returns the zero value with every error: a parser that fails halfway has
// already written some of the fields.
func parseWithAlgorithm[T any](input string, format string, opts ParseOptions) (T, error) {
	var parsed T
	var err error

	switch format {
	case "json":
		parsed, err = parseJSON[T](input)
	case "xml":
		parsed, err = parseXML[T](input)
	case "csv":
		parsed, err = parseCSV[T](input)
	case "yaml", "yml":
		parsed, err = parseYAML[T](input)
	case "pipe-delimited":
		parsed, err = parseDelimited[T](input, "|", opts)
	case "tsv":
		parsed, err = parseDelimited[T](input, "\t", opts)
	default:
		err = parseRefuse(types.KindInvalidRequest, "the input is not in a format Parse recognises")
	}

	// Each parser returns the zero value beside its error, so what is returned
	// with a non-nil error is already empty.
	return parsed, err
}

// parseJSON parses JSON using standard library. The decoder's own error is
// not passed on: a type error quotes the number or string it could not read,
// and a syntax error names the character it stopped at.
func parseJSON[T any](input string) (T, error) {
	var result T
	err := json.Unmarshal([]byte(input), &result)
	if err == nil {
		return result, nil
	}

	var zero T
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntaxErr):
		return zero, parseRefuse(types.KindInvalidRequest, "the input is not valid JSON (syntax error at byte %d)", syntaxErr.Offset)
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return zero, parseRefuse(types.KindInvalidRequest, "the JSON value for field %s cannot be read as %s", typeErr.Field, typeErr.Type)
	case errors.As(err, &typeErr):
		return zero, parseRefuse(types.KindInvalidRequest, "the JSON value cannot be read as %s", typeErr.Type)
	default:
		return zero, parseRefuse(types.KindInvalidRequest, "the input cannot be read as JSON into %s", describeParseTarget[T]())
	}
}

// parseXML parses XML using standard library. Its errors name tags and
// quote text from the document, so only the line is reported.
func parseXML[T any](input string) (T, error) {
	var result T
	err := xml.Unmarshal([]byte(input), &result)
	if err == nil {
		return result, nil
	}

	var zero T
	var syntaxErr *xml.SyntaxError
	if errors.As(err, &syntaxErr) {
		return zero, parseRefuse(types.KindInvalidRequest, "the input is not valid XML (syntax error on line %d)", syntaxErr.Line)
	}
	return zero, parseRefuse(types.KindInvalidRequest, "the input cannot be read as XML into %s", describeParseTarget[T]())
}

// parseYAML parses YAML using yaml.v3. Its type errors quote the value they
// could not convert, so none of its text is reported.
func parseYAML[T any](input string) (T, error) {
	var result T
	err := yaml.Unmarshal([]byte(input), &result)
	if err == nil {
		return result, nil
	}

	var zero T
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		return zero, parseRefuse(types.KindInvalidRequest, "the YAML cannot be read as %s: a value has the wrong type", describeParseTarget[T]())
	}
	return zero, parseRefuse(types.KindInvalidRequest, "the input is not valid YAML")
}

// parseCSV parses CSV data
func parseCSV[T any](input string) (T, error) {
	var result, zero T

	reader := csv.NewReader(strings.NewReader(input))
	records, err := reader.ReadAll()
	if err != nil {
		// A csv.ParseError carries a line, a column, and one of the package's
		// fixed sentences; none of them holds the input.
		var parseErr *csv.ParseError
		if errors.As(err, &parseErr) {
			return zero, parseRefuse(types.KindInvalidRequest, "the input is not valid CSV (line %d: %v)", parseErr.Line, parseErr.Err)
		}
		return zero, parseRefuse(types.KindInvalidRequest, "the input is not valid CSV")
	}

	if len(records) < 2 {
		return zero, parseRefuse(types.KindInvalidRequest, "CSV must have at least header and one data row")
	}

	headers := records[0]
	data := records[1:]

	// reflect.TypeOf on a nil interface returns nil, and Kind() on a nil Type
	// panics. Parse[any] produces exactly that: T is an interface, result is its
	// zero value, and a library that panics on a type parameter a caller is
	// allowed to write is not one they can defend against.
	resultType := reflect.TypeOf(result)
	if resultType == nil {
		return zero, parseRefuse(types.KindInvalidRequest, "cannot parse CSV into %s: the target type is not concrete, so there are no fields to map columns onto -- use Parse[YourStruct] or Parse[[]YourStruct]", describeParseTarget[T]())
	}

	// Handle slice types
	if resultType.Kind() == reflect.Slice {
		elemType := resultType.Elem()
		slice := reflect.MakeSlice(resultType, len(data), len(data))

		if elemType.Kind() == reflect.Struct {
			if err := requireMappableHeaders(headers, elemType); err != nil {
				return zero, err
			}
		}

		for i, row := range data {
			item := reflect.New(elemType).Elem()
			if _, err := mapCSVRowToStruct(row, headers, item); err != nil {
				return zero, err
			}
			slice.Index(i).Set(item)
		}

		return slice.Interface().(T), nil
	}

	// Handle single struct
	item := reflect.ValueOf(&result).Elem()
	if item.Kind() == reflect.Struct {
		if err := requireMappableHeaders(headers, item.Type()); err != nil {
			return zero, err
		}
	}
	// One value cannot hold several rows, and taking the first would drop the
	// rest without a word.
	if len(data) != 1 {
		return zero, parseRefuse(types.KindInvalidRequest, "the CSV has %d data rows and the target holds one; use a slice target for several", len(data))
	}
	if _, err := mapCSVRowToStruct(data[0], headers, item); err != nil {
		return zero, err
	}

	return result, nil
}

// requireMappableHeaders refuses a CSV whose headers reach no field at all.
// Returning a zero-valued struct with a nil error -- which is what this used to
// do -- is a parse that says it worked and produced nothing.
//
// The message counts the header cells and does not print them: a file whose
// first row is data has the caller's values in that row.
func requireMappableHeaders(headers []string, structType reflect.Type) error {
	index := csvFieldIndex(structType)
	for _, header := range headers {
		if _, found := index[foldCSVName(header)]; found {
			return nil
		}
	}

	// Report the names a caller can actually write in a header row -- the json
	// tag, or the field name -- rather than the folded lookup keys.
	var known []string
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		// The name encoding/json gives the field, read as it reads the tag.
		tag := parseJSONTag(field.Tag.Get("json"))
		if tag.omitted {
			continue
		}
		if tag.name != "" {
			known = append(known, tag.name)
			continue
		}
		known = append(known, field.Name)
	}
	sort.Strings(known)

	return parseRefuse(types.KindInvalidRequest,
		"no CSV column maps to a field of %s: the header row has %d columns, and the columns it accepts are %v "+
			"(matched ignoring case, spaces, underscores, and hyphens)",
		structType.String(), len(headers), known)
}

// parseDelimited parses delimited data with the delimiter it is given. Which
// delimiter that is belongs to the caller of this function: the one the format
// was detected with, or one of the caller's custom delimiters for text no
// format claimed.
//
// A first line whose cells all name fields of the target is a header row, as in
// CSV, and places the columns by name. Otherwise the columns are placed by the
// caller's field-mapping hint, or by position. Blank lines are skipped. A
// record that sets no field is refused, and so is more than one record for a
// target that holds one.
func parseDelimited[T any](input string, delimiter string, opts ParseOptions) (T, error) {
	var result, zero T

	if strings.TrimSpace(input) == "" {
		return zero, parseRefuse(types.KindInvalidRequest, "the input is blank")
	}
	if delimiter == "" {
		return zero, parseRefuse(types.KindConfiguration, "the delimiter is empty")
	}

	// Same nil-type guard as parseCSV: Parse[any] has no concrete target.
	resultType := reflect.TypeOf(result)
	if resultType == nil {
		return zero, parseRefuse(types.KindInvalidRequest, "cannot parse delimited text into %s: the target type is not concrete, so there are no fields to map columns onto -- use Parse[YourStruct] or Parse[[]YourStruct]", describeParseTarget[T]())
	}

	var lines []string
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}

	isSlice := resultType.Kind() == reflect.Slice
	structType := resultType
	if isSlice {
		structType = resultType.Elem()
	}

	var hintFields []int
	if structType.Kind() == reflect.Struct {
		if hint := fieldMappingHint(opts.FormatHints); hint != "" {
			var err error
			if hintFields, err = resolveHintFields(structType, hint); err != nil {
				return zero, err
			}
		}
	}

	var headers []string
	if structType.Kind() == reflect.Struct && len(lines) >= 2 {
		if first := strings.Split(lines[0], delimiter); isHeaderRow(first, structType) {
			headers = first
			lines = lines[1:]
		}
	}

	if !isSlice && len(lines) != 1 {
		return zero, parseRefuse(types.KindInvalidRequest, "the input has %d records and the target holds one; use a slice target for several", len(lines))
	}

	mapRecord := func(position int, line string, item reflect.Value) error {
		fields := strings.Split(line, delimiter)
		var mapped int
		var err error
		if headers != nil {
			mapped, err = mapCSVRowToStruct(fields, headers, item)
		} else {
			mapped, err = mapDelimitedFieldsToStruct(fields, item, hintFields)
		}
		if err != nil {
			return err
		}
		if mapped == 0 {
			return parseRefuse(types.KindInvalidRequest, "record %d sets no field of %s", position, structType.String())
		}
		return nil
	}

	// Handle slice types
	if isSlice {
		slice := reflect.MakeSlice(resultType, len(lines), len(lines))
		for i, line := range lines {
			item := reflect.New(structType).Elem()
			if err := mapRecord(i+1, line, item); err != nil {
				return zero, err
			}
			slice.Index(i).Set(item)
		}
		return slice.Interface().(T), nil
	}

	item := reflect.ValueOf(&result).Elem()
	if err := mapRecord(1, lines[0], item); err != nil {
		return zero, err
	}
	return result, nil
}

// isHeaderRow reports whether every cell of a line names a field of the
// struct. It is deliberately all of them: a data row that happens to repeat one
// field's name is still data.
func isHeaderRow(cells []string, structType reflect.Type) bool {
	index := csvFieldIndex(structType)
	for _, cell := range cells {
		key := foldCSVName(cell)
		if key == "" {
			return false
		}
		if _, found := index[key]; !found {
			return false
		}
	}
	return true
}

// csvFieldIndex maps the names a CSV header might use to a struct field index.
// A field is reachable by its json tag and by its Go name, both compared with
// case, spaces, underscores, and hyphens folded away — so "full_name",
// "Full Name", and "FullName" all reach FullName.
//
// The old lookup was FieldByName(capitalizeFirst(header)), which compared a
// header against Go field names only: a struct with `json:"full_name"` never
// received its own column, because "Full_name" is not "FullName".
func csvFieldIndex(structType reflect.Type) map[string]int {
	index := map[string]int{}

	add := func(name string, i int) {
		key := foldCSVName(name)
		if key == "" {
			return
		}
		if _, taken := index[key]; !taken {
			index[key] = i
		}
	}

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}

		// The name encoding/json gives the field, read as it reads the tag. A field
		// the tag leaves out has no column, under its Go name either.
		tag := parseJSONTag(field.Tag.Get("json"))
		if tag.omitted {
			continue
		}
		add(tag.name, i)
		add(field.Name, i)
	}

	return index
}

// foldCSVName normalises a header or field name for comparison.
func foldCSVName(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch r {
		case ' ', '_', '-', '.':
			continue
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// mapCSVRowToStruct fills target from row. It reports how many columns it
// mapped, because zero is the case that used to return a zero-valued struct and
// a nil error: success with no data.
//
// A failed conversion names the field and the column's position. It does not
// repeat the header or the value, which are the caller's data.
func mapCSVRowToStruct(row []string, headers []string, target reflect.Value) (int, error) {
	if target.Kind() != reflect.Struct {
		return 0, parseRefuse(types.KindInvalidRequest, "target must be a struct")
	}

	index := csvFieldIndex(target.Type())
	mapped := 0

	for i, header := range headers {
		if i >= len(row) {
			continue
		}

		fieldIndex, found := index[foldCSVName(header)]
		if !found {
			continue
		}

		field := target.Field(fieldIndex)
		if !field.IsValid() || !field.CanSet() {
			continue
		}

		set, err := setFieldValue(field, row[i])
		if err != nil {
			return 0, parseRefuse(types.KindInvalidRequest, "failed to set field %s from column %d: %v", target.Type().Field(fieldIndex).Name, i+1, err)
		}
		if set {
			mapped++
		}
	}

	return mapped, nil
}

// fieldMappingHint returns the first hint that names columns ("name|age|job"),
// or "" when there is none. Hints without a bar are format names or notes for
// the model and say nothing about the columns.
func fieldMappingHint(hints []string) string {
	for _, hint := range hints {
		if strings.Contains(hint, "|") {
			return hint
		}
	}
	return ""
}

// resolveHintFields turns a field-mapping hint into the struct field index of
// each column. A name that matches no field is an error that names the hint:
// hints are the caller's own configuration, and a column silently left out of
// the result is the failure this replaces.
func resolveHintFields(structType reflect.Type, hint string) ([]int, error) {
	index := csvFieldIndex(structType)
	names := strings.Split(hint, "|")
	fields := make([]int, len(names))
	for i, name := range names {
		fieldIndex, found := index[foldCSVName(name)]
		if !found {
			// The hint maps the caller's keys to fields and can hold data, so the
			// message says which column is wrong and how many there are, and never
			// what the hint says.
			return nil, parseRefuse(types.KindConfiguration, "the format hint's column at position %d of %d names no field of %s", i+1, len(names), structType.String())
		}
		fields[i] = fieldIndex
	}
	return fields, nil
}

// mapDelimitedFieldsToStruct places delimited fields into the struct: by the
// field indexes a mapping hint resolved to, or by position when there is no
// hint. It reports how many fields it set.
func mapDelimitedFieldsToStruct(fields []string, target reflect.Value, hintFields []int) (int, error) {
	if target.Kind() != reflect.Struct {
		return 0, parseRefuse(types.KindInvalidRequest, "target must be a struct")
	}

	mapped := 0
	set := func(fieldIndex, position int) error {
		field := target.Field(fieldIndex)
		if !field.CanSet() {
			return nil
		}
		wasSet, err := setFieldValue(field, fields[position])
		if err != nil {
			return parseRefuse(types.KindInvalidRequest, "failed to set field %s from column %d: %v", target.Type().Field(fieldIndex).Name, position+1, err)
		}
		if wasSet {
			mapped++
		}
		return nil
	}

	if hintFields != nil {
		for position, fieldIndex := range hintFields {
			if position >= len(fields) {
				break
			}
			if err := set(fieldIndex, position); err != nil {
				return 0, err
			}
		}
		return mapped, nil
	}

	// Default: map by position
	for i := 0; i < target.NumField() && i < len(fields); i++ {
		if err := set(i, i); err != nil {
			return 0, err
		}
	}
	return mapped, nil
}

// fieldValueError is why one value could not be placed in a field. Its text
// names the field's type, never the value.
type fieldValueError string

func (e fieldValueError) Error() string { return string(e) }

// setFieldValue sets a field value with type conversion. It reports whether it
// set anything: a blank value keeps the zero value and sets nothing.
//
// A number is refused when it does not fit the field's own width. The parse is
// done at 64 bits and reflect's Set* calls do not check, so a uint8 given 300
// held 44, and a float32 given 1e300 held +Inf. NaN and the infinities are
// refused for every float field, since ParseFloat accepts the words.
func setFieldValue(field reflect.Value, value string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return false, nil // Keep zero value
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		intVal, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return false, fieldValueRefusal(err, field)
		}
		if field.OverflowInt(intVal) {
			return false, fieldValueError(fmt.Sprintf("the value does not fit in a %s", field.Type()))
		}
		field.SetInt(intVal)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uintVal, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return false, fieldValueRefusal(err, field)
		}
		if field.OverflowUint(uintVal) {
			return false, fieldValueError(fmt.Sprintf("the value does not fit in a %s", field.Type()))
		}
		field.SetUint(uintVal)
	case reflect.Float32, reflect.Float64:
		floatVal, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return false, fieldValueRefusal(err, field)
		}
		if math.IsNaN(floatVal) || math.IsInf(floatVal, 0) {
			return false, fieldValueError("the value is not a finite number")
		}
		if field.OverflowFloat(floatVal) {
			return false, fieldValueError(fmt.Sprintf("the value does not fit in a %s", field.Type()))
		}
		field.SetFloat(floatVal)
	case reflect.Bool:
		boolVal, err := strconv.ParseBool(value)
		if err != nil {
			return false, fieldValueError("the value is not a valid boolean")
		}
		field.SetBool(boolVal)
	default:
		return false, fieldValueError(fmt.Sprintf("the field type %s is not supported", field.Kind()))
	}

	return true, nil
}

// fieldValueRefusal turns a strconv failure into a message about the field's
// type. strconv's own text quotes the value it could not read.
func fieldValueRefusal(err error, field reflect.Value) error {
	if errors.Is(err, strconv.ErrRange) {
		return fieldValueError(fmt.Sprintf("the value does not fit in a %s", field.Type()))
	}
	return fieldValueError(fmt.Sprintf("the value is not a valid %s", field.Type()))
}

// parseWithLLM uses LLM for complex parsing cases
func parseWithLLM[T any](input string, detectedFormat string, opts ParseOptions) (ParseResult[T], error) {
	var result ParseResult[T]

	ctx, cancel := operationContext(opts.OpOptions.Context, config.GetTimeout())
	defer cancel()

	// Generate type schema
	var zero T
	targetType := reflect.TypeOf(zero)
	typeSchema := GenerateTypeSchema(targetType)

	// Build prompt based on context
	systemPrompt := buildParseSystemPrompt(detectedFormat, opts)
	userPrompt := buildParseUserPrompt(input, typeSchema, detectedFormat, opts)

	// Call LLM
	opt := opts.toOpOptions()
	response, err := callLLM(ctx, systemPrompt, userPrompt, opt)
	if err != nil {
		return result, fmt.Errorf("LLM parsing failed: %w", err)
	}

	// Parse LLM response. The decoder's error is not carried: it can quote the
	// model's answer, which holds the caller's data.
	if err := decodeCallerAnswer(parseOpName, response, &zero); err != nil {
		return result, parseRefuse(types.KindOf(err), "failed to parse LLM response into %s", describeParseTarget[T]())
	}

	result.Data = zero
	result.Format = detectedFormat + " (LLM-assisted)"
	return result, nil
}

// buildParseSystemPrompt creates system prompt for LLM parsing
func buildParseSystemPrompt(format string, opts ParseOptions) string {
	prompt := "You are an expert data parser. Parse the input data into the specified Go struct format.\n\n"

	switch format {
	case "unknown", "custom":
		prompt += "The data appears to be in a custom or unknown format. Analyze the structure and extract meaningful information.\n"
	case "pipe-delimited", "tsv":
		prompt += "Parse the delimited data, handling field mapping and type conversion.\n"
	default:
		prompt += fmt.Sprintf("Parse the %s data, correcting any formatting issues.\n", format)
	}

	if opts.AutoFix {
		prompt += "Attempt to fix malformed or incomplete data where possible.\n"
	}

	prompt += "\nReturn ONLY valid JSON matching the target schema, no explanations or markdown."

	return prompt
}

// buildParseUserPrompt creates user prompt for LLM parsing. The input, the
// hints and the delimiters are the caller's text, so each is written as a JSON
// string: a newline in them is an escape, and none of them can end its line
// and start a heading or an instruction of its own.
func buildParseUserPrompt(input, typeSchema, format string, opts ParseOptions) string {
	prompt := fmt.Sprintf("Parse this data into the following Go struct:\n\n%s\n\n", typeSchema)
	prompt += fmt.Sprintf("Input data (%s):\n%s\n\n", format, parsePromptQuote(input))

	if len(opts.FormatHints) > 0 {
		prompt += fmt.Sprintf("Format hints: %s\n\n", parsePromptQuote(opts.FormatHints))
	}

	if len(opts.CustomDelimiters) > 0 {
		prompt += fmt.Sprintf("Custom delimiters: %s\n\n", parsePromptQuote(opts.CustomDelimiters))
	}

	return prompt
}

// parsePromptQuote writes a caller's string or list of strings as JSON.
func parsePromptQuote(value any) string {
	quoted, err := promptJSON(value)
	if err != nil {
		return strconv.Quote(fmt.Sprint(value))
	}
	return string(quoted)
}

// describeParseTarget names the type parameter for an error message, without
// needing a value of it.
func describeParseTarget[T any]() string {
	targetType := reflect.TypeOf((*T)(nil)).Elem()
	if targetType == nil {
		return "an unnamed type"
	}
	if name := targetType.String(); name != "" {
		return name
	}
	return targetType.Kind().String()
}
