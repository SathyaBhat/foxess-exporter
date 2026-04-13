package foxess

// White-box tests for unexported helpers in the foxess package.
// Uses package foxess (not foxess_test) so sign() is accessible.

import (
	"crypto/md5" //nolint:gosec
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wantSign computes the expected signature independently of sign() so we have
// a separate reference implementation to compare against.
// Uses literal \r\n (4 chars) matching the FoxESS Python example:
//
//	fr'{path}\r\n{token}\r\n{timestamp}'
func wantSign(path, token string, tsMs int64) string {
	raw := path + `\r\n` + token + `\r\n` + strconv.FormatInt(tsMs, 10) //nolint:gosec
	return fmt.Sprintf("%x", md5.Sum([]byte(raw)))                       //nolint:gosec
}

func TestSign_Format(t *testing.T) {
	got := sign("/op/v0/device/list", "test-api-key", 1704067200000)
	// Must be a 32-character lowercase hex string.
	require.Len(t, got, 32, "signature must be a 32-char MD5 hex string")
	for _, ch := range got {
		assert.True(t, (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f'),
			"signature must be lowercase hex, got char %q", ch)
	}
}

func TestSign_Deterministic(t *testing.T) {
	const path, token = "/op/v1/device/real/query", "my-key"
	const tsMs = int64(1704153600000)
	a := sign(path, token, tsMs)
	b := sign(path, token, tsMs)
	assert.Equal(t, a, b, "sign must be deterministic for the same inputs")
}

func TestSign_MatchesReferenceImpl(t *testing.T) {
	cases := []struct {
		path  string
		token string
		tsMs  int64
	}{
		{"/op/v0/device/list", "test-api-key", 1704067200000},
		{"/op/v1/device/real/query", "another-key-xyz", 1712000000000},
		{"/op/v0/device/history/query", "key-with-special-!@#", 9999999999999},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := sign(tc.path, tc.token, tc.tsMs)
			want := wantSign(tc.path, tc.token, tc.tsMs)
			assert.Equal(t, want, got,
				"sign(%q, %q, %d) mismatch", tc.path, tc.token, tc.tsMs)
		})
	}
}

// TestSign_UsesLiteralBackslashRN verifies that the separator is the 4-character
// literal sequence "\r\n" (backslash-r-backslash-n), NOT actual CRLF bytes.
// The FoxESS API Python example uses fr'{path}\r\n{token}\r\n{timestamp}' where
// the raw-string prefix makes \r\n literal characters, not escape sequences.
func TestSign_UsesLiteralBackslashRN(t *testing.T) {
	const path, token = "/test", "k"
	const tsMs = int64(1)

	// What the correct implementation produces (literal \r\n separator).
	rawLiteral := path + `\r\n` + token + `\r\n` + "1" //nolint:gosec
	wantLiteral := fmt.Sprintf("%x", md5.Sum([]byte(rawLiteral))) //nolint:gosec

	// What a CRLF implementation would produce (wrong).
	rawCRLF := fmt.Sprintf("%s\r\n%s\r\n%d", path, token, tsMs) //nolint:gosec
	wrongCRLF := fmt.Sprintf("%x", md5.Sum([]byte(rawCRLF)))    //nolint:gosec

	got := sign(path, token, tsMs)
	assert.Equal(t, wantLiteral, got,
		"sign must use literal \\r\\n separator (4 chars), not CRLF bytes")
	assert.NotEqual(t, wrongCRLF, got,
		"sign must NOT use actual CRLF bytes as separator")
}

// TestSign_InputOrder verifies that path, token, timestamp appear in that order.
func TestSign_InputOrderMatters(t *testing.T) {
	const path, token = "/path", "token"
	const tsMs = int64(12345)

	correct := sign(path, token, tsMs)
	// Swap path and token — should produce a different signature.
	swapped := sign(token, path, tsMs)
	assert.NotEqual(t, correct, swapped, "order of path and token must matter")
}
