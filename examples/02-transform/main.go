package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// Resume represents structured resume data.
type Resume struct {
	Name       string   `json:"name"`
	Email      string   `json:"email"`
	Phone      string   `json:"phone"`
	Skills     []string `json:"skills"`
	Experience []struct {
		Company  string `json:"company"`
		Position string `json:"position"`
		Years    string `json:"years"`
	} `json:"experience"`
	Education string `json:"education"`
}

// MarkdownCV is the formatted CV returned by Transform.
type MarkdownCV struct {
	Content string `json:"content"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Could not transform the resume. Check your provider configuration and try again: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
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

	fmt.Fprintln(out, "📄 Transform Example - Resume to CV")
	fmt.Fprintln(out, "==================================================")
	fmt.Fprintln(out, "\n📥 Input Resume (Structured):")
	fmt.Fprintf(out, "  Name:   %s\n", resume.Name)
	fmt.Fprintf(out, "  Email:  %s\n", resume.Email)
	fmt.Fprintf(out, "  Skills: %v\n", resume.Skills)

	// Transform turns the structured resume into a typed MarkdownCV. Steering
	// guides the format; the returned value is still decoded and checked as Go data.
	cv, err := schemaflux.Transform[MarkdownCV](ctx, resume, schemaflux.TransformOptions{
		Options: schemaflux.Options{
			Tier:     schemaflux.TierFast,
			Steering: "Create a professional, well-formatted CV in markdown. Use headers, bullet points, and emphasis. Make it visually appealing.",
		},
	})
	if err != nil {
		return fmt.Errorf("transform resume: %w", err)
	}

	fmt.Fprintln(out, "\n✅ Transformed CV (Markdown):")
	fmt.Fprintln(out, "---")
	fmt.Fprintln(out, cv.Content)
	fmt.Fprintln(out, "---")
	fmt.Fprintln(out, "\n✨ Success! Structured data → Formatted document")
	return nil
}
