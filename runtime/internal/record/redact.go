package record

import (
	"sort"
	"strings"
)

// Placeholder is what a redacted value is replaced by. It is deliberately visible: a
// record that silently dropped a value would read as if the value had never been there,
// and someone would eventually go looking for why the field is empty.
const Placeholder = "[redacted]"

// Redactor removes the deployment's configured secret values from anything on its way
// into the record store or the log.
//
// It is applied at the write boundary of both, never by the callers. A caller that has
// to remember to redact will eventually forget, and by then the value is durable.
//
// What it cannot do is stated rather than papered over: it is seeded from what the
// deployment knows about itself, so a secret appearing for the first time inside a
// gathered input is not in it. That is why gather steps must not print secrets — the
// same rule the constitution states for command lines.
type Redactor struct {
	secrets []string
}

// NewRedactor seeds a redactor. Empty values are ignored; replacing the empty string
// would rewrite every position in every record.
func NewRedactor(secrets []string) *Redactor {
	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			kept = append(kept, secret)
		}
	}
	// Longest first. A deployment can hold one secret that contains another — a URL and
	// the token inside it — and replacing the shorter one first leaves the rest of the
	// longer one in the record, which is the half that identifies it.
	sort.Slice(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	return &Redactor{secrets: kept}
}

// Redact returns text with every configured secret value replaced.
func (r *Redactor) Redact(text string) string {
	if r == nil {
		return text
	}
	for _, secret := range r.secrets {
		text = strings.ReplaceAll(text, secret, Placeholder)
	}
	return text
}

// RedactBytes is Redact for a blob on its way to disk.
func (r *Redactor) RedactBytes(data []byte) []byte {
	if r == nil || len(r.secrets) == 0 {
		return data
	}
	return []byte(r.Redact(string(data)))
}
