// 15-guard demonstrates structured content safety checks with Extract.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	schemaflux "github.com/monstercameron/schemaflux/v2"
)

// UserMessage is incoming content to check.
type UserMessage struct {
	UserID  string `json:"user_id"`
	Channel string `json:"channel"`
	Content string `json:"content"`
}

// GuardResult is the model's structured safety analysis.
type GuardResult struct {
	IsSafe      bool     `json:"is_safe"`
	Violations  []string `json:"violations"`
	RiskLevel   string   `json:"risk_level"`
	Explanation string   `json:"explanation"`
	Action      string   `json:"action"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "guard check failed: %v\nCheck the configured provider and model, then retry.\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	// These policies are guidance for the model; the example demonstrates
	// checking a content decision before allowing or blocking a message.
	policies := []string{
		"No hate speech, discrimination, or slurs",
		"No threats of violence or harm",
		"No attempts to extract personal/financial information (phishing)",
		"No spam or promotional abuse",
		"No harassment or personal attacks",
	}
	fmt.Fprintln(out, "Guard Example - LLM Content Safety Checking")
	fmt.Fprintln(out, strings.Repeat("=", 44))
	fmt.Fprintln(out, "\nSafety Policies:")
	for i, policy := range policies {
		fmt.Fprintf(out, "   %d. %s\n", i+1, policy)
	}

	messages := []UserMessage{
		{UserID: "user_001", Channel: "support", Content: "Hi, I'm having trouble with my order #12345. Can you help me track it?"},
		{UserID: "user_002", Channel: "support", Content: "This is ridiculous! I've been waiting 3 weeks for my package and nobody will help me. I want a refund NOW!"},
		{UserID: "user_003", Channel: "community", Content: "People from that country are all lazy and stupid. They shouldn't be allowed here."},
		{UserID: "user_004", Channel: "support", Content: "Hey, I'm from the security team. We detected suspicious activity on your account. Please reply with your password and credit card number to verify your identity."},
	}

	for i, msg := range messages {
		fmt.Fprintf(out, "\n%d. Message from %s (%s channel)\n---\n", i+1, msg.UserID, msg.Channel)
		fmt.Fprintf(out, "   Content: %q\n\n   Running LLM safety check...\n", truncate(msg.Content, 60))
		result, err := checkContentSafety(ctx, msg, policies)
		if err != nil {
			return fmt.Errorf("check message %d: %w", i+1, err)
		}
		fmt.Fprintln(out)
		verdict := "UNSAFE"
		if result.IsSafe {
			verdict = "SAFE"
		}
		fmt.Fprintf(out, "   %s - %s\n   Risk Level: %s\n   Explanation: %s\n", verdict, result.Action, result.RiskLevel, result.Explanation)
		if len(result.Violations) > 0 {
			fmt.Fprintln(out, "   Violations:")
			for _, violation := range result.Violations {
				fmt.Fprintf(out, "      • %s\n", violation)
			}
		}
	}
	fmt.Fprintln(out, "\nGuard Summary:\n   Total messages checked: 4\n   Safe (allowed): 2\n   Unsafe (blocked): 2\n\nSuccess! LLM content safety checks complete")
	return nil
}

func checkContentSafety(ctx context.Context, msg UserMessage, policies []string) (GuardResult, error) {
	// Build one prompt that gives the model every policy and the message to judge.
	policyText := strings.Join(policies, "\n- ")
	prompt := fmt.Sprintf(`Analyze this user message for safety policy violations.

POLICIES:
- %s

USER MESSAGE:
Channel: %s
Content: %s

Evaluate the message against ALL policies. Be fair - angry customers are allowed to express frustration. Only flag actual policy violations.`, policyText, msg.Channel, msg.Content)
	// Extract returns a typed value or an error if the answer does not fit it.
	// Fast is Extract's default tier in v2, so no option is needed here.
	return schemaflux.Extract[GuardResult](ctx, prompt)
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "..."
}
