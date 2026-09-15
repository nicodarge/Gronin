package ingress

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

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
// value in the same sense playbook.Check applies to a declared trigger value, or the
// SHA-256 digest of body's exact bytes when the source declares no pointer at all.
func Identity(source sources.Source, body []byte) (identity string, kind record.IdentityKind, outcome IdentityOutcome) {
	if source.Identity == "" {
		digest := sha256.Sum256(body)
		return hex.EncodeToString(digest[:]), record.IdentityDigest, IdentityFound
	}

	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	// A body that is not JSON decodes to a nil document, which atPointer resolves to
	// nothing — an absent identity (spec.md, *Edge Cases*), never a parse failure.
	_ = decoder.Decode(&decoded)

	value := atPointer(decoded, source.Identity)
	if value == nil {
		return "", "", IdentityMissing
	}
	text, single := singleValue(value)
	if !single {
		return "", "", IdentityNotSingle
	}
	return text, record.IdentityDeclared, IdentityFound
}

// atPointer resolves an RFC 6901 JSON Pointer against an already-decoded document — the
// same rule playbook.Extract applies to a declared value's own pointer. Duplicated rather
// than imported: the one in internal/playbook/values.go is unexported, and small enough
// that exporting it there for this one caller would cost more than it saves.
func atPointer(document any, pointer string) any {
	if pointer == "" {
		return document
	}
	current := document
	for _, segment := range strings.Split(pointer, "/")[1:] {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			value, found := node[segment]
			if !found {
				return nil
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil
			}
			current = node[index]
		default:
			return nil
		}
	}
	return current
}

// singleValue reads FR-316's single value out of a decoded JSON value: a string, a
// number kept as the literal text it was written as, or a boolean. Anything else — an
// object, an array, or nil, which stands for both a missing pointer and a JSON null — is
// not one.
func singleValue(raw any) (string, bool) {
	switch typed := raw.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}
