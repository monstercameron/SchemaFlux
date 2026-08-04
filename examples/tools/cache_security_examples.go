package main

import (
	"context"
	"fmt"

	"github.com/monstercameron/schemaflux/internal/tools"
)

func runCacheSecurityExamples(ctx context.Context) {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🔐 CACHE & SECURITY TOOLS EXAMPLES")
	fmt.Println(strings.Repeat("=", 60))

	// === CACHE EXAMPLES ===

	// Example 1: Set cache value
	result, err := tools.Execute(ctx, "cache", map[string]any{
		"action": "set",
		"key":    "user:123",
		"value":  `{"name": "Alice", "role": "admin"}`,
		"ttl":    3600.0, // 1 hour
	})
	printResult("Cache: Set value", result, err)

	// Example 2: Get cache value
	result, err = tools.Execute(ctx, "cache", map[string]any{
		"action": "get",
		"key":    "user:123",
	})
	printResult("Cache: Get value", result, err)

	// Example 3: Check if key exists
	result, err = tools.Execute(ctx, "cache", map[string]any{
		"action": "has",
		"key":    "user:123",
	})
	printResult("Cache: Has key (exists)", result, err)

	result, err = tools.Execute(ctx, "cache", map[string]any{
		"action": "has",
		"key":    "nonexistent",
	})
	printResult("Cache: Has key (not exists)", result, err)

	// Example 4: Set multiple values
	tools.Execute(ctx, "cache", map[string]any{
		"action": "set",
		"key":    "setting:theme",
		"value":  "dark",
	})
	tools.Execute(ctx, "cache", map[string]any{
		"action": "set",
		"key":    "setting:language",
		"value":  "en",
	})

	// Example 5: List all keys
	result, err = tools.Execute(ctx, "cache", map[string]any{
		"action": "keys",
	})
	printResult("Cache: List all keys", result, err)

	// Example 6: Delete a key
	result, err = tools.Execute(ctx, "cache", map[string]any{
		"action": "delete",
		"key":    "setting:theme",
	})
	printResult("Cache: Delete key", result, err)

	// Example 7: Memoize a value
	result, err = tools.Execute(ctx, "memoize", map[string]any{
		"key":   "expensive_calculation",
		"value": "computed result",
		"ttl":   60.0,
	})
	printResult("Memoize: First call (not cached)", result, err)

	result, err = tools.Execute(ctx, "memoize", map[string]any{
		"key":   "expensive_calculation",
		"value": "this won't be used",
	})
	printResult("Memoize: Second call (cached)", result, err)

	// Example 8: Clear cache
	result, err = tools.Execute(ctx, "cache", map[string]any{
		"action": "clear",
	})
	printResult("Cache: Clear all", result, err)

	// === SECURITY EXAMPLES ===

	// Example 9: Hash with SHA256
	result, err = tools.Execute(ctx, "hash", map[string]any{
		"data":      "password123",
		"algorithm": "sha256",
		"encoding":  "hex",
	})
	printResult("Hash: SHA256 (hex)", result, err)

	// Example 10: Hash with MD5
	result, err = tools.Execute(ctx, "hash", map[string]any{
		"data":      "password123",
		"algorithm": "md5",
		"encoding":  "hex",
	})
	printResult("Hash: MD5 (hex)", result, err)

	// Example 11: Hash with base64 encoding
	result, err = tools.Execute(ctx, "hash", map[string]any{
		"data":      "password123",
		"algorithm": "sha256",
		"encoding":  "base64",
	})
	printResult("Hash: SHA256 (base64)", result, err)

	// Example 12: Base64 encode
	result, err = tools.Execute(ctx, "base64", map[string]any{
		"action": "encode",
		"data":   "Hello, World!",
	})
	printResult("Base64: Encode", result, err)

	// Example 13: Base64 decode
	result, err = tools.Execute(ctx, "base64", map[string]any{
		"action": "decode",
		"data":   "SGVsbG8sIFdvcmxkIQ==",
	})
	printResult("Base64: Decode", result, err)

	// Example 14: URL-safe Base64
	result, err = tools.Execute(ctx, "base64", map[string]any{
		"action": "encode",
		"data":   "Hello, World! With special chars: +/=",
		"url":    true,
	})
	printResult("Base64: URL-safe encode", result, err)

	// Example 15: Generate random token
	result, err = tools.Execute(ctx, "token", map[string]any{
		"action": "generate",
		"type":   "random",
		"length": 32.0,
	})
	printResult("Token: Generate random (32 chars)", result, err)

	// Example 16: Generate shorter token
	result, err = tools.Execute(ctx, "token", map[string]any{
		"action": "generate",
		"type":   "random",
		"length": 16.0,
	})
	printResult("Token: Generate random (16 chars)", result, err)

	// Example 17: JWT token (stub)
	result, err = tools.Execute(ctx, "token", map[string]any{
		"action":  "generate",
		"type":    "jwt",
		"payload": `{"user_id": 123, "role": "admin"}`,
		"secret":  "my-secret-key",
	})
	printResult("Token: JWT (stub)", result, err)

	// Example 18: Encrypt (stub)
	result, err = tools.Execute(ctx, "encrypt", map[string]any{
		"data":      "sensitive data",
		"algorithm": "aes-256-gcm",
	})
	printResult("Encrypt: AES-256-GCM (stub)", result, err)

	// Example 19: Decrypt (stub)
	result, err = tools.Execute(ctx, "decrypt", map[string]any{
		"data":      "encrypted-data-here",
		"algorithm": "aes-256-gcm",
	})
	printResult("Decrypt: AES-256-GCM (stub)", result, err)

	// Example 20: Using Hash helper function directly
	hash, err := tools.Hash("hello world", "sha256")
	if err != nil {
		fmt.Printf("\n❌ Hash helper error: %v\n", err)
	} else {
		fmt.Printf("\n✅ Hash helper: SHA256('hello world') = %s\n", hash)
	}
}
