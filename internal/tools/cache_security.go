package tools

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sync"
	"time"
)

// CacheTool provides in-memory caching.
var CacheTool = &Tool{
	Name:        "cache",
	Description: "In-memory cache operations (get, set, delete, clear)",
	Category:    CategoryCache,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action to perform", []string{"get", "set", "delete", "clear", "keys", "has"}),
		"key":    StringParam("Cache key"),
		"value":  StringParam("Value to cache (for set action)"),
		"ttl":    NumberParam("Time-to-live in seconds (for set action)"),
	}, []string{"action"}),
	Execute: executeCache,
}

// CacheEntry represents a cached value with metadata.
type CacheEntry struct {
	Value     any       `json:"value"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// IsExpired checks if the entry has expired.
func (e *CacheEntry) IsExpired() bool {
	if e.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(e.ExpiresAt)
}

// InMemoryCache is a thread-safe in-memory cache.
type InMemoryCache struct {
	mu    sync.RWMutex
	data  map[string]*CacheEntry
	stats CacheStats
}

// CacheStats tracks cache statistics.
type CacheStats struct {
	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
	Sets   int64 `json:"sets"`
	Evicts int64 `json:"evicts"`
}

var globalCache = &InMemoryCache{
	data: make(map[string]*CacheEntry),
}

// Get retrieves a value from the cache.
func (c *InMemoryCache) Get(key string) (any, bool) {
	c.mu.RLock()
	entry, exists := c.data[key]
	c.mu.RUnlock()

	if !exists {
		c.mu.Lock()
		c.stats.Misses++
		c.mu.Unlock()
		return nil, false
	}

	if entry.IsExpired() {
		c.Delete(key)
		c.mu.Lock()
		c.stats.Misses++
		c.mu.Unlock()
		return nil, false
	}

	c.mu.Lock()
	c.stats.Hits++
	c.mu.Unlock()

	return entry.Value, true
}

// Set stores a value in the cache.
func (c *InMemoryCache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := &CacheEntry{
		Value:     value,
		CreatedAt: time.Now(),
	}
	if ttl > 0 {
		entry.ExpiresAt = time.Now().Add(ttl)
	}

	c.data[key] = entry
	c.stats.Sets++
}

// Delete removes a key from the cache.
func (c *InMemoryCache) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, exists := c.data[key]
	if exists {
		delete(c.data, key)
		c.stats.Evicts++
	}
	return exists
}

// Clear removes all entries from the cache.
func (c *InMemoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data = make(map[string]*CacheEntry)
}

// Keys returns all cache keys.
func (c *InMemoryCache) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, len(c.data))
	for k := range c.data {
		keys = append(keys, k)
	}
	return keys
}

// Has checks if a key exists (and is not expired).
func (c *InMemoryCache) Has(key string) bool {
	_, exists := c.Get(key)
	return exists
}

// Stats returns cache statistics.
func (c *InMemoryCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats
}

// Size returns the number of entries in the cache.
func (c *InMemoryCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data)
}

func executeCache(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	key, _ := params["key"].(string)
	value, _ := params["value"].(string)
	ttlSec, _ := params["ttl"].(float64)

	switch action {
	case "get":
		if key == "" {
			return ErrorResultFromError(fmt.Errorf("key is required for get action")), nil
		}
		val, exists := globalCache.Get(key)
		if !exists {
			return NewResultWithMeta(nil, map[string]any{
				"key":   key,
				"found": false,
			}), nil
		}
		return NewResultWithMeta(val, map[string]any{
			"key":   key,
			"found": true,
		}), nil

	case "set":
		if key == "" {
			return ErrorResultFromError(fmt.Errorf("key is required for set action")), nil
		}
		ttl := time.Duration(ttlSec) * time.Second
		globalCache.Set(key, value, ttl)
		return NewResultWithMeta("cached", map[string]any{
			"key": key,
			"ttl": ttlSec,
		}), nil

	case "delete":
		if key == "" {
			return ErrorResultFromError(fmt.Errorf("key is required for delete action")), nil
		}
		deleted := globalCache.Delete(key)
		return NewResultWithMeta(deleted, map[string]any{
			"key":     key,
			"deleted": deleted,
		}), nil

	case "clear":
		globalCache.Clear()
		return NewResult("cache cleared"), nil

	case "keys":
		keys := globalCache.Keys()
		return NewResultWithMeta(keys, map[string]any{
			"count": len(keys),
		}), nil

	case "has":
		if key == "" {
			return ErrorResultFromError(fmt.Errorf("key is required for has action")), nil
		}
		return NewResult(globalCache.Has(key)), nil

	default:
		return ErrorResultFromError(fmt.Errorf("unknown action: %s", action)), nil
	}
}

// MemoizeTool caches function results.
var MemoizeTool = &Tool{
	Name:        "memoize",
	Description: "Cache results of expensive operations by key",
	Category:    CategoryCache,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"key":   StringParam("Unique key for the memoized result"),
		"value": StringParam("Value to memoize (if not already cached)"),
		"ttl":   NumberParam("Time-to-live in seconds"),
		"force": BoolParam("Force update even if cached"),
	}, []string{"key"}),
	Execute: executeMemoize,
}

func executeMemoize(ctx context.Context, params map[string]any) (Result, error) {
	key, _ := params["key"].(string)
	value, _ := params["value"].(string)
	ttlSec, _ := params["ttl"].(float64)
	force, _ := params["force"].(bool)

	// Check if already cached
	if !force {
		if cached, exists := globalCache.Get(key); exists {
			return NewResultWithMeta(cached, map[string]any{
				"key":    key,
				"cached": true,
			}), nil
		}
	}

	// Store new value
	ttl := time.Duration(ttlSec) * time.Second
	globalCache.Set(key, value, ttl)

	return NewResultWithMeta(value, map[string]any{
		"key":    key,
		"cached": false,
	}), nil
}

// HashTool computes cryptographic hashes.
var HashTool = &Tool{
	Name:        "hash",
	Description: "Compute cryptographic hash of data",
	Category:    CategorySecurity,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"data":      StringParam("Data to hash"),
		"algorithm": EnumParam("Hash algorithm", []string{"md5", "sha1", "sha256", "sha512"}),
		"encoding":  EnumParam("Output encoding", []string{"hex", "base64"}),
	}, []string{"data"}),
	Execute: executeHash,
}

func executeHash(ctx context.Context, params map[string]any) (Result, error) {
	data, _ := params["data"].(string)
	algorithm, _ := params["algorithm"].(string)
	encoding, _ := params["encoding"].(string)

	if algorithm == "" {
		algorithm = "sha256"
	}
	if encoding == "" {
		encoding = "hex"
	}

	var h hash.Hash
	switch algorithm {
	case "md5":
		h = md5.New()
	case "sha1":
		h = sha1.New()
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	default:
		return ErrorResultFromError(fmt.Errorf("unknown algorithm: %s", algorithm)), nil
	}

	h.Write([]byte(data))
	digest := h.Sum(nil)

	var result string
	switch encoding {
	case "hex":
		result = hex.EncodeToString(digest)
	case "base64":
		result = base64.StdEncoding.EncodeToString(digest)
	default:
		return ErrorResultFromError(fmt.Errorf("unknown encoding: %s", encoding)), nil
	}

	return NewResultWithMeta(result, map[string]any{
		"algorithm": algorithm,
		"encoding":  encoding,
	}), nil
}

// Hash computes a hash of data using the specified algorithm.
func Hash(data string, algorithm string) (string, error) {
	result, err := HashTool.Execute(context.Background(), map[string]any{
		"data":      data,
		"algorithm": algorithm,
		"encoding":  "hex",
	})
	if err != nil {
		return "", err
	}
	if !result.Success {
		return "", fmt.Errorf("hash failed: %s", result.Error)
	}
	return result.Data.(string), nil
}

// Base64Tool encodes/decodes base64.
var Base64Tool = &Tool{
	Name:        "base64",
	Description: "Encode or decode base64 data",
	Category:    CategorySecurity,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action": EnumParam("Action to perform", []string{"encode", "decode"}),
		"data":   StringParam("Data to encode/decode"),
		"url":    BoolParam("Use URL-safe encoding"),
	}, []string{"action", "data"}),
	Execute: executeBase64,
}

func executeBase64(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	data, _ := params["data"].(string)
	urlSafe, _ := params["url"].(bool)

	var encoding *base64.Encoding
	if urlSafe {
		encoding = base64.URLEncoding
	} else {
		encoding = base64.StdEncoding
	}

	var result string
	switch action {
	case "encode":
		result = encoding.EncodeToString([]byte(data))
	case "decode":
		decoded, err := encoding.DecodeString(data)
		if err != nil {
			return ErrorResult(err), nil
		}
		result = string(decoded)
	default:
		return ErrorResultFromError(fmt.Errorf("unknown action: %s", action)), nil
	}

	return NewResult(result), nil
}

// TokenTool generates random tokens (STUBBED for JWT).
var TokenTool = &Tool{
	Name:        "token",
	Description: "Generate or validate tokens (JWT support requires additional setup)",
	Category:    CategorySecurity,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"action":  EnumParam("Action", []string{"generate", "validate"}),
		"type":    EnumParam("Token type", []string{"random", "jwt"}),
		"length":  NumberParam("Token length (for random)"),
		"payload": StringParam("JWT payload (JSON)"),
		"secret":  StringParam("JWT secret (for signing)"),
	}, []string{"action", "type"}),
	Execute: executeToken,
}

func executeToken(ctx context.Context, params map[string]any) (Result, error) {
	action, _ := params["action"].(string)
	tokenType, _ := params["type"].(string)
	length, _ := params["length"].(float64)

	if tokenType == "jwt" {
		// This used to return StubResult, which reports Success: true. A caller
		// filtering on IsStub could not see it, because the tool was not marked
		// a stub — so this shipped as a working JWT generator that generated no
		// JWT and said everything went fine.
		return ErrorResultFromError(errors.New("jwt tokens are not implemented; this tool generates random tokens only")), nil
	}

	if action != "generate" {
		return ErrorResultFromError(fmt.Errorf("only 'generate' action is supported for random tokens")), nil
	}

	if length <= 0 {
		length = 32
	}

	token, err := generateRandomToken(int(length))
	if err != nil {
		return ErrorResultFromError(err), nil
	}

	return NewResultWithMeta(token, map[string]any{
		"type":   tokenType,
		"length": length,
	}), nil
}

// generateRandomToken returns length hex characters from crypto/rand.
//
// It used to hash the current nanosecond timestamp, so a token was a
// deterministic function of the time it was issued: anyone who could guess the
// issue time within a window could enumerate the tokens. The comment said "in
// production, use crypto/rand", from a tool in the security category.
func generateRandomToken(length int) (string, error) {
	if length <= 0 {
		return "", errors.New("token length must be positive")
	}

	raw := make([]byte, (length+1)/2)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}

	return hex.EncodeToString(raw)[:length], nil
}

// EncryptTool encrypts data (STUBBED - requires key management).
var EncryptTool = &Tool{
	Name:        "encrypt",
	Description: "Encrypt data using AES (requires encryption key setup)",
	Category:    CategorySecurity,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"data":      StringParam("Data to encrypt"),
		"key":       StringParam("Encryption key (or use default)"),
		"algorithm": EnumParam("Algorithm", []string{"aes-256-gcm", "aes-256-cbc"}),
	}, []string{"data"}),
	Execute:      executeEncryptStub,
	RequiresAuth: true,
	IsStub:       true,
}

func executeEncryptStub(ctx context.Context, params map[string]any) (Result, error) {
	return StubResult("Encryption requires proper key management. Configure ENCRYPTION_KEY or use a key management service."), nil
}

// DecryptTool decrypts data (STUBBED).
var DecryptTool = &Tool{
	Name:        "decrypt",
	Description: "Decrypt data using AES (requires encryption key setup)",
	Category:    CategorySecurity,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"data":      StringParam("Encrypted data (base64)"),
		"key":       StringParam("Encryption key"),
		"algorithm": EnumParam("Algorithm", []string{"aes-256-gcm", "aes-256-cbc"}),
	}, []string{"data"}),
	Execute:      executeDecryptStub,
	RequiresAuth: true,
	IsStub:       true,
}

func executeDecryptStub(ctx context.Context, params map[string]any) (Result, error) {
	return StubResult("Decryption requires proper key management. Configure ENCRYPTION_KEY or use a key management service."), nil
}

func init() {
	mustRegister(CacheTool)
	mustRegister(MemoizeTool)
	mustRegister(HashTool)
	mustRegister(Base64Tool)
	mustRegister(TokenTool)
	mustRegister(EncryptTool)
	mustRegister(DecryptTool)
}
