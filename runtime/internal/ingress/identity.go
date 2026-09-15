package ingress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/sources"
)

// IdentityOutcome is what reading a delivery's identity found (FR-316).
type IdentityOutcome int

// Every outcome Identity can report.
const (
	// IdentityFound is a usable identity: the declared value, or the digest.
	IdentityFound IdentityOutcome = iota
	// IdentityMissing is a declared pointer that resolved to nothing.
	IdentityMissing
	// IdentityNotSingle is a declared pointer that resolved to an object, an array or
	// a JSON null.
	IdentityNotSingle
)

// Identity resolves FR-316: the value at source's declared pointer, taken as a single
// value through playbook.AtPointer and playbook.SingleValue — the same functions
// playbook.Extract and playbook.Check apply to a declared trigger value (plan.md, *Path
// Conventions*) — or the SHA-256 digest of body's exact bytes when the source declares no
// pointer at all.
func Identity(source sources.Source, body []byte) (identity string, kind record.IdentityKind, outcome IdentityOutcome) {
	if source.Identity == "" {
		digest := sha256.Sum256(body)
		return hex.EncodeToString(digest[:]), record.IdentityDigest, IdentityFound
	}

	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	// A body that is not JSON decodes to a nil document, which AtPointer resolves to
	// nothing — an absent identity (spec.md, *Edge Cases*), never a parse failure.
	_ = decoder.Decode(&decoded)

	value := playbook.AtPointer(decoded, source.Identity)
	if value == nil {
		return "", "", IdentityMissing
	}
	text, single := playbook.SingleValue(value)
	if !single {
		return "", "", IdentityNotSingle
	}
	return text, record.IdentityDeclared, IdentityFound
}
