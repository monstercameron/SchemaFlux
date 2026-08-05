package tools

import (
	"context"
)

// EmailTool sends emails (stub - requires SMTP configuration)
var EmailTool = &Tool{
	Name:         "email",
	Description:  "Send emails (stub - requires SMTP server configuration)",
	Category:     CategoryBusiness,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"to":      StringParam("Recipient email address"),
		"subject": StringParam("Email subject"),
		"body":    StringParam("Email body (plain text or HTML)"),
		"html":    BoolParam("Send as HTML email"),
		"cc":      StringParam("CC recipients (comma-separated)"),
		"bcc":     StringParam("BCC recipients (comma-separated)"),
	}, []string{"to", "subject", "body"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		to, _ := params["to"].(string)
		subject, _ := params["subject"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"to":      to,
			"subject": subject,
			"message": "Email sending requires SMTP configuration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// SMSTool sends SMS messages (stub - requires SMS API)
var SMSTool = &Tool{
	Name:         "sms",
	Description:  "Send SMS messages (stub - requires Twilio or similar API)",
	Category:     CategoryBusiness,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"to":      StringParam("Recipient phone number"),
		"message": StringParam("SMS message content"),
		"from":    StringParam("Sender phone number (if applicable)"),
	}, []string{"to", "message"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		to, _ := params["to"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"to":      to,
			"message": "SMS sending requires Twilio or similar API integration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// PushTool sends push notifications (stub - requires push service)
var PushTool = &Tool{
	Name:         "push",
	Description:  "Send push notifications (stub - requires Firebase/APNs integration)",
	Category:     CategoryBusiness,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"token": StringParam("Device push token"),
		"title": StringParam("Notification title"),
		"body":  StringParam("Notification body"),
		"data":  {Type: "object", Description: "Additional data payload"},
	}, []string{"token", "title", "body"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		title, _ := params["title"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"title":   title,
			"message": "Push notifications require Firebase or APNs integration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// SlackTool sends Slack messages (stub - requires Slack webhook/API)
var SlackTool = &Tool{
	Name:         "slack",
	Description:  "Send Slack messages (stub - requires Slack webhook or API token)",
	Category:     CategoryBusiness,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"channel":    StringParam("Slack channel (e.g., '#general')"),
		"message":    StringParam("Message text"),
		"webhook":    StringParam("Webhook URL (optional if using API token)"),
		"username":   StringParam("Bot username to display"),
		"icon_emoji": StringParam("Bot emoji icon (e.g., ':robot_face:')"),
	}, []string{"channel", "message"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		channel, _ := params["channel"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"channel": channel,
			"message": "Slack messaging requires webhook or API token configuration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// DiscordTool sends Discord messages (stub - requires Discord webhook/API)
var DiscordTool = &Tool{
	Name:         "discord",
	Description:  "Send Discord messages (stub - requires Discord webhook or bot token)",
	Category:     CategoryBusiness,
	IsStub:       true,
	RequiresAuth: true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"channel":  StringParam("Discord channel ID"),
		"message":  StringParam("Message content"),
		"webhook":  StringParam("Webhook URL (optional)"),
		"username": StringParam("Bot username to display"),
		"embed":    {Type: "object", Description: "Discord embed object"},
	}, []string{"channel", "message"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		channel, _ := params["channel"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"channel": channel,
			"message": "Discord messaging requires webhook or bot token configuration",
		}, map[string]any{"stubbed": true}), nil
	},
}

// WebhookNotifyTool sends webhook notifications
var WebhookNotifyTool = &Tool{
	Name:        "webhook_notify",
	Description: "Send webhook notifications to external services (stub - not implemented)",
	Category:    CategoryBusiness,
	IsStub:      true,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"url":     StringParam("Webhook URL"),
		"payload": {Type: "object", Description: "JSON payload to send"},
		"method":  EnumParam("HTTP method", []string{"POST", "PUT", "PATCH"}),
		"headers": {Type: "object", Description: "Additional HTTP headers"},
	}, []string{"url", "payload"}),
	Execute: func(ctx context.Context, params map[string]any) (Result, error) {
		url, _ := params["url"].(string)
		return NewResultWithMeta(map[string]any{
			"stub":    true,
			"url":     url,
			"message": "Webhook notification stub - use fetch tool for actual HTTP requests",
		}, map[string]any{"stubbed": true}), nil
	},
}

func init() {
	mustRegister(EmailTool)
	mustRegister(SMSTool)
	mustRegister(PushTool)
	mustRegister(SlackTool)
	mustRegister(DiscordTool)
	mustRegister(WebhookNotifyTool)
}
