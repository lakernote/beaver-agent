package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMarshalRedactsBeforeTruncation(t *testing.T) {
	const limit = 1024
	result := Marshal(map[string]any{
		"authorization": "Bearer private-secret",
		"image":         "data:image/png;base64,c2VjcmV0",
		"padding":       strings.Repeat("x", limit*2),
	}, limit)
	if strings.Contains(result, "private-secret") || strings.Contains(result, "c2VjcmV0") {
		t.Fatal("trace still contains sensitive data")
	}
	if !strings.Contains(result, "redacted") || !strings.Contains(result, "image/png attachment data omitted") {
		t.Fatal("trace lost redaction markers")
	}
	if len(result) > limit || !strings.HasSuffix(result, "… [truncated]") || !utf8.ValidString(result) {
		t.Fatalf("trace is not safely bounded: bytes=%d valid=%v", len(result), utf8.ValidString(result))
	}
}

func TestSanitizePreservesNonJSONText(t *testing.T) {
	const input = "plain tool output"
	if result := Sanitize(input); result != input {
		t.Fatalf("Sanitize() = %q, want %q", result, input)
	}
}

func TestSanitizeRedactsPrivateDataInText(t *testing.T) {
	const message = "HTTP 403 for member@example.test. Authorization: Bearer demo-value-12345, URL https://example.test/callback?code=demo-code&state=demo-state&keep=yes"
	for _, input := range []string{message, `{"error":"` + message + `"}`} {
		result := Sanitize(input)
		for _, value := range []string{"member@example.test", "demo-value-12345", "demo-code", "demo-state"} {
			if strings.Contains(result, value) {
				t.Fatal("trace still contains private data")
			}
		}
		if !strings.Contains(result, "HTTP 403") || !strings.Contains(result, "keep=yes") {
			t.Fatal("trace lost its diagnostic details")
		}
	}
}
