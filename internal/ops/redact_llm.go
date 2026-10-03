// LLM-powered Redact operation for intelligent data masking.

package ops

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/monstercameron/schemaflux/internal/types"
)

// RedactSpan is one redacted range of the text.
//
// Start and End are offsets in characters (runes), not bytes: the span is
// []rune(text)[Start:End]. That is this repository's rule for every offset over
// text, because a byte offset is only meaningful to a program indexing the same
// encoding. They are computed by this library from where the value was found,
// never taken from the model.
type RedactSpan struct {
	Start    int    `json:"start"`    // Start index in characters (0-based)
	End      int    `json:"end"`      // End index in characters (exclusive)
	Category string `json:"category"` // Type of sensitive data (email, phone, ssn, name, etc.)
	Original string `json:"original"` // The original text that was redacted
}

// RedactLLMResult contains the redaction result with detailed span information
type RedactLLMResult struct {
	Text       string         `json:"text"`       // The redacted text
	Original   string         `json:"original"`   // The original text
	Spans      []RedactSpan   `json:"spans"`      // List of redacted spans
	Categories map[string]int `json:"categories"` // Count per category
	Metadata   map[string]any `json:"metadata"`   // Additional metadata
}

// RedactLLMOptions configures the LLM-powered Redact operation
type RedactLLMOptions struct {
	types.OpOptions
	// Categories to detect. "all" means every built-in category: email, phone,
	// ssn, name, address, credit_card, password, api_key, ip, date_of_birth and
	// financial. Any other name is passed to the model as a category of the
	// caller's own.
	Categories []string
	MaskChar   rune // Character to use for masking (default: '*')
	ShowFirst  int  // Number of characters to show at start (default: 0, not negative)
	ShowLast   int  // Number of characters to show at end (default: 0, not negative)
	MinMask    int  // Minimum mask length (default: 3)
}

// NewRedactLLMOptions creates RedactLLMOptions with defaults
func NewRedactLLMOptions() RedactLLMOptions {
	return RedactLLMOptions{
		OpOptions: types.OpOptions{
			Mode:         types.Strict,
			Intelligence: types.Fast,
		},
		Categories: []string{"all"}, // Detect all sensitive data by default
		MaskChar:   '*',
		ShowFirst:  0,
		ShowLast:   0,
		MinMask:    3,
	}
}

// WithCategories sets which categories to detect
func (opts RedactLLMOptions) WithCategories(categories []string) RedactLLMOptions {
	opts.Categories = categories
	return opts
}

// WithMaskChar sets the masking character
func (opts RedactLLMOptions) WithMaskChar(char rune) RedactLLMOptions {
	opts.MaskChar = char
	return opts
}

// WithShowFirst shows N characters at the start
func (opts RedactLLMOptions) WithShowFirst(n int) RedactLLMOptions {
	opts.ShowFirst = n
	return opts
}

// WithShowLast shows N characters at the end
func (opts RedactLLMOptions) WithShowLast(n int) RedactLLMOptions {
	opts.ShowLast = n
	return opts
}

// WithMinMask sets minimum mask length
func (opts RedactLLMOptions) WithMinMask(n int) RedactLLMOptions {
	opts.MinMask = n
	return opts
}

// WithIntelligence sets the intelligence level
func (opts RedactLLMOptions) WithIntelligence(intelligence types.Speed) RedactLLMOptions {
	opts.OpOptions.Intelligence = intelligence
	return opts
}

// Validate checks if options are valid
func (opts RedactLLMOptions) Validate() error {
	if len(opts.Categories) == 0 {
		return fmt.Errorf("at least one category must be specified")
	}
	// A span can carry several categories, joined with commas, so a label is
	// split on them. A requested name with a comma in it could therefore never
	// match, and every span in it would be refused as unrequested.
	for i, category := range opts.Categories {
		if strings.TrimSpace(category) == "" {
			return fmt.Errorf("category %d is blank", i)
		}
		if strings.Contains(category, ",") {
			return fmt.Errorf("category %d has a comma in its name, which is what separates two categories", i)
		}
	}
	if opts.MinMask < 1 {
		return fmt.Errorf("MinMask must be at least 1")
	}
	// A negative count was clamped to zero after the check that stops the
	// unmasked part from covering the whole value, so ShowFirst -10 with ShowLast
	// 14 on a 15-character value showed 14 characters.
	if opts.ShowFirst < 0 || opts.ShowLast < 0 {
		return fmt.Errorf("ShowFirst and ShowLast must not be negative")
	}
	return nil
}

// llmSpanResponse is the expected JSON response from LLM
type llmSpanResponse struct {
	Spans []struct {
		// Text is the sensitive value itself, and it is all the library works
		// from. A position was once asked for as well, first as the authority
		// and then as a hint. Models count characters and Go slices bytes, so
		// a position that is right in the model's terms lands somewhere else in
		// ours; and once every occurrence of a value is masked there is nothing
		// left for a hint to choose between. A response that still carries one
		// is accepted and the field ignored.
		Text string `json:"text"`
		// Category is asked for with every span, and the declared schema requires
		// it; an empty one is how the model says the value fits none.
		Category string `json:"category"`
	} `json:"spans"`
}

// RedactLLM uses an LLM to identify and redact sensitive data.
//
// # How a span is located
//
// The model returns each sensitive value, and the library finds it in the
// document. The model is not asked where the value is.
//
// Offsets used to be the authority, bounds-checked and otherwise trusted. A
// bounds check cannot tell a correct offset from a plausible wrong one, so a
// span that was in range but off by a few characters masked the wrong part of
// the document and reported the wrong thing as redacted -- which is worse than
// not redacting, because it looks done. Offsets were also the one place where
// the model and Go disagree by construction: the model counts characters, Go
// slices bytes, and the two agree only for ASCII.
//
// # Which occurrences are masked
//
// Every occurrence of a value the model named, not only the one it had in mind:
// a value is sensitive wherever it appears. Capitalisation is ignored, and any
// run of whitespace, of any kind, matches any other, because "Dana Kim" quoted
// from a document that breaks the line between the two words, or shouts the
// name in a heading, is still the same value.
//
// One kind of occurrence is left alone, and only for a very short value: three
// characters or fewer, or four digits or fewer. Such a value is also a piece of
// unrelated words and numbers. A short run of digits is left alone inside a
// longer number or identifier, so a reported age of "34" does not change
// "$1345". A short word is left alone in the middle or at the end of a longer
// word, so "Al" does not change "called" or "Hal"; at the start of a longer
// word it is masked, because that is where an inflected form of a name is
// ("Otts Konto" beside "Ott"), and so "Also" loses its first two letters. A
// copy is only ever left alone when some other copy is being masked: if the
// rule would leave every occurrence, all of them are masked, because the value
// was reported and leaving it everywhere is the worse error. Scripts written
// without spaces between words (Chinese, Japanese, Korean, Thai and their
// neighbours) are exempt, since there a neighbouring letter says nothing about
// where a word ends.
// How many copies were left readable is in Metadata["left_inside_longer_words"].
// What that leaves readable: a short name in the middle or at the end of
// another word, and a short number inside a longer one.
//
// Anything longer is masked wherever it appears, inside a word or not. The rule
// was first applied to values of any length, and it left "Müllers" readable in
// a document where "Müller" also stood alone: an inflected or run-together copy
// of a name or a number is the same secret.
//
// What this costs: a name that is also a common word ("Rose", "Mark") is masked
// wherever those letters appear, in any capitalisation, including inside other
// words. The comparison is not Unicode-normalised, so a value the model returns
// in composed form does not match text written with combining marks; that fails
// the call, below.
//
// # What fails the call
//
// A span whose text cannot be found at all, and a span with no text. They used
// to be dropped, on the reasoning that a hallucinated finding should remove
// nothing. That is the safe reading of only one of the two cases. The other is
// a real value the model quoted inexactly, and dropping it returns the document
// with the secret still in it and no error. The two cannot be told apart from
// here, and this operation exists to be trusted with text that must not leak,
// so it refuses (LX-020).
//
// On any error the result carries no text. It used to carry the original, so a
// caller who logged the result and ignored the error logged the secret. No
// error and no log line repeats the document or anything the model wrote.
//
// What is still not checked is whether the substring *is* what the model called
// it: a span labelled "ssn" over an order number is applied. Detection is the
// model's judgement, and this layer verifies location, not classification.
//
// # What does not depend on the model
//
// The document is untrusted, and text in it can persuade a model to answer with
// an empty list. That cannot be told from an honest "nothing found", so it is not
// tried: the deterministic detectors of Redact (email, phone, SSN, payment cards
// by Luhn, IBAN, secret assignments and bearer tokens) are run over the text as
// well, for the categories they cover that the caller asked for, and their
// findings are masked together with the model's. An empty answer from the model
// therefore does not undo them. What they do not cover (names, addresses, IP
// addresses, dates of birth and categories of the caller's own) still depends on
// the model alone. Metadata["spans_from_detectors"] says how many findings came
// from them.
//
// # It is a model call
//
// Detection is only as good as the model's, and it is not deterministic. Where
// you know the shape of your data, tag the fields and use Redact.
func RedactLLM(ctx context.Context, text string, opts RedactLLMOptions) (RedactLLMResult, error) {
	if ctx == nil {
		ctx = opts.Context
	}
	logger := loggerFor(ctx)
	logger.Debug("Starting LLM redact operation", "requestID", opts.RequestID, "textLength", len(text))

	result := RedactLLMResult{
		Original:   text,
		Text:       text,
		Spans:      []RedactSpan{},
		Categories: make(map[string]int),
		Metadata:   make(map[string]any),
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		logger.Error("RedactLLM validation failed", "requestID", opts.RequestID, "error", err)
		return RedactLLMResult{}, &types.OperationError{
			Kind: types.KindConfiguration, Op: "redact_llm", Message: "invalid options: " + err.Error(), Cause: err,
		}
	}

	if strings.TrimSpace(text) == "" {
		return result, nil // Nothing to redact
	}

	// The default deadline applies to every caller who set none. It used to be
	// applied only when ctx was nil, so a caller who passed any context at all,
	// which includes every call made through a Client, ran with no timeout.
	ctx, cancel := operationContext(ctx, 30*time.Second)
	defer cancel()

	// Build prompts
	systemPrompt := buildRedactSystemPrompt(opts)
	userPrompt := buildRedactUserPrompt(text, opts)

	// Call LLM. The shape is declared as well as described: with only the
	// prose, a provider with structured output had nothing to enforce and
	// could return a span with no text.
	opOpts := opts.OpOptions
	opOpts.JSONSchema = redactSchema
	opOpts.SchemaName = "redaction_spans"
	response, err := callLLM(ctx, systemPrompt, userPrompt, opOpts)
	if err != nil {
		logger.Error("RedactLLM LLM call failed", "requestID", opts.RequestID, "error", err)
		return RedactLLMResult{}, fmt.Errorf("LLM call failed: %w", err)
	}

	// Parse LLM response, and add what the deterministic detectors found.
	detected := detectedRedactSpans(text, opts.Categories)
	spans, leftAlone, err := locateRedactSpansWith(response, text, detected)
	if err != nil {
		logger.Error("RedactLLM parse failed", "requestID", opts.RequestID, "error", err)
		return RedactLLMResult{}, err
	}

	// The category list is what the caller asked to have detected, and it was
	// stated in the prompt and checked nowhere -- so a caller who narrowed
	// detection to "email" could get a name redacted anyway. That is not a
	// harmless over-redaction: the caller narrowed the list precisely because
	// the other fields have to survive intact downstream.
	//
	// "all" is the default and means what it says, so it opts out of the check.
	// An empty Category on a span is not matched against anything: the span
	// still redacts, and refusing it would turn a model that merely omitted a
	// label into a failed redaction, which fails in the unsafe direction.
	//
	// A span merged from two overlapping ones carries both categories, joined
	// with a comma. Each is checked on its own: the joined string used to be
	// compared as if it were one category, so two requested categories that
	// happened to overlap were refused as a third that nobody asked for.
	//
	// The label is the model's text and is not repeated unless it is one of the
	// built-in names. A model that writes the value into the category field
	// would otherwise put the secret in the error and in the log.
	everything := containsCategory(opts.Categories, "all")
	for i := range spans {
		labels := strings.Split(spans[i].Category, ",")
		for k, category := range labels {
			// A category is reported under the caller's spelling of it, or the
			// built-in one, so the counts do not depend on how the model
			// capitalised a label.
			labels[k] = categoryAsAsked(category, opts.Categories)
		}
		spans[i].Category = strings.Join(labels, ",")

		if !everything {
			for _, category := range labels {
				if category == "" || containsCategory(opts.Categories, category) {
					continue
				}
				named := "a label of the model's own"
				if containsCategory(builtinRedactCategories, category) {
					named = fmt.Sprintf("%q", strings.ToLower(category))
				}
				logger.Error("RedactLLM returned a span outside the requested categories",
					"requestID", opts.RequestID, "category", named)
				return RedactLLMResult{}, types.Newf(types.KindInvariantViolation, "redact_llm",
					"a span was categorized as %s, which is not among the requested categories", named)
			}
		}
	}

	// Apply redactions. A span the text cannot support is an error: applying it
	// anyway redacts the wrong characters and leaves the sensitive ones in
	// place, which is worse than not redacting at all because it looks done.
	result.Spans = spans
	redacted, err := applyRedactions(text, spans, opts)
	if err != nil {
		// The spans were computed here, from where each value was found, so a
		// span the text cannot support is this library's mistake and not the
		// model's.
		return RedactLLMResult{}, &types.OperationError{
			Kind: types.KindInvariantViolation, Op: "redact_llm", Message: "redaction failed: " + err.Error(), Cause: err,
		}
	}
	result.Text = redacted

	// A span that carries two categories is one finding in each. A span the
	// model gave no label is masked and counted under nothing.
	for _, span := range spans {
		for _, category := range strings.Split(span.Category, ",") {
			if category != "" {
				result.Categories[category]++
			}
		}
	}

	result.Metadata["mask_char"] = string(opts.MaskChar)
	result.Metadata["categories_requested"] = opts.Categories
	result.Metadata["spans_found"] = len(spans)
	result.Metadata["spans_from_detectors"] = len(detected)
	result.Metadata["left_inside_longer_words"] = leftAlone

	logger.Debug("RedactLLM succeeded", "requestID", opts.RequestID, "spansFound", len(spans))

	return result, nil
}

// builtinRedactCategories are the categories the prompt offers when the caller
// asks for "all". They are this library's words, so unlike a label the model
// invents they are safe to repeat in an error.
var builtinRedactCategories = []string{
	"email", "phone", "ssn", "name", "address", "credit_card", "password",
	"api_key", "ip", "date_of_birth", "financial",
}

// buildRedactSystemPrompt creates the system prompt for redaction
func buildRedactSystemPrompt(opts RedactLLMOptions) string {
	prompt := `You are a sensitive data detection expert. Your task is to identify ALL sensitive information in the given text and quote it back exactly.

Return a JSON object with a "spans" array. Each span must have:
- "text": the sensitive value, copied exactly as it appears in the input
- "category": the type of sensitive data

Categories to detect:
`

	if containsCategory(opts.Categories, "all") {
		prompt += `- email: Email addresses
- phone: Phone numbers (any format)
- ssn: Social Security Numbers
- name: Person names (first, last, full)
- address: Physical addresses
- credit_card: Credit card numbers
- password: Passwords or secrets in text
- api_key: API keys, tokens, secrets
- ip: IP addresses
- date_of_birth: Birth dates
- financial: Bank accounts, amounts with context
`
	} else {
		// A category is the caller's text. It is quoted so that a name with a
		// line break in it cannot add a line of instructions to this prompt.
		for _, cat := range opts.Categories {
			quoted, err := promptJSON(cat)
			if err != nil {
				quoted = []byte(`""`)
			}
			prompt += "- " + string(quoted) + "\n"
		}
	}

	prompt += `
IMPORTANT:
1. Be thorough - find every distinct sensitive value
2. "text" must be the value copied EXACTLY as it appears, character for character
3. Quote the whole value, never a fragment of it
4. List each distinct value once. Every occurrence of a listed value is redacted, so do not repeat it
5. "category" must be one of the category names listed above
6. Return ONLY valid JSON, no explanations
7. If no sensitive data found, return {"spans": []}

Example for "Contact john@email.com or call 555-1234. Reply to john@email.com":
{"spans": [{"text": "john@email.com", "category": "email"}, {"text": "555-1234", "category": "phone"}]}`

	return prompt
}

// buildRedactUserPrompt creates the user prompt. The document is untrusted, so
// it is written as one JSON string: text in it that looks like an instruction or
// a heading is a part of the quoted value and cannot end the quotation.
func buildRedactUserPrompt(text string, _ RedactLLMOptions) string {
	quoted, err := promptJSON(text)
	if err != nil {
		quoted = []byte(`""`)
	}
	return "Find all sensitive data in this text and return each value as JSON:\n\n" + string(quoted)
}

// parseRedactResponse parses the LLM response into spans.
func parseRedactResponse(response, originalText string) ([]RedactSpan, error) {
	spans, _, err := locateRedactSpans(response, originalText)
	return spans, err
}

// detectedRedactSpans runs the deterministic detectors over the text for the
// categories the caller asked for and returns what they found, as located byte
// spans labelled with the built-in category name.
//
// A detector's findings are returned only for a category that was asked for, so
// a caller who narrowed the list to "email" does not have phone numbers masked.
// A secret assignment ("password: ...", "api_key=...") is labelled by the key it
// has and dropped when that label was not requested.
func detectedRedactSpans(text string, requested []string) []byteSpan {
	everything := containsCategory(requested, "all")
	wants := func(category string) bool {
		return everything || containsCategory(requested, category)
	}

	var found []byteSpan
	add := func(category string, ranges [][]int) {
		for _, at := range ranges {
			found = append(found, byteSpan{start: at[0], end: at[1], category: category})
		}
	}

	if wants("email") {
		add("email", emailPattern.FindAllStringIndex(text, -1))
	}
	if wants("phone") {
		add("phone", phonePattern.FindAllStringIndex(text, -1))
	}
	if wants("ssn") {
		add("ssn", ssnPattern.FindAllStringIndex(text, -1))
	}
	if wants("financial") {
		add("financial", ibanPattern.FindAllStringIndex(text, -1))
	}

	// A card is labelled credit_card, or financial when that is the only one of
	// the two the caller asked for.
	cardCategory := ""
	switch {
	case wants("credit_card"):
		cardCategory = "credit_card"
	case wants("financial"):
		cardCategory = "financial"
	}
	if cardCategory != "" {
		for _, at := range cardNumberSpans(text) {
			found = append(found, byteSpan{start: at[0], end: at[1], category: cardCategory})
		}
	}

	if wants("password") || wants("api_key") {
		for _, at := range secretAssignmentPattern.FindAllStringSubmatchIndex(text, -1) {
			key := strings.ToLower(text[at[2]:at[3]])
			category := "api_key"
			switch key {
			case "password", "passwd", "pwd", "secret":
				category = "password"
			}
			if wants(category) {
				found = append(found, byteSpan{start: at[0], end: at[1], category: category})
			}
		}
	}
	if wants("api_key") {
		add("api_key", bearerPattern.FindAllStringIndex(text, -1))
	}
	return found
}

// locateRedactSpans parses the response and finds each reported value in the
// text. The count is how many copies of a very short value were left alone
// because they sit inside a longer word: see occurrencesIn.
func locateRedactSpans(response, originalText string) ([]RedactSpan, int, error) {
	return locateRedactSpansWith(response, originalText, nil)
}

// locateRedactSpansWith is locateRedactSpans with the findings of the
// deterministic detectors added: they are merged with the model's, so an empty
// list from the model leaves them in place.
func locateRedactSpansWith(response, originalText string, detected []byteSpan) ([]RedactSpan, int, error) {
	// Clean up response - remove markdown code blocks
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	// None of the errors below repeats the response. A response that names a
	// secret and then fails a check would otherwise carry it into the error, and
	// the reader's refusals name a member by position and carry nothing the model
	// wrote. A response that is not JSON, or is of another shape, is refused with
	// the kind the reader gives it; a span without its text or its category is
	// refused by position.
	var llmResponse llmSpanResponse
	if err := decodeAnswer("redact_llm", response, &llmResponse); err != nil {
		return nil, 0, err
	}
	// A null list is not the same answer as an empty one. `{"spans": []}` says
	// the model looked and found nothing; a null one says nothing about the
	// document at all, and the reader lets a null through for a list.
	if llmResponse.Spans == nil {
		return nil, 0, types.Newf(types.KindSchemaViolation, "redact_llm", "spans is null")
	}

	// Locate each value in the document.
	//
	// Values are found in bytes, because that is what a search returns, and
	// converted to character offsets once, in spansInCharacters. The search
	// used to hand its byte offsets straight to applyRedactions, which counts
	// characters. The two agree only when every character before the span is
	// one byte long; with a single accented letter in front, the mask moved one
	// character to the right, left the start of the secret readable, and still
	// reported the right text in Original (LX-020).
	//
	// Two tests disagreed about the unit and both passed: one sliced the string
	// with these offsets as bytes, the other fed applyRedactions character
	// offsets. Neither ran the two halves together on non-ASCII text.
	if len(llmResponse.Spans) == 0 {
		// The detectors' findings stand whatever the model said.
		return spansInCharacters(originalText, mergeByteSpans(append([]byteSpan(nil), detected...))), 0, nil
	}

	// The folded copy keeps byte offsets in 32 bits, which is half the memory
	// of a full-width offset and enough for any text a model can be sent.
	if len(originalText) > math.MaxInt32 {
		return nil, 0, types.Newf(types.KindConfiguration, "redact_llm", "the text is longer than this operation can search")
	}

	// The text is folded once and every value is searched in that copy. A
	// value the model lists twice is searched once.
	folded := foldText(originalText)
	searched := make(map[string]redactOccurrences, len(llmResponse.Spans))
	found := append([]byteSpan(nil), detected...)
	var skipped [][2]int
	for i, s := range llmResponse.Spans {
		if strings.TrimSpace(s.Text) == "" {
			return nil, 0, types.Newf(types.KindSchemaViolation, "redact_llm", "span %d names no text to redact", i)
		}
		pattern := foldValue(s.Text)
		at, repeated := searched[string(pattern)]
		if !repeated {
			at = occurrencesIn(originalText, folded, pattern)
			searched[string(pattern)] = at
			skipped = append(skipped, at.leftAlone...)
		}
		occurrences := at.masked
		if len(occurrences) == 0 {
			// The value's length says nothing about its content. Its category
			// is the model's text and is left out.
			return nil, 0, types.Newf(types.KindInvariantViolation, "redact_llm",
				"span %d (%d characters) could not be found in the text, so it cannot be redacted",
				i, utf8.RuneCountInString(s.Text))
		}
		category := joinCategories("", s.Category)
		for _, at := range occurrences {
			found = append(found, byteSpan{start: at[0], end: at[1], category: category})
		}
	}

	merged := mergeByteSpans(found)

	// A copy that was left alone and then masked as part of another reported
	// value is not readable, so it is not counted.
	leftAlone := 0
	for _, at := range skipped {
		covered := sort.Search(len(merged), func(i int) bool { return merged[i].end >= at[1] })
		if covered == len(merged) || merged[covered].start > at[0] {
			leftAlone++
		}
	}
	return spansInCharacters(originalText, merged), leftAlone, nil
}

// byteSpan is a located value: byte offsets into the document and the
// categories the model gave it, joined with commas.
type byteSpan struct {
	start, end int
	category   string
}

// foldedText is a document prepared for searching. runes is the text with case
// folded and every run of whitespace collapsed to one space; starts[i] is the
// byte offset in the document where runes[i] begins, and one extra entry holds
// the length of the document, so runes[i] ends at starts[i+1].
//
// It costs eight bytes a character and is built once for a call, however many
// values are searched for.
type foldedText struct {
	runes  []rune
	starts []int32
}

// foldRune maps a character to one representative of the set of characters
// that differ from it only in case, so two characters are equal ignoring case
// exactly when they fold to the same one. The set is Unicode's simple case
// folding, which is what a case-insensitive regexp uses: "K", "k" and the
// kelvin sign are one set. The representative is the smallest member.
func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		// Every ASCII letter's set has its capital as the smallest member.
		if 'a' <= r && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	smallest := r
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		if next < smallest {
			smallest = next
		}
	}
	return smallest
}

// foldText prepares a document for searching. Whitespace is what
// unicode.IsSpace says it is, which is what strings.Fields splits on, so a
// no-break space or a line break in the document matches an ordinary space in
// the quotation.
func foldText(text string) foldedText {
	folded := foldedText{
		runes:  make([]rune, 0, len(text)),
		starts: make([]int32, 0, len(text)+1),
	}
	for at := 0; at < len(text); {
		r, size := utf8.DecodeRuneInString(text[at:])
		if unicode.IsSpace(r) {
			if n := len(folded.runes); n == 0 || folded.runes[n-1] != ' ' {
				folded.runes = append(folded.runes, ' ')
				folded.starts = append(folded.starts, int32(at))
			}
		} else {
			folded.runes = append(folded.runes, foldRune(r))
			folded.starts = append(folded.starts, int32(at))
		}
		at += size
	}
	folded.starts = append(folded.starts, int32(len(text)))
	return folded
}

// foldValue folds a quoted value the same way and drops whitespace at its ends.
func foldValue(value string) []rune {
	folded := make([]rune, 0, len(value))
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsSpace(r) {
			if n := len(folded); n > 0 && folded[n-1] != ' ' {
				folded = append(folded, ' ')
			}
			continue
		}
		folded = append(folded, foldRune(r))
	}
	return folded
}

// find returns the byte range in the document of every occurrence of pattern,
// in order, including occurrences that overlap: "ABAB" is in "ABABAB" twice,
// and skipping the second leaves "AB" of it readable.
//
// It is the Knuth-Morris-Pratt search, so the work is the length of the text
// plus the length of the pattern whatever they contain. The first version
// restarted a regexp one character after each match, which on a long value in
// a repetitive document is about the cube of the length, and the document is
// untrusted.
func (f foldedText) find(pattern []rune) [][2]int {
	if len(pattern) == 0 || len(pattern) > len(f.runes) {
		return nil
	}
	// fallback[k] is how much of the pattern is still matched when the
	// character after its first k+1 characters does not fit.
	fallback := make([]int32, len(pattern))
	for i, matched := 1, 0; i < len(pattern); i++ {
		for matched > 0 && pattern[i] != pattern[matched] {
			matched = int(fallback[matched-1])
		}
		if pattern[i] == pattern[matched] {
			matched++
		}
		fallback[i] = int32(matched)
	}

	var found [][2]int
	matched := 0
	for i, r := range f.runes {
		for matched > 0 && r != pattern[matched] {
			matched = int(fallback[matched-1])
		}
		if r == pattern[matched] {
			matched++
		}
		if matched == len(pattern) {
			first := i - len(pattern) + 1
			found = append(found, [2]int{int(f.starts[first]), int(f.starts[i+1])})
			matched = int(fallback[matched-1])
		}
	}
	return found
}

// redactOccurrences is where one value was found: the occurrences to mask, in
// order of where they start, and the ones left alone.
type redactOccurrences struct {
	masked, leftAlone [][2]int
}

// occurrencesOf returns the byte range of every occurrence of value in text
// that should be masked. It folds the text for one search; a caller with
// several values folds once and uses occurrencesIn.
func occurrencesOf(text, value string) [][2]int {
	return occurrencesIn(text, foldText(text), foldValue(value)).masked
}

// occurrencesIn finds a folded value in a folded text and sorts its
// occurrences into the ones to mask and the ones to leave. Occurrences may
// overlap.
//
// Capitalisation is ignored and any run of whitespace matches any other. An
// exact copy used to be looked for first, and the looser search ran only when
// there was none, so "DANA KIM" stayed readable in a document that also said
// "Dana Kim".
//
// A very short value is also a piece of unrelated words and numbers, and some
// of its copies inside longer words are left alone: see leftInLongerWord. When
// that would leave nothing to mask, every copy is masked. A longer value is
// masked everywhere.
//
// An occurrence to mask takes in the combining marks that follow it: they
// belong to its last letter, and masking the letter without them leaves an
// accent on the mask.
func occurrencesIn(text string, folded foldedText, pattern []rune) redactOccurrences {
	all := folded.find(pattern)
	if len(all) == 0 {
		return redactOccurrences{}
	}

	found := redactOccurrences{masked: all}
	if kind := shortRedactValue(pattern); kind != notShort {
		var masked, left [][2]int
		maskedUpTo := -1
		for _, at := range all {
			// A copy that overlaps or touches a masked one is part of the same
			// run: the letter before it is the masked copy's own. Without
			// this, "aa" in "aaa" left the last "a" readable.
			if at[0] > maskedUpTo && leftInLongerWord(text, at[0], at[1], kind) {
				left = append(left, at)
				continue
			}
			masked = append(masked, at)
			maskedUpTo = at[1]
		}
		if len(masked) > 0 {
			found = redactOccurrences{masked: masked, leftAlone: left}
		}
	}

	for i, at := range found.masked {
		end := at[1]
		for end < len(text) {
			r, size := utf8.DecodeRuneInString(text[end:])
			if !unicode.IsMark(r) {
				break
			}
			end += size
		}
		found.masked[i][1] = end
	}
	return found
}

// How short a value is, for the rule about copies inside longer words.
const (
	notShort    = iota
	shortDigits // four digits or fewer
	shortWord   // three characters or fewer, not all digits
)

// shortRedactValue reports whether a folded value is short enough to be a piece
// of unrelated words or numbers, and which of the two rules applies.
func shortRedactValue(pattern []rune) int {
	if len(pattern) > 4 {
		return notShort
	}
	digits := true
	for _, r := range pattern {
		digits = digits && unicode.IsDigit(r)
	}
	switch {
	case digits:
		return shortDigits
	case len(pattern) <= 3:
		return shortWord
	}
	return notShort
}

// leftInLongerWord reports whether an occurrence of a short value is left
// alone because of where it sits in a longer word.
//
// A short run of digits is left alone when it has a letter or digit on either
// side: numbers do not inflect, and "34" inside "1345" or "3400" is another
// number.
//
// A short word is left alone only when a letter or digit comes before it: in
// the middle or at the end of a longer word. At the start of one it is masked.
// The first version left it alone on either side, and "Otts Konto" stayed
// readable beside a reported "Ott": a suffixed form is how most languages
// inflect a name, and it is the same secret.
func leftInLongerWord(text string, start, end, kind int) bool {
	if kind == shortDigits {
		return embeddedInWord(text, start, end)
	}
	first, _ := utf8.DecodeRuneInString(text[start:end])
	if !joinsAWord(first) {
		return false
	}
	before, size := utf8.DecodeLastRuneInString(text[:start])
	return size > 0 && joinsAWord(before)
}

// embeddedInWord reports whether text[start:end] is a piece of a longer word or
// number: its first character and the one before it both belong to a word, or
// its last character and the one after it do.
func embeddedInWord(text string, start, end int) bool {
	if first, _ := utf8.DecodeRuneInString(text[start:end]); joinsAWord(first) {
		if before, size := utf8.DecodeLastRuneInString(text[:start]); size > 0 && joinsAWord(before) {
			return true
		}
	}
	if last, _ := utf8.DecodeLastRuneInString(text[start:end]); joinsAWord(last) {
		if after, size := utf8.DecodeRuneInString(text[end:]); size > 0 && joinsAWord(after) {
			return true
		}
	}
	return false
}

// unspacedScripts are written without spaces between words, so a letter next
// to a value says nothing about whether the value is a whole word. Hangul is
// here because particles attach directly to the word they follow.
var unspacedScripts = []*unicode.RangeTable{
	unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul,
	unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar,
}

// joinsAWord reports whether r continues a word or a number. A combining mark
// does: it belongs to the letter before it.
func joinsAWord(r rune) bool {
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
		return false
	}
	return !unicode.In(r, unspacedScripts...)
}

// joinCategories adds the categories in more to the comma-joined list in have,
// each trimmed, without repeating one already there. A model's label can itself
// be a list ("email, phone"), which is why more is split too.
func joinCategories(have, more string) string {
	var names []string
	for _, list := range [2]string{have, more} {
		for _, category := range strings.Split(list, ",") {
			category = strings.TrimSpace(category)
			if category != "" && !containsCategory(names, category) {
				names = append(names, category)
			}
		}
	}
	return strings.Join(names, ",")
}

// mergeByteSpans sorts the spans and joins any that overlap or touch. Applying
// them then never masks a character twice, and two values side by side are
// masked as one, so ShowFirst and ShowLast cannot reveal characters from the
// middle of the stretch. A merged span keeps each distinct category, in the
// order they were met, joined with commas.
func mergeByteSpans(spans []byteSpan) []byteSpan {
	if len(spans) <= 1 {
		return spans
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})

	merged := []byteSpan{spans[0]}
	for _, next := range spans[1:] {
		last := &merged[len(merged)-1]
		if next.start > last.end {
			merged = append(merged, next)
			continue
		}
		if next.end > last.end {
			last.end = next.end
		}
		last.category = joinCategories(last.category, next.category)
	}
	return merged
}

// spansInCharacters converts located byte ranges into the spans a caller is
// given: offsets in characters, and the text each one covers. The spans must be
// in order and must not overlap, which mergeByteSpans guarantees.
//
// Original is read from the document. A merged span covers more than either
// value the model named, and used to be left with no Original at all.
func spansInCharacters(text string, located []byteSpan) []RedactSpan {
	spans := make([]RedactSpan, 0, len(located))
	characters, bytesSeen := 0, 0
	for _, span := range located {
		characters += utf8.RuneCountInString(text[bytesSeen:span.start])
		length := utf8.RuneCountInString(text[span.start:span.end])
		spans = append(spans, RedactSpan{
			Start:    characters,
			End:      characters + length,
			Category: span.category,
			Original: text[span.start:span.end],
		})
		characters += length
		bytesSeen = span.end
	}
	return spans
}

// redactResponseSchema is the shape RedactLLM declares for the model's answer:
// a list of spans, each with the text to redact and a category.
func redactResponseSchema() map[string]any {
	span := strictEnvelope(map[string]any{
		"text":     map[string]any{"type": "string"},
		"category": map[string]any{"type": "string"},
	}, "text", "category")
	return strictEnvelope(map[string]any{"spans": listOf(span)}, "spans")
}

// redactSchema is the same for every call, so it is built once. Nothing writes
// to it.
var redactSchema = redactResponseSchema()

// applyRedactions splices the masked spans into the text. The spans carry
// character offsets, and out-of-range or overlapping spans are refused rather
// than applied, because a bad offset here silently redacts the wrong characters
// and leaves the right ones in place.
//
// The four errors it returns (a negative offset, an inverted span, a span past
// the end, an overlap) carry no kind of their own, on purpose. The spans are
// computed in this package from where each value was found in the text, so one
// that the text cannot support is a broken invariant of this library and not a
// fault in what the caller gave or in what the model said. Its one caller
// classifies the error as that, an invariant violation, and a kind given here
// as well would be a second opinion on the same failure.
//
// The text is walked once and copied in bytes. It used to be converted to
// []rune and back, which costs four bytes a character for the whole document
// and replaces any byte that is not valid UTF-8, so text nobody asked to change
// came back changed.
func applyRedactions(text string, spans []RedactSpan, opts RedactLLMOptions) (string, error) {
	if len(spans) == 0 {
		return text, nil
	}

	total := utf8.RuneCountInString(text)

	ordered := make([]RedactSpan, len(spans))
	copy(ordered, spans)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })

	var result strings.Builder
	result.Grow(len(text))

	// at is the byte offset of character number seen. It only moves forward.
	at, seen := 0, 0
	byteOffsetOf := func(character int) int {
		for seen < character {
			_, size := utf8.DecodeRuneInString(text[at:])
			at += size
			seen++
		}
		return at
	}

	lastEnd, copied := 0, 0
	for i, span := range ordered {
		switch {
		case span.Start < 0 || span.End < 0:
			return "", fmt.Errorf("redaction span %d has a negative offset [%d:%d]", i, span.Start, span.End)
		case span.Start > span.End:
			return "", fmt.Errorf("redaction span %d is inverted [%d:%d]", i, span.Start, span.End)
		case span.End > total:
			return "", fmt.Errorf("redaction span %d [%d:%d] exceeds the text, which is %d characters",
				i, span.Start, span.End, total)
		case span.Start < lastEnd:
			return "", fmt.Errorf("redaction span %d [%d:%d] overlaps the previous one, which ended at %d",
				i, span.Start, span.End, lastEnd)
		}

		from := byteOffsetOf(span.Start)
		to := byteOffsetOf(span.End)
		result.WriteString(text[copied:from])
		result.WriteString(maskText(text[from:to], opts))
		lastEnd, copied = span.End, to
	}
	result.WriteString(text[copied:])

	return result.String(), nil
}

// maskText applies the masking strategy to sensitive text
func maskText(text string, opts RedactLLMOptions) string {
	// ShowFirst and ShowLast are documented in characters. Indexing bytes meant
	// that revealing "the first four characters" of a name beginning with a
	// multi-byte rune cut it in half, producing invalid UTF-8 in a function
	// whose entire purpose is to be careful with the text it is handed.
	runes := []rune(text)
	textLen := len(runes)

	// A negative count is clamped before the sum is tested: tested first, a
	// negative one cancelled a positive one, the guard did not fire, and the
	// clamp then left the positive one showing nearly the whole value.
	showFirst := max(opts.ShowFirst, 0)
	showLast := max(opts.ShowLast, 0)

	// Ensure we don't show more than the text length
	if showFirst+showLast >= textLen {
		// If showing first+last would reveal everything, just mask all
		showFirst = 0
		showLast = 0
	}

	maskLen := textLen - showFirst - showLast
	if maskLen < opts.MinMask {
		maskLen = opts.MinMask
	}

	var result strings.Builder

	if showFirst > 0 && showFirst < textLen {
		result.WriteString(string(runes[:showFirst]))
	}

	for i := 0; i < maskLen; i++ {
		result.WriteRune(opts.MaskChar)
	}

	if showLast > 0 && showLast < textLen {
		result.WriteString(string(runes[textLen-showLast:]))
	}

	return result.String()
}

// containsCategory checks if a category list contains a specific category,
// ignoring case and the space around a name.
func containsCategory(categories []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, c := range categories {
		if strings.EqualFold(strings.TrimSpace(c), target) {
			return true
		}
	}
	return false
}

// categoryAsAsked returns the name a category is reported under: the caller's
// spelling when it is one they requested, the built-in spelling when it is one
// of those, and otherwise the label as the model wrote it.
func categoryAsAsked(label string, requested []string) string {
	label = strings.TrimSpace(label)
	for _, name := range requested {
		if name = strings.TrimSpace(name); strings.EqualFold(name, label) {
			return name
		}
	}
	for _, name := range builtinRedactCategories {
		if strings.EqualFold(name, label) {
			return name
		}
	}
	return label
}
