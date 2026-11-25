package redact

import (
	"math/rand"
	"strings"
)

// fakeToken builds format-shaped credentials at run time. Keeping the complete
// tokens out of the source avoids GitHub push protection while still
// exercising the same detectors and entropies.
var (
	openAIProject = fakeToken("sk-proj-", 100, 1)
	openrouter    = fakeHex("sk-or-v1-", 64, 16)
	xai           = fakeToken("xai-", 80, 14)
)

func fakeToken(prefix string, n int, seed int64) string {
	return prefix + randomString("abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789", n, seed)
}

func fakeHex(prefix string, n int, seed int64) string {
	return prefix + randomString("0123456789abcdef", n, seed)
}

func fakeUpper(prefix string, n int, seed int64) string {
	return prefix + randomString("ABCDEFGHIJKLMNOPQRSTUVWXYZ23456789", n, seed)
}

func fakeBase64(prefix string, n int, seed int64) string {
	return prefix + randomString("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/", n, seed)
}

func randomString(alphabet string, n int, seed int64) string {
	r := rand.New(rand.NewSource(seed))
	var b strings.Builder
	b.Grow(n)
	for range n {
		b.WriteByte(alphabet[r.Intn(len(alphabet))])
	}
	return b.String()
}
