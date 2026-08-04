package main

import (
	"context"
	"fmt"
	"runtime"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runAIExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🤖 AI & EXECUTION TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// === AI TOOLS (all stubs - require external API integration) ===

	// Example 1: Embed - Generate text embeddings
	result, err := tools.Execute(ctx, "embed", map[string]any{
		"text":  "SchemaFlux is a type-safe LLM operations library for Go",
		"model": "text-embedding-3-small",
	})
	printResult("Embed: Generate text embedding (stub)", result, err)

	// Example 2: Similarity - Calculate semantic similarity
	result, err = tools.Execute(ctx, "similarity", map[string]any{
		"text1": "The cat sat on the mat",
		"text2": "A feline rested on the rug",
	})
	printResult("Similarity: Compare two texts (stub)", result, err)

	// Example 3: Semantic Search - Search by meaning
	result, err = tools.Execute(ctx, "semantic_search", map[string]any{
		"query": "How do I create a new project?",
		"documents": []any{
			"To start a new project, run 'go mod init'",
			"The weather is nice today",
			"Initialize your workspace with the setup command",
			"Go is a statically typed language",
		},
		"top_k": 2.0,
	})
	printResult("Semantic Search: Find relevant docs (stub)", result, err)

	// Example 4: Classify - Categorize text
	result, err = tools.Execute(ctx, "classify", map[string]any{
		"text": "I absolutely love this product! It exceeded all my expectations.",
		"categories": []any{
			"positive_review",
			"negative_review",
			"neutral_review",
			"question",
			"complaint",
		},
	})
	printResult("Classify: Categorize review text (stub)", result, err)

	// Example 5: Sentiment - Analyze emotional tone
	result, err = tools.Execute(ctx, "sentiment", map[string]any{
		"text": "This is the worst experience I've ever had. Totally disappointed.",
	})
	printResult("Sentiment: Analyze negative text (stub)", result, err)

	result, err = tools.Execute(ctx, "sentiment", map[string]any{
		"text": "Amazing! Best purchase I've made this year. Highly recommend!",
	})
	printResult("Sentiment: Analyze positive text (stub)", result, err)

	// Example 6: Translate - Translate between languages
	result, err = tools.Execute(ctx, "translate", map[string]any{
		"text": "Hello, how are you today?",
		"from": "en",
		"to":   "es",
	})
	printResult("Translate: English to Spanish (stub)", result, err)

	result, err = tools.Execute(ctx, "translate", map[string]any{
		"text": "Bonjour le monde",
		"from": "fr",
		"to":   "en",
	})
	printResult("Translate: French to English (stub)", result, err)

	// === EXECUTION TOOLS ===

	// Example 7: Shell - Execute shell commands
	fmt.Println("\n" + strings.Repeat("-", 40))
	fmt.Println("⚡ Execution Tools (working implementations)")
	fmt.Println(strings.Repeat("-", 40))

	// Platform-appropriate echo command
	if runtime.GOOS == "windows" {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "echo Hello from SchemaFlux!",
			"timeout": 5.0,
		})
	} else {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "echo 'Hello from SchemaFlux!'",
			"timeout": 5.0,
		})
	}
	printResult("Shell: Echo command", result, err)

	// Example 8: Shell - List current directory
	if runtime.GOOS == "windows" {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "dir /b",
			"timeout": 5.0,
		})
	} else {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "ls -la",
			"timeout": 5.0,
		})
	}
	printResult("Shell: List directory", result, err)

	// Example 9: Shell - Get current date
	if runtime.GOOS == "windows" {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "date /t",
			"timeout": 5.0,
		})
	} else {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "date",
			"timeout": 5.0,
		})
	}
	printResult("Shell: Get current date", result, err)

	// Example 10: Shell - Environment variable
	if runtime.GOOS == "windows" {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "echo %USERNAME%",
			"timeout": 5.0,
		})
	} else {
		result, err = tools.Execute(ctx, "shell", map[string]any{
			"command": "echo $USER",
			"timeout": 5.0,
		})
	}
	printResult("Shell: Get username", result, err)

	// Example 11: Run Code - Execute code snippets (stub)
	result, err = tools.Execute(ctx, "run_code", map[string]any{
		"language": "python",
		"code":     "print('Hello from Python!')\nresult = 2 + 2\nprint(f'2 + 2 = {result}')",
		"timeout":  10.0,
	})
	printResult("Run Code: Python snippet (stub)", result, err)

	result, err = tools.Execute(ctx, "run_code", map[string]any{
		"language": "javascript",
		"code":     "console.log('Hello from JavaScript!');\nconst sum = [1,2,3].reduce((a,b) => a+b, 0);\nconsole.log(`Sum: ${sum}`);",
		"timeout":  10.0,
	})
	printResult("Run Code: JavaScript snippet (stub)", result, err)

	result, err = tools.Execute(ctx, "run_code", map[string]any{
		"language": "go",
		"code":     "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from Go!\")\n}",
		"timeout":  10.0,
	})
	printResult("Run Code: Go snippet (stub)", result, err)

	// === AUDIO TOOLS (all stubs) ===

	fmt.Println("\n" + strings.Repeat("-", 40))
	fmt.Println("🔊 Audio Tools (stubs - require external services)")
	fmt.Println(strings.Repeat("-", 40))

	// Example 12: Text-to-Speech
	result, err = tools.Execute(ctx, "tts", map[string]any{
		"text":   "Welcome to SchemaFlux, a type-safe LLM operations library.",
		"voice":  "alloy",
		"output": "welcome.mp3",
		"format": "mp3",
		"speed":  1.0,
	})
	printResult("TTS: Convert text to speech (stub)", result, err)

	// Example 13: Speech-to-Text
	result, err = tools.Execute(ctx, "stt", map[string]any{
		"input":    "recording.wav",
		"language": "en",
		"format":   "text",
	})
	printResult("STT: Transcribe audio (stub)", result, err)

	// Example 14: Audio Info
	result, err = tools.Execute(ctx, "audio_info", map[string]any{
		"path": "song.mp3",
	})
	printResult("Audio Info: Get metadata (stub)", result, err)

	// Example 15: Audio Convert
	result, err = tools.Execute(ctx, "audio_convert", map[string]any{
		"input":   "recording.wav",
		"output":  "recording.mp3",
		"format":  "mp3",
		"bitrate": "192k",
	})
	printResult("Audio Convert: WAV to MP3 (stub)", result, err)

	// Example 16: Audio Trim
	result, err = tools.Execute(ctx, "audio_trim", map[string]any{
		"input":  "podcast.mp3",
		"output": "clip.mp3",
		"start":  "00:01:30",
		"end":    "00:02:00",
	})
	printResult("Audio Trim: Extract clip (stub)", result, err)

	// Example 17: Audio Analyze
	result, err = tools.Execute(ctx, "audio_analyze", map[string]any{
		"path":    "music.mp3",
		"analyze": "all",
	})
	printResult("Audio Analyze: Full analysis (stub)", result, err)

	// === MESSAGING TOOLS (all stubs) ===

	fmt.Println("\n" + strings.Repeat("-", 40))
	fmt.Println("💬 Messaging Tools (stubs - require external services)")
	fmt.Println(strings.Repeat("-", 40))

	// Example 18: Email
	result, err = tools.Execute(ctx, "email", map[string]any{
		"to":      "user@example.com",
		"subject": "Welcome to SchemaFlux",
		"body":    "Thank you for using SchemaFlux!",
		"html":    false,
	})
	printResult("Email: Send email (stub)", result, err)

	// Example 19: SMS
	result, err = tools.Execute(ctx, "sms", map[string]any{
		"to":      "+1234567890",
		"message": "Your verification code is 123456",
	})
	printResult("SMS: Send text message (stub)", result, err)

	// Example 20: Push Notification
	result, err = tools.Execute(ctx, "push", map[string]any{
		"token": "device-token-here",
		"title": "New Message",
		"body":  "You have a new notification",
	})
	printResult("Push: Send notification (stub)", result, err)

	// Example 21: Slack
	result, err = tools.Execute(ctx, "slack", map[string]any{
		"channel":    "#general",
		"message":    "Build completed successfully! :rocket:",
		"username":   "SchemaFlux Bot",
		"icon_emoji": ":robot_face:",
	})
	printResult("Slack: Send message (stub)", result, err)

	// Example 22: Discord
	result, err = tools.Execute(ctx, "discord", map[string]any{
		"channel":  "123456789",
		"message":  "Server status: Online",
		"username": "Status Bot",
	})
	printResult("Discord: Send message (stub)", result, err)

	// Example 23: Webhook Notify
	result, err = tools.Execute(ctx, "webhook_notify", map[string]any{
		"url":     "https://hooks.example.com/webhook",
		"payload": map[string]any{"event": "build_complete", "status": "success"},
		"method":  "POST",
	})
	printResult("Webhook Notify: Trigger webhook (stub)", result, err)

	// === IMAGE TOOLS ===

	fmt.Println("\n" + strings.Repeat("-", 40))
	fmt.Println("🖼️ Image Tools")
	fmt.Println(strings.Repeat("-", 40))

	// Example 24: Vision - AI image analysis
	result, err = tools.Execute(ctx, "vision", map[string]any{
		"image":  "https://example.com/image.jpg",
		"prompt": "What objects are in this image?",
		"detail": "high",
	})
	printResult("Vision: Analyze image (stub)", result, err)

	// Example 25: OCR - Extract text from images
	result, err = tools.Execute(ctx, "ocr", map[string]any{
		"image":    "document.png",
		"language": "eng",
	})
	printResult("OCR: Extract text (stub)", result, err)

	// Example 26: Image Resize
	result, err = tools.Execute(ctx, "image_resize", map[string]any{
		"input":       "photo.jpg",
		"output":      "photo_small.jpg",
		"width":       800.0,
		"height":      600.0,
		"keep_aspect": true,
	})
	printResult("Image Resize: Resize image (stub)", result, err)

	// Example 27: Image Crop
	result, err = tools.Execute(ctx, "image_crop", map[string]any{
		"input":  "photo.jpg",
		"output": "cropped.jpg",
		"x":      100.0,
		"y":      100.0,
		"width":  400.0,
		"height": 300.0,
	})
	printResult("Image Crop: Crop image (stub)", result, err)

	// Example 28: Image Convert
	result, err = tools.Execute(ctx, "image_convert", map[string]any{
		"input":   "photo.png",
		"output":  "photo.jpg",
		"format":  "jpeg",
		"quality": 85.0,
	})
	printResult("Image Convert: PNG to JPEG (stub)", result, err)

	// Example 29: Thumbnail
	result, err = tools.Execute(ctx, "thumbnail", map[string]any{
		"input":  "photo.jpg",
		"output": "thumb.jpg",
		"size":   150.0,
	})
	printResult("Thumbnail: Generate thumbnail (stub)", result, err)
}
