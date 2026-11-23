// Package redact 在写入 Trace 前脱敏并截断结构化值（去掉附件数据与凭据字段）。
package redact

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	privateValue = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+|[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,63})\b`)
	authHeader   = regexp.MustCompile(`(?i)(\bauthorization\s*:\s*(?:basic|bearer)\s+|\bbearer\s+)[a-z0-9._~+/=-]{8,}`)
	oauthValue   = regexp.MustCompile(`(?i)([?&](?:code|state|code_verifier)=|\b(?:[a-z0-9_-]*[_-])?(?:api[_-]?key|(?:access|refresh|id)[_-]?token|client[_-]?secret|password|code[_-]?verifier)["']?\s*[:=]\s*["']?)[^\s"'<>;&]+`)
)

// Marshal redacts structured values before applying the storage limit. Redaction
// must happen first: truncating JSON can make it invalid and prevent safe parsing.
func Marshal(value any, limit int) string {
	var encoded string
	if text, ok := value.(string); ok {
		encoded = Sanitize(text)
	} else {
		data, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		encoded = Sanitize(string(data))
	}
	return truncateUTF8(encoded, limit)
}

// Sanitize preserves JSON structure and removes common secrets from both
// structured fields and text in traces or error messages.
func Sanitize(value string) string {
	var payload any
	if json.Unmarshal([]byte(value), &payload) != nil {
		return sanitizeText(value)
	}
	encoded, err := json.Marshal(sanitize(payload))
	if err != nil {
		return value
	}
	return string(encoded)
}

func sanitize(value any) any {
	switch typed := value.(type) {
	case string:
		if strings.HasPrefix(typed, "data:") {
			if marker := strings.Index(typed, ";base64,"); marker > len("data:") {
				return fmt.Sprintf("<%s attachment data omitted>", typed[len("data:"):marker])
			}
		}
		return sanitizeText(typed)
	case []any:
		for index := range typed {
			typed[index] = sanitize(typed[index])
		}
		return typed
	case map[string]any:
		for key := range typed {
			if isSensitiveKey(key) {
				typed[key] = "<redacted>"
				continue
			}
			typed[key] = sanitize(typed[key])
		}
		return typed
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	if normalized == "encryptedcontent" || normalized == "email" {
		return true
	}
	for _, suffix := range []string{"apikey", "authorization", "password", "secret", "token", "cookie"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func sanitizeText(value string) string {
	value = authHeader.ReplaceAllString(value, "${1}<redacted>")
	value = oauthValue.ReplaceAllString(value, "${1}<redacted>")
	return privateValue.ReplaceAllString(value, "<redacted>")
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	const suffix = "… [truncated]"
	if limit <= len(suffix) {
		cut := limit
		for cut > 0 && !utf8.ValidString(value[:cut]) {
			cut--
		}
		return value[:cut]
	}
	cut := limit - len(suffix)
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + suffix
}
