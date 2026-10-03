package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

type UserRegistration struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Age      int    `json:"age"`
	Country  string `json:"country"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "validation example failed: %v\nCheck your provider configuration and try again.\n", err)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "✅ Validate Example - User Registration Validation")
	fmt.Fprintln(out, strings.Repeat("=", 60))

	testCases := []struct {
		name string
		data UserRegistration
	}{
		{"Valid Registration", UserRegistration{"johndoe123", "john.doe@example.com", "SecureP@ssw0rd!", 25, "USA"}},
		{"Invalid Email", UserRegistration{"janedoe", "not-an-email", "GoodPassword123!", 30, "Canada"}},
		{"Weak Password", UserRegistration{"bobsmith", "bob@example.com", "12345", 22, "UK"}},
		{"Underage User", UserRegistration{"younguser", "young@example.com", "StrongPass123!", 15, "Germany"}},
	}

	validationRules := `
Validation Rules:
1. Username: 3-20 characters, alphanumeric only
2. Email: Must be valid email format
3. Password: Minimum 8 characters, must include uppercase, lowercase, number, and special character
4. Age: Must be 18 or older
5. Country: Must be a valid country name
`
	// The zero value keeps explanations enabled, matching the old example's
	// WithIncludeExplanations(true). No corrected registration is requested.
	options := schemaflux.ValidateOptions{Rules: validationRules, OmitCorrection: true}

	for i, tc := range testCases {
		fmt.Fprintf(out, "\n%d. %s\n---\n", i+1, tc.name)
		fmt.Fprintf(out, "   Username: %s\n   Email: %s\n", tc.data.Username, tc.data.Email)
		fmt.Fprintf(out, "   Password: %s (length: %d)\n", maskPassword(tc.data.Password), len([]rune(tc.data.Password)))
		fmt.Fprintf(out, "   Age: %d\n   Country: %s\n", tc.data.Age, tc.data.Country)

		// Validate decides exact field rules in Go; these natural-language rules
		// need a model, so this call uses the environment's configured provider.
		result, err := schemaflux.Validate(ctx, tc.data, options)
		if err != nil {
			return fmt.Errorf("validate %q: %w", tc.name, err)
		}
		if result.Verdict == schemaflux.VerdictPass {
			fmt.Fprintln(out, "\n   ✅ VALID - Registration accepted")
			fmt.Fprintf(out, "   Model-claimed confidence: %.0f%%\n", result.ModelConfidence*100)
		} else {
			fmt.Fprintln(out, "\n   ❌ INVALID - Issues found:")
			for _, issue := range result.Issues {
				marker := "ℹ️ "
				switch issue.Severity {
				case "critical", "error":
					marker = "❌"
				case "warning":
					marker = "⚠️ "
				}
				fmt.Fprintf(out, "      %s [%s] %s\n", marker, issue.Subject, issue.Message)
				if issue.Suggestion != "" {
					fmt.Fprintf(out, "         💡 Suggestion: %s\n", issue.Suggestion)
				}
			}
		}
		if result.Summary != "" {
			fmt.Fprintf(out, "\n   📝 Summary: %s\n", result.Summary)
		}
	}

	fmt.Fprintln(out, "\n📊 Validation Summary:")
	fmt.Fprintln(out, "   Total tested: 4 registrations")
	fmt.Fprintln(out, "   Expected: 1 valid, 3 invalid")
	fmt.Fprintln(out, "\n✨ Success! Validation complete")
	return nil
}

func maskPassword(password string) string {
	runes := []rune(password)
	if len(runes) <= 2 {
		return "***"
	}
	return string(runes[:2]) + strings.Repeat("*", len(runes)-2)
}
