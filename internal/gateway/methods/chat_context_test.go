package methods

import (
	"strings"
	"testing"
)

func TestParseTurnContextAcceptsFreeFormData(t *testing.T) {
	raw := []byte(`{"version":1,"data":{"route":"/dashboard","tokens":["BTC","ETH"]}}`)
	ctx, err := parseTurnContext(raw)
	if err != nil {
		t.Fatalf("parseTurnContext returned error: %v", err)
	}
	if ctx == nil || len(ctx.Data) == 0 {
		t.Fatalf("parsed context = %#v", ctx)
	}
}

func TestParseTurnContextAcceptsEmptyData(t *testing.T) {
	raw := []byte(`{"version":1}`)
	ctx, err := parseTurnContext(raw)
	if err != nil {
		t.Fatalf("parseTurnContext returned error: %v", err)
	}
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

func TestParseTurnContextRejectsWrongVersion(t *testing.T) {
	raw := []byte(`{"version":99,"data":{}}`)
	_, err := parseTurnContext(raw)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error = %v, want unsupported version", err)
	}
}

func TestParseTurnContextRejectsOversizedRawInput(t *testing.T) {
	raw := []byte(`{"version":1,"padding":"` + strings.Repeat("x", maxTurnContextBytes) + `"}`)
	_, err := parseTurnContext(raw)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v, want oversized context", err)
	}
}

func TestParseTurnContextReturnsNilForEmptyInput(t *testing.T) {
	for _, input := range [][]byte{nil, []byte(""), []byte("null"), []byte("  ")} {
		ctx, err := parseTurnContext(input)
		if err != nil {
			t.Fatalf("input=%q error: %v", input, err)
		}
		if ctx != nil {
			t.Fatalf("input=%q expected nil context", input)
		}
	}
}
