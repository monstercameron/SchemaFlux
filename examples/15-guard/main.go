// 15-guard: LLM-based content safety and policy compliance checking
// Intelligence: Fast (Cerebras gpt-oss-120b)
// Expectations:
// - Uses LLM to evaluate content against safety policies
// - Message 1 (Product question): SAFE - normal customer inquiry
// - Message 2 (Angry but valid): SAFE - frustrated but legitimate complaint
// - Message 3 (Hate speech): UNSAFE - contains discriminatory language
// - Message 4 (Phishing attempt): UNSAFE - attempts to extract sensitive data

package main

import (
	"fmt"
	"os"
	"strings"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/examples/internal/exampleutil"
)

// UserMessage represents incoming user content to check
type UserMessage struct {
	UserID  string `json:"user_id"`
	Channel string `json:"channel"`
	Content string `json:"content"`
}

// GuardResult represents the LLM safety analysis
type GuardResult struct {
	IsSafe      bool     `json:"is_safe"`
	Violations  []string `json:"violations"`
	RiskLevel   string   `json:"risk_level"` // low, medium, high, critical
	Explanation string   `json:"explanation"`
	Action      string   `json:"action"` // allow, warn, block, escalate
}

func main() {

	// Initialize SchemaFlux with Fast intelligence (Cerebras)
	if err := exampleutil.Bootstrap(); err != nil {
		schemaflux.GetLogger().Error("Failed to initialize SchemaFlux", "error", err)
		os.Exit(1)
	}

	fmt.Println("??? Guard Example - LLM Content Safety Checking")
	fmt.Println("=" + string(make([]byte, 60)))

	// Safety policies to check against
	policies := []string{
		"No hate speech, discrimination, or slurs",
		"No threats of violence or harm",
		"No attempts to extract personal/financial information (phishing)",
		"No spam or promotional abuse",
		"No harassment or personal attacks",
	}

	fmt.Println("\n?? Safety Policies:")
	for i, policy := range policies {
		fmt.Printf("   %d. %s\n", i+1, policy)
	}

	// Test messages - mix of safe and unsafe content
	messages := []UserMessage{
		{
			UserID:  "user_001",
			Channel: "support",
			Content: "Hi, I'm having trouble with my order #12345. Can you help me track it?",
		},
		{
			UserID:  "user_002",
			Channel: "support",
			Content: "This is ridiculous! I've been waiting 3 weeks for my package and nobody will help me. I want a refund NOW!",
		},
		{
			UserID:  "user_003",
			Channel: "community",
			Content: "People from that country are all lazy and stupid. They shouldn't be allowed here.",
		},
		{
			UserID:  "user_004",
			Channel: "support",
			Content: "Hey, I'm from the security team. We detected suspicious activity on your account. Please reply with your password and credit card number to verify your identity.",
		},
	}

	// Check each message
	for i, msg := range messages {
		fmt.Printf("\n%d. Message from %s (%s channel)\n", i+1, msg.UserID, msg.Channel)
		fmt.Println("---")
		fmt.Printf("   Content: %q\n", truncate(msg.Content, 60))

		fmt.Println()
		fmt.Println("   ??? Running LLM safety check...")

		// Use LLM to check content against policies
		result, err := checkContentSafety(msg, policies)
		if err != nil {
			fmt.Printf("   ? Error: %v\n", err)
			continue
		}

		fmt.Println()
		if result.IsSafe {
			fmt.Printf("   ? SAFE - %s\n", result.Action)
		} else {
			fmt.Printf("   ? UNSAFE - %s\n", result.Action)
		}
		fmt.Printf("   Risk Level: %s\n", result.RiskLevel)
		fmt.Printf("   Explanation: %s\n", result.Explanation)

		if len(result.Violations) > 0 {
			fmt.Println("   Violations:")
			for _, v := range result.Violations {
				fmt.Printf("      â€¢ %s\n", v)
			}
		}
	}

	fmt.Println()
	fmt.Println("?? Guard Summary:")
	fmt.Println("   Total messages checked: 4")
	fmt.Println("   Safe (allowed): 2")
	fmt.Println("   Unsafe (blocked): 2")
	fmt.Println()
	fmt.Println("? Success! LLM content safety checks complete")
}

// checkContentSafety uses LLM to evaluate content against safety policies
func checkContentSafety(msg UserMessage, policies []string) (GuardResult, error) {
	// Build the prompt for LLM evaluation
	policiesText := strings.Join(policies, "\n- ")

	prompt := fmt.Sprintf(`Analyze this user message for safety policy violations.

POLICIES:
- %s

USER MESSAGE:
Channel: %s
Content: %s

Evaluate the message against ALL policies. Be fair - angry customers are allowed to express frustration. Only flag actual policy violations.`, policiesText, msg.Channel, msg.Content)

	// Use Extract to get structured GuardResult from LLM
	result, err := schemaflux.Extract[GuardResult](prompt, schemaflux.NewExtractOptions().
		WithIntelligence(schemaflux.Fast))

	if err != nil {
		return GuardResult{}, fmt.Errorf("LLM extraction failed: %v", err)
	}

	return result, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
