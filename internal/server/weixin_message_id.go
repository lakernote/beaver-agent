package server

import "strings"

// compareWeixinMessageID compares non-negative decimal IDs without converting
// them to a fixed-width integer. Empty and all-zero values are the zero sentinel.
func compareWeixinMessageID(left, right string) int {
	left = canonicalWeixinMessageID(left)
	right = canonicalWeixinMessageID(right)
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func canonicalWeixinMessageID(value string) string {
	value = strings.TrimLeft(strings.TrimSpace(value), "0")
	if value == "" {
		return "0"
	}
	return value
}
