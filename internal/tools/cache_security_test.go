package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCacheOperations(t *testing.T) {
	cache := &InMemoryCache{
		data: make(map[string]*CacheEntry),
	}

	// Test Set and Get
	cache.Set("key1", "value1", 0)
	val, exists := cache.Get("key1")
	if !exists {
		t.Error("Expected key1 to exist")
	}
	if val != "value1" {
		t.Errorf("Expected 'value1', got %v", val)
	}

	// Test Get non-existent
	_, exists = cache.Get("nonexistent")
	if exists {
		t.Error("Expected nonexistent key to not exist")
	}

	// Test Delete
	cache.Delete("key1")
	_, exists = cache.Get("key1")
	if exists {
		t.Error("Expected key1 to be deleted")
	}

	// Test Clear
	cache.Set("key2", "value2", 0)
	cache.Set("key3", "value3", 0)
	cache.Clear()
	if cache.Size() != 0 {
		t.Errorf("Expected empty cache, got size %d", cache.Size())
	}

	// Test Keys
	cache.Set("a", "1", 0)
	cache.Set("b", "2", 0)
	keys := cache.Keys()
	if len(keys) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(keys))
	}

	// Test Has
	if !cache.Has("a") {
		t.Error("Expected 'a' to exist")
	}
	if cache.Has("nonexistent") {
		t.Error("Expected 'nonexistent' to not exist")
	}
}

func TestCacheTTL(t *testing.T) {
	cache := &InMemoryCache{
		data: make(map[string]*CacheEntry),
	}

	// Set with short TTL
	cache.Set("expiring", "value", 100*time.Millisecond)

	// Should exist initially
	val, exists := cache.Get("expiring")
	if !exists || val != "value" {
		t.Error("Expected key to exist before expiration")
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should be expired
	_, exists = cache.Get("expiring")
	if exists {
		t.Error("Expected key to be expired")
	}
}

func TestCacheStats(t *testing.T) {
	cache := &InMemoryCache{
		data: make(map[string]*CacheEntry),
	}

	cache.Set("key", "value", 0)
	cache.Get("key")     // hit
	cache.Get("missing") // miss
	cache.Delete("key")  // evict

	stats := cache.Stats()
	if stats.Sets != 1 {
		t.Errorf("Expected 1 set, got %d", stats.Sets)
	}
	if stats.Hits != 1 {
		t.Errorf("Expected 1 hit, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Expected 1 miss, got %d", stats.Misses)
	}
	if stats.Evicts != 1 {
		t.Errorf("Expected 1 evict, got %d", stats.Evicts)
	}
}

func TestCacheTool(t *testing.T) {
	// Reset global cache
	globalCache = &InMemoryCache{
		data: make(map[string]*CacheEntry),
	}

	// Test set
	result, _ := CacheTool.Execute(context.Background(), map[string]any{
		"action": "set",
		"key":    "test_key",
		"value":  "test_value",
		"ttl":    60.0,
	})
	if !result.Success {
		t.Errorf("Set failed: %s", result.Error)
	}

	// Test get
	result, _ = CacheTool.Execute(context.Background(), map[string]any{
		"action": "get",
		"key":    "test_key",
	})
	if result.Data != "test_value" {
		t.Errorf("Expected 'test_value', got %v", result.Data)
	}

	// Test has
	result, _ = CacheTool.Execute(context.Background(), map[string]any{
		"action": "has",
		"key":    "test_key",
	})
	if result.Data != true {
		t.Error("Expected true for has")
	}

	// Test keys
	result, _ = CacheTool.Execute(context.Background(), map[string]any{
		"action": "keys",
	})
	keys := result.Data.([]string)
	if len(keys) != 1 {
		t.Errorf("Expected 1 key, got %d", len(keys))
	}

	// Test delete
	result, _ = CacheTool.Execute(context.Background(), map[string]any{
		"action": "delete",
		"key":    "test_key",
	})
	if result.Data != true {
		t.Error("Expected true for delete")
	}

	// Test clear
	CacheTool.Execute(context.Background(), map[string]any{
		"action": "set",
		"key":    "key1",
		"value":  "val1",
	})
	result, _ = CacheTool.Execute(context.Background(), map[string]any{
		"action": "clear",
	})
	if !result.Success {
		t.Errorf("Clear failed: %s", result.Error)
	}
}

func TestMemoizeTool(t *testing.T) {
	globalCache = &InMemoryCache{
		data: make(map[string]*CacheEntry),
	}

	// First call - should cache
	result, _ := MemoizeTool.Execute(context.Background(), map[string]any{
		"key":   "expensive_op",
		"value": "computed_result",
		"ttl":   60.0,
	})
	if result.Metadata["cached"] != false {
		t.Error("Expected cached=false for first call")
	}

	// Second call - should return cached
	result, _ = MemoizeTool.Execute(context.Background(), map[string]any{
		"key": "expensive_op",
	})
	if result.Metadata["cached"] != true {
		t.Error("Expected cached=true for second call")
	}
	if result.Data != "computed_result" {
		t.Errorf("Expected 'computed_result', got %v", result.Data)
	}

	// Force update
	result, _ = MemoizeTool.Execute(context.Background(), map[string]any{
		"key":   "expensive_op",
		"value": "new_result",
		"force": true,
	})
	if result.Data != "new_result" {
		t.Errorf("Expected 'new_result', got %v", result.Data)
	}
}

func TestHashTool(t *testing.T) {
	tests := []struct {
		algorithm string
		data      string
	}{
		{"md5", "hello"},
		{"sha1", "hello"},
		{"sha256", "hello"},
		{"sha512", "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.algorithm, func(t *testing.T) {
			result, _ := HashTool.Execute(context.Background(), map[string]any{
				"data":      tt.data,
				"algorithm": tt.algorithm,
			})
			if !result.Success {
				t.Errorf("Hash failed: %s", result.Error)
			}
			if result.Data.(string) == "" {
				t.Error("Expected non-empty hash")
			}
		})
	}

	// Test base64 encoding
	result, _ := HashTool.Execute(context.Background(), map[string]any{
		"data":      "hello",
		"algorithm": "sha256",
		"encoding":  "base64",
	})
	if !result.Success {
		t.Errorf("Hash with base64 failed: %s", result.Error)
	}
}

func TestHash(t *testing.T) {
	hash, err := Hash("hello", "sha256")
	if err != nil {
		t.Fatalf("Hash error: %v", err)
	}
	if hash == "" {
		t.Error("Expected non-empty hash")
	}

	// SHA256 of "hello" should be consistent
	hash2, _ := Hash("hello", "sha256")
	if hash != hash2 {
		t.Error("Hash should be deterministic")
	}
}

func TestBase64Tool(t *testing.T) {
	// Test encode
	result, _ := Base64Tool.Execute(context.Background(), map[string]any{
		"action": "encode",
		"data":   "hello world",
	})
	encoded := result.Data.(string)
	if encoded != "aGVsbG8gd29ybGQ=" {
		t.Errorf("Expected 'aGVsbG8gd29ybGQ=', got %s", encoded)
	}

	// Test decode
	result, _ = Base64Tool.Execute(context.Background(), map[string]any{
		"action": "decode",
		"data":   "aGVsbG8gd29ybGQ=",
	})
	decoded := result.Data.(string)
	if decoded != "hello world" {
		t.Errorf("Expected 'hello world', got %s", decoded)
	}

	// Test URL-safe encoding
	result, _ = Base64Tool.Execute(context.Background(), map[string]any{
		"action": "encode",
		"data":   "hello+world/test",
		"url":    true,
	})
	if !result.Success {
		t.Errorf("URL-safe encode failed: %s", result.Error)
	}

	// Test invalid base64
	result, _ = Base64Tool.Execute(context.Background(), map[string]any{
		"action": "decode",
		"data":   "!!!invalid!!!",
	})
	if result.Success {
		t.Error("Expected failure for invalid base64")
	}
}

func TestTokenTool(t *testing.T) {
	// Test random token generation
	result, _ := TokenTool.Execute(context.Background(), map[string]any{
		"action": "generate",
		"type":   "random",
		"length": 16.0,
	})
	if !result.Success {
		t.Errorf("Token generation failed: %s", result.Error)
	}
	token := result.Data.(string)
	if len(token) != 16 {
		t.Errorf("Expected 16 char token, got %d", len(token))
	}

	// The JWT path is unimplemented, and says so rather than returning a
	// successful-looking stub from a tool that is not marked as a stub.
	result, _ = TokenTool.Execute(context.Background(), map[string]any{
		"action": "generate",
		"type":   "jwt",
	})
	if result.Success {
		t.Error("Expected the unimplemented JWT path to be refused")
	}
}

func TestEncryptDecryptStubs(t *testing.T) {
	// Encrypt stub
	result, _ := EncryptTool.Execute(context.Background(), map[string]any{
		"data": "secret",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected encrypt to be stubbed")
	}

	// Decrypt stub
	result, _ = DecryptTool.Execute(context.Background(), map[string]any{
		"data": "encrypted_data",
	})
	if result.Metadata["stubbed"] != true {
		t.Error("Expected decrypt to be stubbed")
	}
}

func TestCacheToolErrors(t *testing.T) {
	// Missing key for get
	result, _ := CacheTool.Execute(context.Background(), map[string]any{
		"action": "get",
	})
	if result.Success {
		t.Error("Expected failure for missing key")
	}

	// Unknown action
	result, _ = CacheTool.Execute(context.Background(), map[string]any{
		"action": "unknown",
	})
	if result.Success {
		t.Error("Expected failure for unknown action")
	}
}

func TestHashToolErrors(t *testing.T) {
	// Unknown algorithm
	result, _ := HashTool.Execute(context.Background(), map[string]any{
		"data":      "test",
		"algorithm": "unknown",
	})
	if result.Success {
		t.Error("Expected failure for unknown algorithm")
	}

	// Unknown encoding
	result, _ = HashTool.Execute(context.Background(), map[string]any{
		"data":     "test",
		"encoding": "unknown",
	})
	if result.Success {
		t.Error("Expected failure for unknown encoding")
	}
}

func TestGenerateRandomToken(t *testing.T) {
	for _, length := range []int{1, 2, 8, 16, 32, 64, 128} {
		token, err := generateRandomToken(length)
		if err != nil {
			t.Fatalf("generateRandomToken(%d): %v", length, err)
		}
		if len(token) != length {
			t.Errorf("length %d produced %d characters", length, len(token))
		}
	}

	if _, err := generateRandomToken(0); err == nil {
		t.Error("a zero length must be an error")
	}
	if _, err := generateRandomToken(-1); err == nil {
		t.Error("a negative length must be an error")
	}
}

// The old implementation hashed the current nanosecond, so tokens issued in the
// same nanosecond were identical and every token was a function of its issue
// time. A thousand draws must all differ.
func TestGeneratedTokensAreUnpredictable(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		token, err := generateRandomToken(32)
		if err != nil {
			t.Fatalf("generateRandomToken: %v", err)
		}
		if _, duplicate := seen[token]; duplicate {
			t.Fatalf("token %q was issued twice in %d draws", token, i+1)
		}
		seen[token] = struct{}{}
	}
}

// The jwt path returned a successful-looking stub from a tool not marked as a
// stub, so a caller filtering on IsStub shipped a JWT generator that generated
// nothing.
func TestTokenToolRefusesJWTRatherThanFakingIt(t *testing.T) {
	result, err := TokenTool.Execute(context.Background(), map[string]any{
		"action": "generate",
		"type":   "jwt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("an unimplemented JWT path must not report success")
	}
	if !strings.Contains(strings.ToLower(result.Error), "jwt") {
		t.Errorf("the error should name what is unimplemented, got %q", result.Error)
	}
	if stubbed, _ := result.Metadata["stubbed"].(bool); stubbed {
		t.Error("this is not a stub result, it is a refusal")
	}
}

// The random path still works.
func TestTokenToolGeneratesRandomTokens(t *testing.T) {
	result, err := TokenTool.Execute(context.Background(), map[string]any{
		"action": "generate",
		"type":   "random",
		"length": float64(48),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("the random path must work: %s", result.Error)
	}
	token, _ := result.Data.(string)
	if len(token) != 48 {
		t.Errorf("token length = %d, want 48", len(token))
	}
}
