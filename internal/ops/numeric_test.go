package ops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monstercameron/schemaflux/internal/types"
)

// S-009. float64 is the wrong default for money, identifiers, and large
// integers, and it is the default a Go struct gets by accident.
func TestNumericFidelityCatchesSilentLoss(t *testing.T) {
	t.Run("a nineteen-digit identifier in a float64", func(t *testing.T) {
		type record struct {
			Account float64 `json:"account"`
		}
		var target record
		// 9007199254740993 is the first integer float64 cannot represent.
		err := DecodeExact(`{"account":9007199254740993}`, &target, DecodeLimits{})
		if err == nil {
			t.Fatal("a value float64 cannot hold was accepted silently")
		}
		if !strings.Contains(err.Error(), "account") {
			t.Errorf("the error does not name the field: %v", err)
		}
		if kind := types.KindOf(err); kind != types.KindSchemaViolation {
			t.Errorf("kind = %v, want schema violation", kind)
		}
	})

	// float32 is the documented blind spot, and asserting the limitation keeps
	// it from being mistaken for coverage. Go marshals a float32 as the
	// shortest decimal that round-trips as a float32, so 1284.57 stored as
	// 1284.5699462890625 re-encodes as "1284.57" and the trip looks clean. The
	// loss is real; this method cannot see it. Catching a float32 money field
	// belongs at preflight (S-010).
	t.Run("float32 loss is not detected, and the test says so", func(t *testing.T) {
		type record struct {
			Price float32 `json:"price"`
		}
		var target record
		if err := DecodeExact(`{"price":1284.57}`, &target, DecodeLimits{}); err != nil {
			t.Fatalf("DecodeExact = %v; if this now fails, the blind spot closed and the comment above is stale", err)
		}
		// The value really is wrong, which is the point of recording this.
		if float64(target.Price) == 1284.57 {
			t.Skip("this platform's float32 holds 1284.57 exactly, so there is nothing to miss")
		}
	})

	t.Run("precision beyond float64 in a nested field", func(t *testing.T) {
		type line struct {
			Amount float64 `json:"amount"`
		}
		type record struct {
			Lines []line `json:"lines"`
		}
		var target record
		err := DecodeExact(`{"lines":[{"amount":1},{"amount":0.12345678901234567890123}]}`, &target, DecodeLimits{})
		if err == nil {
			t.Fatal("precision loss inside an array element was accepted")
		}
		if !strings.Contains(err.Error(), "/lines/1/amount") {
			t.Errorf("the error does not point at the field: %v", err)
		}
	})
}

// The values a type *can* hold pass, or the check is just noise.
func TestNumericFidelityAcceptsWhatFits(t *testing.T) {
	type record struct {
		Count  int     `json:"count"`
		Price  float64 `json:"price"`
		Ratio  float64 `json:"ratio"`
		Big    int64   `json:"big"`
		Signed int     `json:"signed"`
	}

	bodies := []string{
		`{"count":3,"price":1284.5,"ratio":0.5,"big":9007199254740992,"signed":-42}`,
		// Trailing-zero and exponent forms are the same rational, not a loss.
		`{"count":3,"price":1284.50,"ratio":5e-1,"big":1,"signed":0}`,
		`{"count":0,"price":0,"ratio":0,"big":0,"signed":0}`,
	}

	for _, body := range bodies {
		var target record
		if err := DecodeExact(body, &target, DecodeLimits{}); err != nil {
			t.Errorf("DecodeExact(%s) = %v", body, err)
		}
	}
}

// An integer type that cannot hold the value is encoding/json's error, and it
// has to stay an error rather than being masked by this check.
func TestOverflowIsStillReported(t *testing.T) {
	type record struct {
		Status int8 `json:"status"`
	}
	var target record
	if err := DecodeExact(`{"status":9000}`, &target, DecodeLimits{}); err == nil {
		t.Error("9000 was accepted into an int8")
	}
}

// json.Number is the escape hatch for a caller who wants the literal, and it
// must not be reported as loss -- it is the opposite.
func TestJSONNumberKeepsTheLiteral(t *testing.T) {
	type record struct {
		Account json.Number `json:"account"`
	}
	var target record
	if err := DecodeExact(`{"account":9007199254740993}`, &target, DecodeLimits{}); err != nil {
		t.Fatalf("json.Number reported a precision loss: %v", err)
	}
	if target.Account.String() != "9007199254740993" {
		t.Errorf("Account = %s, want the exact literal", target.Account)
	}
}

// A string field holding digits keeps its leading zeros, which is the reason to
// use one for an account number or a postal code.
func TestStringFieldsKeepTheirShape(t *testing.T) {
	type record struct {
		Postal  string `json:"postal"`
		Account string `json:"account"`
	}
	var target record
	if err := DecodeExact(`{"postal":"02134","account":"0000123456789012345"}`, &target, DecodeLimits{}); err != nil {
		t.Fatalf("DecodeExact: %v", err)
	}
	if target.Postal != "02134" {
		t.Errorf("Postal = %q, want the leading zero kept", target.Postal)
	}
}

// The message names the field and the two values -- which are numbers, not
// somebody's name -- because "a number changed" without saying which is not
// actionable.
func TestTheLossMessageIsActionable(t *testing.T) {
	type record struct {
		Account float64 `json:"account"`
	}
	var target record
	err := DecodeExact(`{"account":9007199254740993}`, &target, DecodeLimits{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"account", "9007199254740993", "cannot be held"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not contain %q: %v", want, err)
		}
	}
}
