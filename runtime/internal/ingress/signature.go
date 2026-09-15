package ingress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/nicodarge/Gronin/runtime/internal/sources"
)

// VerifySignature applies contracts/ingress.md's Step 5: exactly one MAC and one
// constant-time comparison, whatever is wrong with the request. secret is the source's
// resolved secret, and body the exact bytes received.
func VerifySignature(header http.Header, source sources.Source, secret string, body []byte) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(expected, offeredSignature(header, source))
}

// offeredSignature reads the offered MAC out of header at source's declared signature
// header, decoded only when the header appears exactly once, starts with the declared
// prefix, and the remainder decodes to exactly 32 bytes — an HMAC-SHA256 is always that
// long, so the length is not a secret worth hiding (research.md §4). Otherwise it returns
// a 32-byte buffer no MAC equals, so exactly one comparison always runs and the answer to
// every failure here is the same (contracts/ingress.md, Step 5).
func offeredSignature(header http.Header, source sources.Source) []byte {
	buffer := make([]byte, sha256.Size)

	values := header.Values(source.SignatureHeader)
	if len(values) != 1 {
		return buffer
	}
	value, ok := strings.CutPrefix(values[0], source.SignaturePrefix)
	if !ok {
		return buffer
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return buffer
	}
	return decoded
}
