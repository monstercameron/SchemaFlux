package main

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runHTTPExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🌐 HTTP TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// Example 1: Basic GET request
	result, err := tools.Execute(ctx, "fetch", map[string]any{
		"url":     "https://httpbin.org/get",
		"timeout": 10.0,
	})
	printResult("Fetch: GET request", result, err)

	// Example 2: GET with headers
	result, err = tools.Execute(ctx, "fetch", map[string]any{
		"url":     "https://httpbin.org/headers",
		"headers": `{"X-Custom-Header": "SchemaFlux"}`,
		"timeout": 10.0,
	})
	printResult("Fetch: GET with custom headers", result, err)

	// Example 3: POST request with JSON body
	result, err = tools.Execute(ctx, "post", map[string]any{
		"url":          "https://httpbin.org/post",
		"body":         `{"name": "SchemaFlux", "version": "1.0"}`,
		"content_type": "application/json",
		"timeout":      10.0,
	})
	printResult("Post: JSON body", result, err)

	// Example 4: URL encoding
	result, err = tools.Execute(ctx, "encode_url", map[string]any{
		"action": "encode",
		"text":   "Hello World! Special chars: &?=",
	})
	printResult("URL Encode", result, err)

	// Example 5: URL decoding
	result, err = tools.Execute(ctx, "encode_url", map[string]any{
		"action": "decode",
		"text":   "Hello%20World%21%20Special%20chars%3A%20%26%3F%3D",
	})
	printResult("URL Decode", result, err)

	// Example 6: Build URL with query parameters
	result, err = tools.Execute(ctx, "build_url", map[string]any{
		"base":   "https://api.example.com/search",
		"params": `{"q": "schemaflux", "page": "1", "limit": "10"}`,
	})
	printResult("Build URL", result, err)

	// Example 7: Webhook (to echo service)
	result, err = tools.Execute(ctx, "webhook", map[string]any{
		"url":     "https://httpbin.org/post",
		"payload": `{"event": "test", "data": {"message": "Hello from SchemaFlux"}}`,
		"method":  "POST",
	})
	printResult("Webhook", result, err)

	// Example 11: Using Fetch helper function directly
	resp, err := tools.Fetch(ctx, "https://httpbin.org/json", nil, 10*1000*1000*1000) // 10s in nanoseconds
	if err != nil {
		fmt.Printf("\n❌ Fetch helper error: %v\n", err)
	} else {
		fmt.Printf("\n✅ Fetch helper: Status %d, Body length: %d\n", resp.StatusCode, len(resp.Body))
	}
}
