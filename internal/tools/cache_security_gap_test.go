package tools

import (
	"context"
	"strings"
	"testing"
)

// Deleting a key that was never set reports deleted=false rather than
// erroring, so a caller can use delete idempotently.
func TestCacheToolDeleteMissingKeyReportsNotDeleted(t *testing.T) {
	globalCache = &InMemoryCache{data: make(map[string]*CacheEntry)}

	result, _ := CacheTool.Execute(context.Background(), map[string]any{
		"action": "delete",
		"key":    "never-set",
	})
	if !result.Success {
		t.Fatalf("delete of a missing key should still succeed: %s", result.Error)
	}
	if result.Data != false {
		t.Errorf("Data = %v, want false", result.Data)
	}
}

// set/delete/has each refuse a missing key rather than operating on an
// empty string key.
func TestCacheToolRefusesEmptyKey(t *testing.T) {
	cases := []string{"set", "delete", "has"}
	for _, action := range cases {
		t.Run(action, func(t *testing.T) {
			result, _ := CacheTool.Execute(context.Background(), map[string]any{
				"action": action,
			})
			if result.Success {
				t.Errorf("action %q with no key must be refused", action)
			}
		})
	}
}

// Hash surfaces both failure modes of the tool it wraps: an Execute error
// (not reachable through HashTool today, but Hash's own contract is that it
// propagates one) and a result the tool reports as unsuccessful.
func TestHashPropagatesToolFailure(t *testing.T) {
	if _, err := Hash("data", "not-a-real-algorithm"); err == nil {
		t.Fatal("Hash with an unknown algorithm must return an error")
	} else if !strings.Contains(err.Error(), "hash failed") {
		t.Errorf("the error does not say the hash failed: %v", err)
	}
}

// The token tool only supports "generate" for random tokens; "validate" (or
// anything else) is refused rather than silently doing nothing.
func TestTokenToolRefusesNonGenerateActionsForRandomTokens(t *testing.T) {
	result, _ := TokenTool.Execute(context.Background(), map[string]any{
		"action": "validate",
		"type":   "random",
	})
	if result.Success {
		t.Error("only 'generate' is supported for random tokens")
	}
}

// A length of zero or less falls back to the documented default of 32
// rather than producing an empty or invalid token.
func TestTokenToolDefaultsLengthWhenNotPositive(t *testing.T) {
	result, _ := TokenTool.Execute(context.Background(), map[string]any{
		"action": "generate",
		"type":   "random",
		"length": 0.0,
	})
	if !result.Success {
		t.Fatalf("Execute: %s", result.Error)
	}
	if token, _ := result.Data.(string); len(token) != 32 {
		t.Errorf("token length = %d, want the default of 32", len(token))
	}
}

// base64 encode/decode both honour the URL-safe alphabet consistently: a
// value with characters that differ between the two alphabets round-trips
// only when both calls agree on url-safety.
func TestBase64URLSafeRoundTrips(t *testing.T) {
	raw := "\xfb\xff\xfe subject to alphabet differences"

	encoded, err := Base64Tool.Execute(context.Background(), map[string]any{
		"action": "encode",
		"data":   raw,
		"url":    true,
	})
	if err != nil || !encoded.Success {
		t.Fatalf("encode: %v %v", err, encoded.Error)
	}

	decoded, err := Base64Tool.Execute(context.Background(), map[string]any{
		"action": "decode",
		"data":   encoded.Data.(string),
		"url":    true,
	})
	if err != nil || !decoded.Success {
		t.Fatalf("decode: %v %v", err, decoded.Error)
	}
	if decoded.Data.(string) != raw {
		t.Errorf("round trip = %q, want %q", decoded.Data, raw)
	}
}

// The base64 tool refuses an action it does not recognise.
func TestBase64ToolRefusesUnknownAction(t *testing.T) {
	result, _ := Base64Tool.Execute(context.Background(), map[string]any{
		"action": "rot13",
		"data":   "x",
	})
	if result.Success {
		t.Error("an unrecognised base64 action must be refused")
	}
}
