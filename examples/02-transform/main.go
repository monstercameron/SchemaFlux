// Example: 02-transform
//
// Operation: Transform[T, U] - Converts data from one type to another using LLM intelligence
//
// Input: Structured Resume data
//
//	Resume{
//	    Name:  "Jane Developer",
//	    Email: "jane.dev@email.com",
//	    Phone: "+1-555-0123",
//	    Skills: ["Go", "Python", "JavaScript", "Docker", "Kubernetes", "AWS"],
//	    Experience: [{Company: "Tech Corp", Position: "Senior Engineer", Years: "2020-2024"}, ...],
//	    Education: "BS Computer Science, MIT, 2018",
//	}
//
// Expected Output: MarkdownCV with professionally formatted content
//
//	MarkdownCV{
//	    Content: "# Jane Developer\n\n**Contact:** jane.dev@email.com | +1-555-0123\n\n## Skills\n- Go, Python, JavaScript...\n\n## Experience\n### Senior Engineer at Tech Corp (2020-2024)\n..."
//	}
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~500-1000ms
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

// Resume represents structured resume data (input type)
type Resume struct {
	Name       string   `json:"name"`   // Expected: "Jane Developer"
	Email      string   `json:"email"`  // Expected: "jane.dev@email.com"
	Phone      string   `json:"phone"`  // Expected: "+1-555-0123"
	Skills     []string `json:"skills"` // Expected: ["Go", "Python", "JavaScript", ...]
	Experience []struct {
		Company  string `json:"company"`  // Expected: "Tech Corp"
		Position string `json:"position"` // Expected: "Senior Engineer"
		Years    string `json:"years"`    // Expected: "2020-2024"
	} `json:"experience"`
	Education string `json:"education"` // Expected: "BS Computer Science, MIT, 2018"
}

// MarkdownCV represents a formatted CV in markdown (output type)
type MarkdownCV struct {
	Content string `json:"content"` // Expected: Professionally formatted markdown CV
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

	// Input: Structured resume data
	resume := Resume{
		Name:  "Jane Developer",
		Email: "jane.dev@email.com",
		Phone: "+1-555-0123",
		Skills: []string{
			"Go", "Python", "JavaScript",
			"Docker", "Kubernetes", "AWS",
		},
		Experience: []struct {
			Company  string `json:"company"`
			Position string `json:"position"`
			Years    string `json:"years"`
		}{
			{Company: "Tech Corp", Position: "Senior Engineer", Years: "2020-2024"},
			{Company: "StartupXYZ", Position: "Full Stack Developer", Years: "2018-2020"},
		},
		Education: "BS Computer Science, MIT, 2018",
	}

	fmt.Println("📄 Transform Example - Resume to CV")
	fmt.Println("=" + string(make([]byte, 50)))
	fmt.Println("\n📥 Input Resume (Structured):")
	fmt.Printf("  Name:   %s\n", resume.Name)
	fmt.Printf("  Email:  %s\n", resume.Email)
	fmt.Printf("  Skills: %v\n", resume.Skills)

	// Transform: Resume → Professional Markdown CV
	cv, err := schemaflux.Transform[Resume, MarkdownCV](
		resume,
		schemaflux.NewTransformOptions().
			WithIntelligence(schemaflux.Fast).
			WithSteering("Create a professional, well-formatted CV in markdown. Use headers, bullet points, and emphasis. Make it visually appealing."),
	)

	if err != nil {
		schemaflux.GetLogger().Error("Transformation failed", "error", err)
		os.Exit(1)
	}

	// Display transformed output
	fmt.Println("\n✅ Transformed CV (Markdown):")
	fmt.Println("---")
	fmt.Println(cv.Content)
	fmt.Println("---")

	fmt.Println("\n✨ Success! Structured data → Formatted document")
}
