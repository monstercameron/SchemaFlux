// Example: 07-summarize
//
// Operation: Summarize / SummarizeResult - Condense text with insights
//
// Input: Long article about AI in Healthcare (~2000 characters)
//
//	Topics: AI diagnostics, drug discovery, patient care, challenges
//
// Expected Output:
//  1. Simple Summary: 3-sentence condensed version
//  2. Summary with Metadata:
//     - Text: Condensed summary
//     - CompressionRatio: ~0.10-0.20 (10-20% of original)
//     - ModelConfidence: 0.85+ (high confidence)
//     - KeyPoints: 3-5 main takeaways
//
// Provider: Cerebras (gpt-oss-120b via Fast intelligence)
// Expected Duration: ~800-1500ms
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
)

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

	// Long article text
	article := `
Artificial Intelligence Revolution in Healthcare

The healthcare industry is experiencing a profound transformation driven by artificial 
intelligence technologies. Machine learning algorithms are now capable of analyzing 
medical images with accuracy that rivals or exceeds human radiologists. In a recent 
study published in Nature Medicine, an AI system detected breast cancer in mammograms 
with 94.5% accuracy, compared to 88% for human radiologists.

Beyond diagnostics, AI is revolutionizing drug discovery. Traditional pharmaceutical 
research takes 10-15 years and costs over $2 billion per drug. AI-powered platforms 
can now screen millions of molecular combinations in weeks, identifying promising 
candidates for further testing. Atomwise, a San Francisco-based startup, used AI to 
discover two potential Ebola treatments in just one day - a process that would have 
taken years using conventional methods.

Patient care is also being transformed through AI-powered virtual health assistants 
and predictive analytics. These systems can monitor patient vital signs in real-time, 
predict potential health emergencies before they occur, and provide personalized 
treatment recommendations based on individual genetic profiles and medical histories.

However, challenges remain. Data privacy concerns, algorithmic bias, and the need for 
regulatory frameworks are critical issues that must be addressed. The FDA has approved 
only a handful of AI-based medical devices, and questions about liability when AI 
systems make errors remain unresolved.

Despite these challenges, experts predict that AI will become an integral part of 
healthcare within the next decade, potentially saving millions of lives and reducing 
healthcare costs by up to 30%. The key will be ensuring that these technologies are 
deployed ethically and equitably, benefiting all patients regardless of their 
socioeconomic status.
`

	fmt.Println("📰 Summarize Example - Article Condensation")
	fmt.Println("=" + string(make([]byte, 60)))
	fmt.Printf("\n📄 Original Article Length: %d characters\n", len(article))
	fmt.Println("\n📥 Original Article:")
	fmt.Println("---")
	fmt.Println(article)
	fmt.Println("---")

	// Example 1: Simple string summary (original API)
	fmt.Println("\n🔹 Example 1: Simple Summary (string → string)")
	fmt.Println("-" + string(make([]byte, 40)))

	summaryOpts := schemaflux.NewSummarizeOptions()
	summaryOpts.TargetLength = 3 // 3 sentences
	summaryOpts.LengthUnit = "sentences"
	summaryOpts.OpOptions.Intelligence = schemaflux.Fast
	summaryOpts.OpOptions.Steering = "Create a concise summary capturing key points: AI in diagnostics, drug discovery, patient care, and challenges."

	summary, err := schemaflux.Summarize(article, summaryOpts)
	if err != nil {
		schemaflux.GetLogger().Error("Summarization failed", "error", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ Summary:")
	fmt.Println("---")
	fmt.Println(summary)
	fmt.Println("---")

	// Example 2: Summary with metadata (new API)
	fmt.Println("\n🔹 Example 2: Summary with Metadata (WithMetadata API)")
	fmt.Println("-" + string(make([]byte, 40)))

	metadataOpts := schemaflux.NewSummarizeOptions()
	metadataOpts.TargetLength = 3
	metadataOpts.LengthUnit = "sentences"
	metadataOpts.OpOptions.Intelligence = schemaflux.Fast

	envelope, err := schemaflux.SummarizeResult(article, metadataOpts)
	if err != nil {
		schemaflux.GetLogger().Error("SummarizeResult failed", "error", err)
		os.Exit(1)
	}
	result := envelope.Value

	fmt.Println("\n✅ Summary with Metadata:")
	fmt.Println("---")
	fmt.Println(result.Text)
	fmt.Println("---")

	fmt.Printf("\n📊 Rich Metadata:\n")
	fmt.Printf("   Compression Ratio: %.1f%% of original\n", result.CompressionRatio*100)
	fmt.Printf("   ModelConfidence:        %.0f%%\n", result.ModelConfidence*100)

	if len(result.KeyPoints) > 0 {
		fmt.Println("\n📌 Key Points Extracted:")
		for i, point := range result.KeyPoints {
			fmt.Printf("   %d. %s\n", i+1, point)
		}
	}

	// Show comparison
	fmt.Println("\n📈 Summary Statistics:")
	fmt.Printf("   Original:    %d characters\n", len(article))
	fmt.Printf("   Summary:     %d characters\n", len(result.Text))
	fmt.Printf("   Reduction:   %.1f%% smaller\n", (1-result.CompressionRatio)*100)

	fmt.Println("\n✨ Success! SummarizeWithMetadata provides rich insights beyond just text")
}
