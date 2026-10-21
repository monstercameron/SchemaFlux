package ops

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRedactOptions(t *testing.T) {
	opts := NewRedactOptions()
	if err := opts.Validate(); err != nil {
		t.Errorf("Default options validation failed: %v", err)
	}

	if len(opts.Categories) != 1 || opts.Categories[0] != "PII" {
		t.Errorf("Expected default category PII, got %v", opts.Categories)
	}
	if opts.Strategy != RedactMask {
		t.Errorf("Expected default strategy Mask, got %v", opts.Strategy)
	}
	if opts.MaskText != "" {
		t.Errorf("Expected default mask text empty, got %v", opts.MaskText)
	}
	if opts.MaskChar != '*' {
		t.Errorf("Expected default mask char '*', got %v", opts.MaskChar)
	}
	if opts.MaskLength != 3 {
		t.Errorf("Expected default mask length 3, got %d", opts.MaskLength)
	}
}

func TestRedactOptions_FluentAPI(t *testing.T) {
	opts := NewRedactOptions().
		WithCategories([]string{"PII", "secrets"}).
		WithStrategy(RedactJumble).
		WithMaskText("REDACTED").
		WithMaskChar('#').
		WithMaskLength(5).
		WithJumbleMode(JumbleTypeAware).
		WithJumbleSeed(12345)

	if len(opts.Categories) != 2 {
		t.Errorf("Expected 2 categories, got %d", len(opts.Categories))
	}
	if opts.Strategy != RedactJumble {
		t.Errorf("Expected strategy Jumble, got %v", opts.Strategy)
	}
	if opts.MaskText != "REDACTED" {
		t.Errorf("Expected mask text REDACTED, got %v", opts.MaskText)
	}
	if opts.MaskChar != '#' {
		t.Errorf("Expected mask char '#', got %v", opts.MaskChar)
	}
	if opts.MaskLength != 5 {
		t.Errorf("Expected mask length 5, got %d", opts.MaskLength)
	}
	if opts.JumbleMode != JumbleTypeAware {
		t.Errorf("Expected jumble mode TypeAware, got %v", opts.JumbleMode)
	}
	if opts.JumbleSeed != 12345 {
		t.Errorf("Expected jumble seed 12345, got %d", opts.JumbleSeed)
	}
}

func TestRedactOptions_Validation(t *testing.T) {
	tests := []struct {
		name    string
		opts    RedactOptions
		wantErr bool
	}{
		{"valid defaults", NewRedactOptions(), false},
		{"empty categories", NewRedactOptions().WithCategories([]string{}), true},
		{"invalid strategy", RedactOptions{Categories: []string{"PII"}, Strategy: "invalid"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRedact_String(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		opts     RedactOptions
		expected string
	}{
		{
			name:     "mask email",
			input:    "Contact john@example.com for help",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}),
			expected: "Contact *** for help",
		},
		{
			name:     "mask SSN",
			input:    "SSN: 123-45-6789",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}),
			expected: "SSN: ***",
		},
		{
			name:     "jumble email",
			input:    "Email: test@example.com",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}).WithStrategy(RedactJumble).WithJumbleSeed(42),
			expected: "Email: ttse@mpaleex.com",
		},
		{
			name:     "nil strategy",
			input:    "Phone: 555-123-4567",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}).WithStrategy(RedactNil),
			expected: "Phone: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Redact(tt.input, tt.opts)
			if err != nil {
				t.Errorf("Redact() error = %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("Redact() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestRedact_Struct(t *testing.T) {
	type User struct {
		Name  string `redact:"PII"`
		Email string
		Age   int
	}

	user := User{
		Name:  "John Doe",
		Email: "john@example.com",
		Age:   30,
	}

	// Test masking
	opts := NewRedactOptions().WithCategories([]string{"PII"}).WithStrategy(RedactMask)
	result, err := Redact(user, opts)
	if err != nil {
		t.Errorf("Redact() error = %v", err)
		return
	}

	resultUser := result
	if resultUser.Name != "***" {
		t.Errorf("Expected Name to be masked, got %v", resultUser.Name)
	}
	if resultUser.Email != "***" {
		t.Errorf("Expected Email to be masked, got %v", resultUser.Email)
	}
	if resultUser.Age != 30 {
		t.Errorf("Expected Age to be unchanged, got %v", resultUser.Age)
	}

	// Verify original is unchanged
	if user.Name != "John Doe" {
		t.Errorf("Original user should be unchanged, got Name = %v", user.Name)
	}
}

func TestRedact_Struct_FieldNames(t *testing.T) {
	type TestStruct struct {
		Password string
		Secret   string
		Token    string
		Normal   string
	}

	input := TestStruct{
		Password: "secret123",
		Secret:   "mysecret",
		Token:    "token456",
		Normal:   "normal",
	}

	opts := NewRedactOptions().WithCategories([]string{"secrets"})
	result, err := Redact(input, opts)
	if err != nil {
		t.Errorf("Redact() error = %v", err)
		return
	}

	resultStruct := result
	if resultStruct.Password != "***" {
		t.Errorf("Expected Password to be masked, got %v", resultStruct.Password)
	}
	if resultStruct.Secret != "***" {
		t.Errorf("Expected Secret to be masked, got %v", resultStruct.Secret)
	}
	if resultStruct.Token != "***" {
		t.Errorf("Expected Token to be masked, got %v", resultStruct.Token)
	}
	if resultStruct.Normal != "normal" {
		t.Errorf("Expected Normal to be unchanged, got %v", resultStruct.Normal)
	}
}

func TestRedact_Slice(t *testing.T) {
	users := []string{"john@example.com", "jane@test.com", "normal text"}

	opts := NewRedactOptions().WithCategories([]string{"PII"})
	result, err := Redact(users, opts)
	if err != nil {
		t.Errorf("Redact() error = %v", err)
		return
	}

	resultSlice := result
	if len(resultSlice) != 3 {
		t.Errorf("Expected slice length 3, got %d", len(resultSlice))
		return
	}

	if resultSlice[0] != "***" {
		t.Errorf("Expected first element masked, got %v", resultSlice[0])
	}
	if resultSlice[1] != "***" {
		t.Errorf("Expected second element masked, got %v", resultSlice[1])
	}
	if resultSlice[2] != "normal text" {
		t.Errorf("Expected third element unchanged, got %v", resultSlice[2])
	}
}

func TestRedact_Map(t *testing.T) {
	data := map[string]string{
		"email":    "user@example.com",
		"password": "password: secret123",
		"name":     "John Doe",
	}

	opts := NewRedactOptions().WithCategories([]string{"PII", "secrets"})
	result, err := Redact(data, opts)
	if err != nil {
		t.Errorf("Redact() error = %v", err)
		return
	}

	resultMap := result
	if resultMap["email"] != "***" {
		t.Errorf("Expected email masked, got %v", resultMap["email"])
	}
	if resultMap["password"] != "***" {
		t.Errorf("Expected password masked, got %v", resultMap["password"])
	}
	// "name" is a sensitive key, so its value is redacted by key name. It used
	// to be caught only by the two-capitalised-words pattern, which also
	// redacted "New York" and "Total Revenue" and has been removed.
	if resultMap["name"] != "***" {
		t.Errorf("Expected name masked, got %v", resultMap["name"])
	}
}

func TestRedact_JumbleStrategies(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		mode     JumbleMode
		seed     int64
		validate func(t *testing.T, result string)
	}{
		{
			name:  "jumble basic",
			input: "test@example.com",
			mode:  JumbleBasic,
			seed:  42,
			validate: func(t *testing.T, result string) {
				if len(result) != len("test@example.com") {
					t.Errorf("Expected same length, got %d vs %d", len(result), len("test@example.com"))
				}
				if result == "test@example.com" {
					t.Errorf("Expected jumbled result, got original")
				}
			},
		},
		{
			name:  "jumble email",
			input: "test@example.com",
			mode:  JumbleTypeAware,
			seed:  123,
			validate: func(t *testing.T, result string) {
				if !strings.Contains(result, "@") {
					t.Errorf("Expected @ preserved in email")
				}
				if !strings.Contains(result, ".com") {
					t.Errorf("Expected .com preserved in email")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewRedactOptions().
				WithCategories([]string{"PII"}).
				WithStrategy(RedactJumble).
				WithJumbleMode(tt.mode).
				WithJumbleSeed(tt.seed)

			result, err := Redact(tt.input, opts)
			if err != nil {
				t.Errorf("Redact() error = %v", err)
				return
			}

			resultStr := result
			tt.validate(t, resultStr)
		})
	}
}

func TestRedactWithResult(t *testing.T) {
	input := "Contact john@example.com"

	redacted, result, err := RedactWithResult(input, NewRedactOptions().WithCategories([]string{"PII"}))
	if err != nil {
		t.Errorf("RedactWithResult() error = %v", err)
		return
	}

	if redacted != "Contact ***" {
		t.Errorf("Expected redacted text 'Contact ***', got %v", redacted)
	}

	// This used to assert Count == 0, which was asserting T-09: the function
	// whose purpose is to report what it redacted returned an empty map with a
	// nil error, and an audit read that as "nothing was redacted".
	if result.Count == 0 {
		t.Error("RedactWithResult reported nothing after redacting an email address")
	}
	if len(result.Redacted["PII"]) == 0 {
		t.Errorf("the PII category is empty: %+v", result.Redacted)
	}
}

func TestRedact_InvalidInput(t *testing.T) {
	// Test with invalid options
	_, err := Redact("test", RedactOptions{})
	if err == nil {
		t.Errorf("Expected error for invalid options")
	}
}

func TestRedact_MaskConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		opts     RedactOptions
		expected string
	}{
		{
			name:     "default mask",
			input:    "test@example.com",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}),
			expected: "***",
		},
		{
			name:     "custom mask text",
			input:    "test@example.com",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}).WithMaskText("[REDACTED]"),
			expected: "[REDACTED]",
		},
		{
			name:     "mask char and length",
			input:    "test@example.com",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}).WithMaskChar('#').WithMaskLength(5),
			expected: "#####",
		},
		{
			name:     "original length mask",
			input:    "test@example.com",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}).WithMaskChar('@').WithMaskLength(-1),
			expected: "@@@@@@@@@@@@@@@@", // 16 chars
		},
		{
			name:     "custom char zero length",
			input:    "test@example.com",
			opts:     NewRedactOptions().WithCategories([]string{"PII"}).WithMaskChar('X').WithMaskLength(0),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Redact(tt.input, tt.opts)
			if err != nil {
				t.Errorf("Redact() error = %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("Redact() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// redactStatus is a named string type, to prove the traversal keeps it.
type redactStatus string

type redactWithTime struct {
	When  time.Time
	Email string
}

type redactWithHidden struct {
	Visible string
	hidden  string
}

type redactTaggedHidden struct {
	Visible string
	secret  string `redact:"x"`
}

type redactNested struct {
	Note   *string
	Status redactStatus
	Tags   []redactStatus
	Codes  [2]string
	Extra  any
	Inner  *redactNested
}

// Redact must never panic on a caller's value: each of these panicked.
func TestRedactNeverPanicsOnAwkwardValues(t *testing.T) {
	email := "ada@example.com"
	var nilNote *string
	cases := []struct {
		name  string
		input any
	}{
		{"struct holding a time.Time", redactWithTime{When: time.Unix(1700000000, 0), Email: email}},
		{"struct with an unexported field", redactWithHidden{Visible: email, hidden: email}},
		{"nil pointer field", redactNested{Note: nilNote}},
		{"nil pointer in a slice", []*string{nil, &email}},
		{"nil pointer in a map", map[string]*string{"a": nil, "b": &email}},
		{"named string type field", redactNested{Status: "active"}},
		{"slice of a named string type", []redactStatus{"a", "b"}},
		{"map of a named string type", map[string]redactStatus{"a": "b"}},
		{"pointer to a named string type", func() *redactStatus { s := redactStatus("x"); return &s }()},
		{"array of strings", [3]string{email, "b", "c"}},
		{"array field", redactNested{Codes: [2]string{email, "x"}}},
		{"nil interface field", redactNested{Extra: nil}},
		{"nil slice and map", struct {
			S []string
			M map[string]string
		}{}},
		{"time.Time itself", time.Unix(5, 0)},
		{"pointer to a time.Time", func() *time.Time { n := time.Unix(5, 0); return &n }()},
		{"nested pointer chain", redactNested{Inner: &redactNested{Inner: &redactNested{}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Redact panicked: %v", r)
				}
			}()
			if _, err := Redact(tc.input, NewRedactOptions()); err != nil {
				t.Fatalf("Redact: %v", err)
			}
			if _, _, err := RedactWithResult(tc.input, NewRedactOptions()); err != nil {
				t.Fatalf("RedactWithResult: %v", err)
			}
		})
	}
}

func TestRedactKeepsTheTypesItCannotChange(t *testing.T) {
	when := time.Unix(1700000000, 0)
	out, err := Redact(redactWithTime{When: when, Email: "ada@example.com"}, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if !out.When.Equal(when) {
		t.Errorf("the time.Time changed: %v", out.When)
	}
	if out.Email != "***" {
		t.Errorf("Email = %q, want it masked", out.Email)
	}

	hidden, err := Redact(redactWithHidden{Visible: "ada@example.com", hidden: "kept"}, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if hidden.Visible != "***" || hidden.hidden != "kept" {
		t.Errorf("got %+v, want the visible field masked and the unexported one copied", hidden)
	}
}

func TestRedactNamedStringTypesKeepTheirType(t *testing.T) {
	type record struct {
		Status redactStatus
		Email  redactStatus
	}
	out, err := Redact(record{Status: "active", Email: "ada@example.com"}, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if out.Status != "active" || out.Email != "***" {
		t.Errorf("got %+v", out)
	}

	direct, err := Redact(redactStatus("write to ada@example.com"), NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if direct != "write to ***" {
		t.Errorf("direct = %q", direct)
	}

	list, err := Redact([]redactStatus{"ada@example.com", "fine"}, NewRedactOptions())
	if err != nil || list[0] != "***" || list[1] != "fine" {
		t.Errorf("list = %v, %v", list, err)
	}
}

func TestRedactArraysAndNilsComeBackWithTheirShape(t *testing.T) {
	arr, err := Redact([3]string{"ada@example.com", "b", "c"}, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if arr != [3]string{"***", "b", "c"} {
		t.Errorf("array = %v", arr)
	}

	out, err := Redact([]*string{nil}, NewRedactOptions())
	if err != nil || len(out) != 1 || out[0] != nil {
		t.Errorf("slice with a nil pointer = %v, %v", out, err)
	}

	m, err := Redact(map[string]*string{"a": nil}, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if v, ok := m["a"]; !ok || v != nil {
		t.Errorf("a nil map value was dropped: %v", m)
	}

	// A nil slice or map stays nil: the caller tells "absent" from "empty" by it.
	type holder struct {
		Notes []string
		Tags  map[string]string
		Ptr   *string
		Label string
	}
	got, err := Redact(holder{Label: "x"}, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if got.Notes != nil {
		t.Errorf("a nil slice came back as %#v", got.Notes)
	}
	if got.Tags != nil {
		t.Errorf("a nil map came back as %#v", got.Tags)
	}
	if got.Ptr != nil {
		t.Errorf("a nil pointer came back as %#v", got.Ptr)
	}
	if topSlice, err := Redact([]string(nil), NewRedactOptions()); err != nil || topSlice != nil {
		t.Errorf("a nil slice at the top came back as %#v, %v", topSlice, err)
	}
	if topMap, err := Redact(map[string]string(nil), NewRedactOptions()); err != nil || topMap != nil {
		t.Errorf("a nil map at the top came back as %#v, %v", topMap, err)
	}
}

// A field the caller named for redaction must never come back in the clear
// while being listed as redacted.
func TestRedactNamedNonStringFieldsAreNotLeftReadable(t *testing.T) {
	type record struct {
		SSN      int64
		Password []byte
		Secrets  []string `redact:"x"`
		Token    any
		Active   bool    `redact:"x"`
		Pin      *string `redact:"x"`
	}
	pin := "1234"
	for _, strategy := range []RedactStrategy{RedactMask, RedactNil, RedactRemove, RedactJumble} {
		t.Run(string(strategy), func(t *testing.T) {
			input := record{
				SSN: 123456789, Password: []byte("hunter2"), Secrets: []string{"a", "b"},
				Token: struct{ V string }{"live"}, Active: true, Pin: &pin,
			}
			out, result, err := RedactWithResult(input, NewRedactOptions().WithStrategy(strategy))
			if err != nil {
				t.Fatalf("RedactWithResult: %v", err)
			}
			if out.SSN != 0 || len(out.Password) != 0 || len(out.Secrets) != 0 || out.Active {
				t.Errorf("a field listed as redacted is still readable: %+v", out)
			}
			if out.Pin != nil && *out.Pin == "1234" {
				t.Errorf("Pin pointee is still readable")
			}
			if pin != "1234" {
				t.Errorf("the input was changed: %q", pin)
			}
			if len(result.FieldsRedacted) != 6 {
				t.Errorf("FieldsRedacted = %v, want the six named fields", result.FieldsRedacted)
			}
		})
	}
}

// A field named "token" whose value is a struct is redacted by name too.
func TestRedactNamedInterfaceHoldingAStructIsZeroed(t *testing.T) {
	type record struct{ Token any }
	out, err := Redact(record{Token: struct{ V string }{"live"}}, NewRedactOptions())
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if s, ok := out.Token.(struct{ V string }); !ok || s.V != "" {
		t.Errorf("Token = %#v, want the struct zeroed", out.Token)
	}
}

func TestRedactTaggedUnexportedFieldIsAnError(t *testing.T) {
	out, err := Redact(redactTaggedHidden{Visible: "v", secret: "s3cret"}, NewRedactOptions())
	if err == nil {
		t.Fatal("a field tagged for redaction that cannot be read came back with no error")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("the error does not name the field: %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("the error carries the value: %v", err)
	}
	if out != (redactTaggedHidden{}) {
		t.Errorf("a value came back beside an error: %+v", out)
	}
}

func TestRedactWithResultReportsTheFieldsItCouldNotRead(t *testing.T) {
	_, result, err := RedactWithResult(redactWithHidden{Visible: "x", hidden: "y"}, NewRedactOptions())
	if err != nil {
		t.Fatalf("RedactWithResult: %v", err)
	}
	got, _ := result.Metadata["unreadable_fields"].([]string)
	if len(got) != 1 || got[0] != "hidden" {
		t.Errorf("unreadable_fields = %v, want [hidden]", result.Metadata["unreadable_fields"])
	}

	_, result, _ = RedactWithResult(redactWithTime{When: time.Now()}, NewRedactOptions())
	if got, _ := result.Metadata["unreadable_fields"].([]string); len(got) != 0 {
		t.Errorf("a time.Time was reported as unreadable: %v", got)
	}
}

func TestRedactPatternMatchIsReportedOnceAndNotAsANamedField(t *testing.T) {
	type record struct{ Notes string }
	_, result, err := RedactWithResult(record{Notes: "call (305) 555-1234"}, NewRedactOptions())
	if err != nil {
		t.Fatalf("RedactWithResult: %v", err)
	}
	if len(result.FieldsRedacted) != 0 {
		t.Errorf("a field redacted by pattern was listed as redacted by name: %v", result.FieldsRedacted)
	}
	if result.Count != 1 {
		t.Errorf("Count = %d, want 1 (one value, not the value and its field)", result.Count)
	}
}

func TestRedactRefusesAnUnknownCategory(t *testing.T) {
	for _, category := range []string{"PCI", "PII-US", "secret", "", "  "} {
		t.Run(category, func(t *testing.T) {
			opts := NewRedactOptions().WithCategories([]string{category})
			if err := opts.Validate(); err == nil {
				t.Errorf("Validate accepted category %q", category)
			}
			out, err := Redact("ada@example.com", opts)
			if err == nil {
				t.Fatalf("Redact returned %q with no error for category %q", out, category)
			}
			if out != "" {
				t.Errorf("a value came back beside an error: %q", out)
			}
		})
	}
	for _, category := range []string{"pii", "PII", " Secrets ", "financial"} {
		if err := NewRedactOptions().WithCategories([]string{category}).Validate(); err != nil {
			t.Errorf("Validate refused the known category %q: %v", category, err)
		}
	}
	if err := NewRedactOptions().WithCategories([]string{"PII", "bogus"}).Validate(); err == nil {
		t.Error("a list with one unknown category was accepted")
	}
}

func TestRedactRefusesAnInvalidCustomPattern(t *testing.T) {
	for _, pattern := range []string{"sk-[a-z", "(unterminated[", "a*", ""} {
		t.Run(pattern, func(t *testing.T) {
			opts := NewRedactOptions().WithCustomPatterns([]string{pattern})
			out, err := Redact("key sk-abc here", opts)
			if err == nil {
				t.Fatalf("pattern %q was skipped and the text returned %q", pattern, out)
			}
			if out != "" {
				t.Errorf("a value came back beside an error: %q", out)
			}
		})
	}
}

func TestRedactRefusesABadMaskLength(t *testing.T) {
	for _, length := range []int{-2, -100, 1 << 40} {
		opts := NewRedactOptions().WithMaskLength(length)
		if err := opts.Validate(); err == nil {
			t.Errorf("Validate accepted MaskLength %d", length)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("MaskLength %d panicked: %v", length, r)
				}
			}()
			if _, err := Redact("ada@example.com", opts); err == nil {
				t.Errorf("Redact accepted MaskLength %d", length)
			}
		}()
	}
}

func TestRedactMaskLengthMinusOneCountsCharacters(t *testing.T) {
	// "José" is four characters and five bytes.
	opts := NewRedactOptions().WithMaskLength(-1).WithCustomPatterns([]string{`Jos\x{e9}`})
	got, err := Redact("hi José ok", opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	want := "hi **** ok"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A card number after another group of digits was returned in the clear: the
// leftmost candidate took the year, ended before the last group, and failed
// the Luhn check as a whole.
func TestRedactFindsACardAfterAnotherGroupOfDigits(t *testing.T) {
	for _, text := range []string{
		"Invoice 2024 4111 1111 1111 1111",
		"Invoice 2024 4111-1111-1111-1111 paid",
		"ref 12 34 4111 1111 1111 1111 end",
	} {
		got, err := Redact(text, NewRedactOptions().WithCategories([]string{"financial"}))
		if err != nil {
			t.Fatalf("Redact: %v", err)
		}
		if strings.Contains(got, "4111") {
			t.Errorf("%q came back with the card readable: %q", text, got)
		}
	}
	// A long order number with no card in it is left alone.
	if got, _ := Redact("order 2024 1234 5678 9012", NewRedactOptions().WithCategories([]string{"financial"})); got != "order 2024 1234 5678 9012" {
		t.Errorf("an order number was masked: %q", got)
	}
}

func TestRedactStopsAtACyclicValue(t *testing.T) {
	loop := &redactNested{}
	loop.Inner = loop
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Redact panicked: %v", r)
		}
	}()
	if _, err := Redact(loop, NewRedactOptions()); err == nil {
		t.Error("a cyclic value was accepted")
	}
}

func TestRedactResultIsEmptyBesideAnError(t *testing.T) {
	_, result, err := RedactWithResult("x", RedactOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if result.Redacted != nil || result.Metadata != nil || result.Count != 0 {
		t.Errorf("a result came back beside an error: %+v", result)
	}

	// An error raised while walking the value, after the options were accepted,
	// leaves the result empty as well.
	walked, result, err := RedactWithResult(redactTaggedHidden{Visible: "v", secret: "s3cret"}, NewRedactOptions())
	if err == nil {
		t.Fatal("a tagged unexported field was accepted")
	}
	if walked != (redactTaggedHidden{}) {
		t.Errorf("a value came back beside the error: %+v", walked)
	}
	if result.Redacted != nil || result.Metadata != nil || result.Count != 0 || result.FieldsRedacted != nil {
		t.Errorf("a result came back beside the walk error: %+v", result)
	}
}

// A permutation chosen at random is the identity one time in n!, so a short
// value came back from a jumble exactly as it went in: a field listed as
// redacted and readable, about one time in twenty-four for four distinct
// characters. A value with two different characters in it never comes back
// as it was. One that has nothing to rearrange, a single character or the same
// character repeated, cannot be changed by rearranging it, and is returned as
// it is; RedactMask is the strategy for those.
func TestJumbleBasicNeverReturnsTheInputWhenAnotherArrangementExists(t *testing.T) {
	inputs := []string{"1234", "ab", "ba", "aab", "abb", "abc", "123-45-6789", "ada@example.com", "xy", "0001", "zz9", "日本語"}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			for seed := int64(1); seed <= 2000; seed++ {
				got := jumbleBasic(input, rand.New(rand.NewSource(seed)))
				if got == input {
					t.Fatalf("seed %d returned %q unchanged", seed, input)
				}
				if sortedRunes(got) != sortedRunes(input) {
					t.Fatalf("seed %d: %q is not a rearrangement of %q", seed, got, input)
				}
			}
		})
	}

	for _, input := range []string{"", "a", "0", "aaaa", "0000000", "日"} {
		t.Run("nothing to rearrange: "+input, func(t *testing.T) {
			for seed := int64(1); seed <= 50; seed++ {
				if got := jumbleBasic(input, rand.New(rand.NewSource(seed))); got != input {
					t.Fatalf("seed %d changed %q to %q", seed, input, got)
				}
			}
		})
	}
}

func sortedRunes(text string) string {
	runes := []rune(text)
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	return string(runes)
}
