// Example: 12-validate
//
// Operation: Validate[T] - Validate data against semantic rules
//
// Input: 4 UserRegistration structs with various issues
//   - Valid: johndoe123 (all rules pass)
//   - Invalid Email: "not-an-email" (bad format)
//   - Weak Password: "12345" (too short, missing requirements)
//   - Underage: age 15 (must be 18+)
//
// Validation Rules:
//   - Username: 3-20 chars, alphanumeric
//   - Email: valid format
//   - Password: 8+ chars, uppercase, lowercase, number, special char
//   - Age: 18+
//   - Country: valid name
//
// Expected Output:
//   - Valid: ✅ accepted
//   - Invalid Email: ❌ email format error
//   - Weak Password: ❌ password requirements
//   - Underage: ❌ age < 18
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500ms per validation
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// UserRegistration represents user registration data
type UserRegistration struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Age      int    `json:"age"`
	Country  string `json:"country"`
}

// loadEnv loads environment variables from a .env file
func loadEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			os.Setenv(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return scanner.Err()
}

func main() {
	// Load .env file from project root
	if err := loadEnv("../../.env"); err != nil {
		fmt.Printf("Warning: Could not load .env file: %v\n", err)
	}

	// Initialize SchemaFlux
	if err := schemaflux.InitWithEnv(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		return
	}

	fmt.Println("✅ Validate Example - User Registration Validation")
	fmt.Println("=" + string(make([]byte, 60)))

	// Test cases
	testCases := []struct {
		name string
		data UserRegistration
	}{
		{
			name: "Valid Registration",
			data: UserRegistration{
				Username: "johndoe123",
				Email:    "john.doe@example.com",
				Password: "SecureP@ssw0rd!",
				Age:      25,
				Country:  "USA",
			},
		},
		{
			name: "Invalid Email",
			data: UserRegistration{
				Username: "janedoe",
				Email:    "not-an-email",
				Password: "GoodPassword123!",
				Age:      30,
				Country:  "Canada",
			},
		},
		{
			name: "Weak Password",
			data: UserRegistration{
				Username: "bobsmith",
				Email:    "bob@example.com",
				Password: "12345",
				Age:      22,
				Country:  "UK",
			},
		},
		{
			name: "Underage User",
			data: UserRegistration{
				Username: "younguser",
				Email:    "young@example.com",
				Password: "StrongPass123!",
				Age:      15,
				Country:  "Germany",
			},
		},
	}

	// Define validation rules
	validationRules := `
Validation Rules:
1. Username: 3-20 characters, alphanumeric only
2. Email: Must be valid email format
3. Password: Minimum 8 characters, must include uppercase, lowercase, number, and special character
4. Age: Must be 18 or older
5. Country: Must be a valid country name
`

	// Create validation options with the new typed API
	opts := schemaflux.NewValidateOptions().
		WithRules(validationRules).
		WithAutoCorrect(false).
		WithIncludeExplanations(true)

	for i, tc := range testCases {
		fmt.Printf("\n%d. %s\n", i+1, tc.name)
		fmt.Println("---")
		fmt.Printf("   Username: %s\n", tc.data.Username)
		fmt.Printf("   Email: %s\n", tc.data.Email)
		fmt.Printf("   Password: %s (length: %d)\n", maskPassword(tc.data.Password), len(tc.data.Password))
		fmt.Printf("   Age: %d\n", tc.data.Age)
		fmt.Printf("   Country: %s\n", tc.data.Country)

		// Validate using the new typed API
		result, err := schemaflux.Validate[UserRegistration](tc.data, opts)
		if err != nil {
			schemaflux.GetLogger().Error("Validation error", "error", err)
			continue
		}

		if result.Valid {
			fmt.Println()
			fmt.Println("   ✅ VALID - Registration accepted")
			fmt.Printf("   ModelConfidence: %.0f%%\n", result.ModelConfidence*100)
		} else {
			fmt.Println()
			fmt.Println("   ❌ INVALID - Issues found:")

			// Display errors (critical issues)
			for _, issue := range result.Errors {
				fmt.Printf("      ❌ [%s] %s\n", issue.Field, issue.Message)
				if issue.Suggestion != "" {
					fmt.Printf("         💡 Suggestion: %s\n", issue.Suggestion)
				}
			}

			// Display warnings
			for _, issue := range result.Warnings {
				fmt.Printf("      ⚠️  [%s] %s\n", issue.Field, issue.Message)
			}

			// Display info
			for _, issue := range result.Info {
				fmt.Printf("      ℹ️  %s\n", issue.Message)
			}
		}

		if result.Summary != "" {
			fmt.Printf("\n   📝 Summary: %s\n", result.Summary)
		}
	}

	fmt.Println()
	fmt.Println("📊 Validation Summary:")
	fmt.Println("   Total tested: 4 registrations")
	fmt.Println("   Expected: 1 valid, 3 invalid")
	fmt.Println()
	fmt.Println("✨ Success! Validation complete")
}

func maskPassword(password string) string {
	if len(password) <= 2 {
		return "***"
	}
	return password[:2] + strings.Repeat("*", len(password)-2)
}
