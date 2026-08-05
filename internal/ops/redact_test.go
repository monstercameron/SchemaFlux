package ops

import (
	"strings"
	"testing"
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
