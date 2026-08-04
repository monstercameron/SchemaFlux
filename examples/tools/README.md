# SchemaFlux Tool Primitives Examples

This directory contains comprehensive examples for all 86+ tool primitives available in SchemaFlux.

## Quick Start

```bash
# List all tools and categories
go run ./examples/tools/...

# Run examples for a specific category
go run ./examples/tools/... compute
go run ./examples/tools/... data
go run ./examples/tools/... file
go run ./examples/tools/... http
go run ./examples/tools/... time
go run ./examples/tools/... finance
go run ./examples/tools/... template
go run ./examples/tools/... cache
go run ./examples/tools/... database
go run ./examples/tools/... ai

# Run all examples
go run ./examples/tools/... all
```

## Tool Categories

### 📐 Computation (3 tools)
- `calculate` - Evaluate mathematical expressions
- `convert` - Unit conversion (length, weight, temperature, time, data)
- `regex` - Pattern matching and text extraction

### 📊 Data (5 tools)
- `csv` - Parse and generate CSV data
- `json` - Parse, format, extract, and validate JSON
- `xml` - Parse and generate XML data
- `table` - Format data as text, markdown, or HTML tables
- `diff` - Compare data structures and show differences

### 📁 File (10 tools)
- `read_file` - Read file contents
- `write_file` - Write content to files
- `list_dir` - List directory contents
- `copy_file` - Copy files or directories
- `move_file` - Move/rename files
- `delete_file` - Delete files or directories
- `file_exists` - Check if file exists
- `file_info` - Get detailed file information
- `watch_file` - Watch for file changes [stub]
- `search_files` - Search for files by pattern

### 🌐 HTTP (8 tools)
- `fetch` - HTTP GET requests
- `post` - HTTP POST requests
- `web_search` - Web search [stub - requires API]
- `scrape` - Web scraping [stub - requires browser]
- `browser` - Browser automation [stub]
- `webhook` - Trigger webhooks
- `encode_url` - URL encoding/decoding
- `build_url` - Build URLs with query parameters

### ⏰ Time (7 tools)
- `now` - Get current date/time
- `parse_time` - Parse time strings
- `duration` - Calculate time differences
- `schedule` - Natural language scheduling
- `holiday` - Check for holidays
- `geo` - Geocoding [stub - requires API]
- `weather` - Weather data [stub - requires API]

### 💰 Finance (5 tools)
- `tax` - Calculate taxes (sales, VAT, income, tip)
- `interest` - Calculate interest (simple, compound, loan, mortgage, savings)
- `currency` - Currency conversion [stub - requires API]
- `stock` - Stock information [stub - requires API]
- `chart` - Generate chart configurations [stub]

### 📝 Template (3 tools)
- `template` - Go template rendering
- `string_template` - Simple {{key}} interpolation
- `markdown` - Markdown to HTML/text conversion

### 🔐 Cache & Security (7 tools)
- `cache` - In-memory caching
- `memoize` - Cache expensive operations
- `hash` - Compute cryptographic hashes (MD5, SHA1, SHA256, SHA512)
- `base64` - Base64 encoding/decoding
- `token` - Generate tokens (random, JWT [stub])
- `encrypt` - AES encryption [stub - requires key management]
- `decrypt` - AES decryption [stub]

### 🗄️ Database (5 tools)
- `sqlite` - SQLite operations (query, execute, tables, schema)
- `migrate` - Database migrations
- `seed` - Populate database with data
- `backup` - Database backup/restore
- `vector_db` - Vector database operations [stub]

### 📦 Archive (5 tools)
- `zip` - Create and extract ZIP archives
- `tar` - TAR archives [stub]
- `pdf` - PDF operations [stub]
- `qrcode` - Generate QR codes [stub]
- `barcode` - Generate barcodes [stub]

### 🖼️ Image (8 tools)
- `vision` - AI vision analysis [stub - requires API]
- `ocr` - Text extraction from images [stub]
- `image_info` - Get image metadata
- `image_base64` - Encode/decode images as base64
- `image_resize` - Resize images [stub]
- `image_crop` - Crop images [stub]
- `image_convert` - Convert image formats [stub]
- `thumbnail` - Generate thumbnails [stub]

### 🔊 Audio (6 tools)
- `tts` - Text-to-speech [stub - requires API]
- `stt` - Speech-to-text [stub - requires API]
- `audio_info` - Audio metadata [stub]
- `audio_convert` - Audio format conversion [stub]
- `audio_trim` - Trim audio files [stub]
- `audio_analyze` - Audio analysis [stub]

### 💬 Messaging (6 tools)
- `email` - Send emails [stub - requires SMTP]
- `sms` - Send SMS [stub - requires Twilio]
- `push` - Push notifications [stub]
- `slack` - Slack messages [stub]
- `discord` - Discord messages [stub]
- `webhook_notify` - Webhook notifications [stub]

### 🤖 AI (6 tools)
- `embed` - Generate embeddings [stub - requires API]
- `similarity` - Semantic similarity [stub]
- `semantic_search` - Semantic search [stub]
- `classify` - Text classification [stub]
- `sentiment` - Sentiment analysis [stub]
- `translate` - Translation [stub]

### ⚡ Execution (2 tools)
- `shell` - Execute shell commands
- `run_code` - Execute code snippets [stub - security]

## Example Files

| File | Description |
|------|-------------|
| `main.go` | Entry point and tool listing |
| `compute_examples.go` | Calculate, convert, regex examples |
| `data_examples.go` | CSV, JSON, XML, table, diff examples |
| `file_examples.go` | File and archive operations |
| `http_examples.go` | HTTP requests, URL building |
| `time_examples.go` | Date/time manipulation |
| `finance_examples.go` | Tax, interest calculations |
| `template_examples.go` | Template rendering, markdown |
| `cache_security_examples.go` | Caching, hashing, tokens |
| `database_examples.go` | SQLite, migrations, seeding |
| `ai_examples.go` | AI, audio, messaging, image, execution tools |

## Using Tools in Your Code

```go
package main

import (
    "context"
    "fmt"
    "github.com/monstercameron/schemaflux/internal/tools"
)

func main() {
    ctx := context.Background()

    // Execute a tool by name
    result, err := tools.Execute(ctx, "calculate", map[string]any{
        "expression": "2 + 2 * 3",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Data) // Output: 8

    // Use helper functions directly
    val, _ := tools.Calculate("sqrt(16) + pow(2, 3)")
    fmt.Println(val) // Output: 12

    hash, _ := tools.Hash("password", "sha256")
    fmt.Println(hash)

    // Get OpenAI-compatible tool specs
    specs := tools.GetOpenAITools()
    fmt.Printf("Tools available for LLM: %d\n", len(specs))
}
```

## Creating a Custom Registry

```go
// Create a registry with only specific tools
registry := tools.CreateSubRegistry("calculate", "json", "fetch")

// Or create from categories
registry := tools.CreateCategoryRegistry("computation", "data")

// Execute from custom registry
result, _ := registry.Execute(ctx, "calculate", map[string]any{
    "expression": "2 + 2",
})
```

## Stub Implementations

Tools marked as [stub] require external API keys or services. They return informative messages about what's needed:

```go
result, _ := tools.Execute(ctx, "weather", map[string]any{
    "location": "New York",
})
// Returns: "Weather for 'New York' requires OpenWeatherMap API. Configure WEATHER_API_KEY."
```

To implement these stubs, you can:
1. Fork the repository
2. Add your API integration in the corresponding tool file
3. Set the `IsStub` flag to `false`

