package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// TestUserSpec defines the requirements used to generate test users.
type TestUserSpec struct {
	Application string    `json:"application"`
	Diversity   Diversity `json:"diversity"`
	Roles       []string  `json:"roles"`
	DateRange   string    `json:"date_range"`
	ActiveMix   string    `json:"active_mix"`
}

// Diversity specifies the requested range of names, ages, and countries.
type Diversity struct {
	Names     string `json:"names"`
	Ages      string `json:"ages"`
	Countries string `json:"countries"`
}

// TestUser is one generated user record.
type TestUser struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Age      int    `json:"age"`
	Country  string `json:"country"`
	Role     string `json:"role"`
	JoinDate string `json:"join_date"`
	Active   bool   `json:"active"`
}

// GeneratedUsers wraps the generated records.
type GeneratedUsers struct {
	Users []TestUser `json:"users"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "generation failed: %v\nCheck your provider configuration and try again.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "🧪 Generate Example - Test Data Generator")
	fmt.Fprintln(out, "==================================================")

	// Keep the typed specification separate so the model can use it as seed data.
	spec := TestUserSpec{
		Application: "social media",
		Diversity: Diversity{
			Names: "from different cultures worldwide", Ages: "18-65 years old",
			Countries: "worldwide distribution (Asia, Europe, Americas, Africa)",
		},
		Roles: []string{"user", "moderator", "admin"}, DateRange: "within last 2 years",
		ActiveMix: "mix of active and inactive users",
	}
	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode input specification: %w", err)
	}
	fmt.Fprintln(out, "\n📝 Input Specification (Typed):")
	fmt.Fprintln(out, string(specJSON))

	result, err := schemaflux.Generate[GeneratedUsers](ctx,
		"Generate realistic test users based on the provided specification",
		schemaflux.GenerateOptions{
			SeedData: spec,
			Constraints: map[string]any{
				"count": 5, "unique_emails": true, "unique_names": true,
				"age_range":      "18-65",
				"include_fields": []string{"id", "name", "email", "age", "country", "role", "join_date", "active"},
			},
			Options: schemaflux.Options{
				Tier: schemaflux.TierFast,
				Steering: "Generate exactly 5 diverse, realistic test users in the 'users' array. Each user must have ALL fields populated including email and join_date. Use the specification for constraints.",
			},
		},
	)
	if err != nil {
		return fmt.Errorf("generate users: %w", err)
	}
	users := result.Users
	fmt.Fprintf(out, "\n✅ Generated %d Test Users:\n", len(users))
	fmt.Fprintln(out, "---")
	for i, user := range users {
		fmt.Fprintf(out, "\n👤 User %d:\n", i+1)
		fmt.Fprintf(out, "   ID:        %d\n", user.ID)
		fmt.Fprintf(out, "   Name:      %s\n", user.Name)
		fmt.Fprintf(out, "   Email:     %s\n", user.Email)
		fmt.Fprintf(out, "   Age:       %d\n", user.Age)
		fmt.Fprintf(out, "   Country:   %s\n", user.Country)
		fmt.Fprintf(out, "   Role:      %s\n", user.Role)
		fmt.Fprintf(out, "   Join Date: %s\n", user.JoinDate)
		fmt.Fprintf(out, "   Active:    %t\n", user.Active)
	}
	jsonData, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return fmt.Errorf("encode generated users: %w", err)
	}
	fmt.Fprintln(out, "\n📦 JSON Output (ready for API):")
	fmt.Fprintln(out, string(jsonData))
	fmt.Fprintf(out, "\n✨ Success! Generated %d realistic test users from typed spec\n", len(users))
	return nil
}
