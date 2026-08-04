// Example: 03-generate
//
// Operation: Generate[T] - Creates new structured data from typed specifications
//
// Input: TestUserSpec (typed specification with constraints)
//   TestUserSpec{
//       Application: "social media",
//       Diversity:   Diversity{Names: "from different cultures", Ages: "18-65", Countries: "worldwide"},
//       Roles:       ["user", "moderator", "admin"],
//       DateRange:   "within last 2 years",
//       ActiveMix:   "mix of active and inactive",
//   }
//
// Expected Output: GeneratedUsers with 5 diverse users (all fields populated)
//   GeneratedUsers{
//       Users: [
//           {ID: 1, Name: "Aisha Patel", Email: "aisha.patel@example.com", Age: 27, Country: "India", Role: "user", JoinDate: "2023-07-12", Active: true},
//           {ID: 2, Name: "Liam O'Connor", Email: "liam.oconnor@example.com", Age: 34, Country: "Ireland", Role: "moderator", JoinDate: "2024-02-05", Active: false},
//           {ID: 3, Name: "Yara Silva", Email: "yara.silva@example.com", Age: 22, Country: "Brazil", Role: "user", JoinDate: "2023-11-23", Active: true},
//           {ID: 4, Name: "Kwame Mensah", Email: "kwame.mensah@example.com", Age: 45, Country: "Ghana", Role: "admin", JoinDate: "2024-01-18", Active: false},
//           {ID: 5, Name: "Sofia Rossi", Email: "sofia.rossi@example.com", Age: 31, Country: "Italy", Role: "moderator", JoinDate: "2023-09-30", Active: true},
//       ],
//   }
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~2-3s
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// TestUserSpec defines the specification for generating test users (input type)
type TestUserSpec struct {
	Application string    `json:"application"` // What app these users are for
	Diversity   Diversity `json:"diversity"`   // Diversity requirements
	Roles       []string  `json:"roles"`       // Available roles
	DateRange   string    `json:"date_range"`  // Join date range
	ActiveMix   string    `json:"active_mix"`  // Active/inactive distribution
}

// Diversity specifies diversity requirements
type Diversity struct {
	Names     string `json:"names"`     // Name diversity requirement
	Ages      string `json:"ages"`      // Age range
	Countries string `json:"countries"` // Geographic distribution
}

// TestUser represents a generated user (output type)
type TestUser struct {
	ID       int    `json:"id"`        // Expected: Sequential ID (1, 2, 3...)
	Name     string `json:"name"`      // Expected: Diverse cultural names
	Email    string `json:"email"`     // Expected: Valid email format
	Age      int    `json:"age"`       // Expected: 18-65
	Country  string `json:"country"`   // Expected: Various countries worldwide
	Role     string `json:"role"`      // Expected: "user", "moderator", or "admin"
	JoinDate string `json:"join_date"` // Expected: Date within last 2 years
	Active   bool   `json:"active"`    // Expected: Mix of true/false
}

// GeneratedUsers wraps the array of users for generation
type GeneratedUsers struct {
	Users []TestUser `json:"users"` // Expected: 5 diverse test users
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
		os.Exit(1)
	}

	fmt.Println("🧪 Generate Example - Test Data Generator")
	fmt.Println("=" + string(make([]byte, 50)))

	// Define typed specification for test users
	spec := TestUserSpec{
		Application: "social media",
		Diversity: Diversity{
			Names:     "from different cultures worldwide",
			Ages:      "18-65 years old",
			Countries: "worldwide distribution (Asia, Europe, Americas, Africa)",
		},
		Roles:     []string{"user", "moderator", "admin"},
		DateRange: "within last 2 years (2023-2024)",
		ActiveMix: "mix of active and inactive users",
	}

	specJSON, _ := json.MarshalIndent(spec, "", "  ")
	fmt.Println("\n📝 Input Specification (Typed):")
	fmt.Println(string(specJSON))

	// Generate structured test data using typed spec and options
	result, err := schemaflux.Generate[GeneratedUsers](
		"Generate realistic test users based on the provided specification",
		schemaflux.NewGenerateOptions().
			WithIntelligence(schemaflux.Fast).
			WithSeedData(spec).
			WithConstraints(map[string]interface{}{
				"count":          5,
				"unique_emails":  true,
				"unique_names":   true,
				"age_range":      "18-65",
				"include_fields": []string{"id", "name", "email", "age", "country", "role", "join_date", "active"},
			}).
			WithSteering("Generate exactly 5 diverse, realistic test users in the 'users' array. Each user must have ALL fields populated including email and join_date. Use the specification for constraints."),
	)

	if err != nil {
		schemaflux.GetLogger().Error("Generation failed", "error", err)
		os.Exit(1)
	}

	users := result.Users

	// Display generated data
	fmt.Printf("\n✅ Generated %d Test Users:\n", len(users))
	fmt.Println("---")

	for i, user := range users {
		fmt.Printf("\n👤 User %d:\n", i+1)
		fmt.Printf("   ID:        %d\n", user.ID)
		fmt.Printf("   Name:      %s\n", user.Name)
		fmt.Printf("   Email:     %s\n", user.Email)
		fmt.Printf("   Age:       %d\n", user.Age)
		fmt.Printf("   Country:   %s\n", user.Country)
		fmt.Printf("   Role:      %s\n", user.Role)
		fmt.Printf("   Join Date: %s\n", user.JoinDate)
		fmt.Printf("   Active:    %t\n", user.Active)
	}

	// Show as JSON for API usage
	jsonData, _ := json.MarshalIndent(users, "", "  ")
	fmt.Println("\n📦 JSON Output (ready for API):")
	fmt.Println(string(jsonData))

	fmt.Printf("\n✨ Success! Generated %d realistic test users from typed spec\n", len(users))
}
