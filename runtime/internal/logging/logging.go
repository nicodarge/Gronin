// Package logging is the deployment's log, with the redactor at its write boundary.
//
// It is the other half of FR-029, and it was deferred once already: the record's boundary
// applied the redactor and the log did not exist at all, so anything a stage wanted to
// say went nowhere. Two reviews in a row found the same swallowed error as a consequence
// — a write that failed and told nobody — which is what a runtime with no log looks like
// from the outside.
//
// Redaction happens here rather than in the callers for the same reason it does in the
// store: a caller that has to remember will forget, and by then the line is in the
// journal and on its way to log aggregation.
package logging

import (
	"context"
	"io"
	"log/slog"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// New returns a logger whose every attribute and message passes the redactor.
func New(to io.Writer, redactor *record.Redactor, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(to, &slog.HandlerOptions{Level: level})
	return slog.New(&redacting{inner: handler, redactor: redactor})
}

// Discard is a logger that writes nothing, for a caller that has not configured one.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

type redacting struct {
	inner    slog.Handler
	redactor *record.Redactor
}

func (h *redacting) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redacting) Handle(ctx context.Context, entry slog.Record) error {
	// A new record rather than a mutation: slog.Record shares its backing array with
	// clones, and rewriting attributes in place would reach records already emitted.
	redacted := slog.NewRecord(entry.Time, entry.Level, h.redactor.Redact(entry.Message), entry.PC)
	entry.Attrs(func(attr slog.Attr) bool {
		redacted.AddAttrs(h.redactAttr(attr))
		return true
	})
	return h.inner.Handle(ctx, redacted)
}

func (h *redacting) redactAttr(attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, h.redactor.Redact(value.String()))
	case slog.KindGroup:
		group := value.Group()
		out := make([]any, 0, len(group))
		for _, inner := range group {
			out = append(out, h.redactAttr(inner))
		}
		return slog.Group(attr.Key, out...)
	case slog.KindAny:
		// An error, a fmt.Stringer, anything printed later: rendered here so the
		// redactor sees the text the handler would otherwise format past it.
		return slog.String(attr.Key, h.redactor.Redact(value.String()))
	default:
		return attr
	}
}

func (h *redacting) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		redacted = append(redacted, h.redactAttr(attr))
	}
	return &redacting{inner: h.inner.WithAttrs(redacted), redactor: h.redactor}
}

func (h *redacting) WithGroup(name string) slog.Handler {
	return &redacting{inner: h.inner.WithGroup(name), redactor: h.redactor}
}
