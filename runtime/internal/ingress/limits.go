package ingress

import (
	"net/http"
	"time"
)

// The ingress's bounds, decided 2026-09-10 with their reasons (plan.md, *Constraints*).
// US4 (T091) adds the body and in-progress bounds to the same options; nothing here
// enforces them yet.
const (
	// DefaultHeaderTimeout is what serve already gives the operator API.
	DefaultHeaderTimeout = 10 * time.Second
	// DefaultRequestTimeout covers a full-size body from a slow sender, counted from
	// the start of the request, headers included.
	DefaultRequestTimeout = 15 * time.Second
	// DefaultDurableStep is the handler's own timer around the acceptance, and the
	// record store's own busy timeout — deliberately the same number, so a change to
	// one is a change to both.
	DefaultDurableStep = 5 * time.Second
	// DefaultAnswerTimeout is when net/http gives up writing the answer, counted from
	// the moment headers are read: it has to cover the rest of the body and the
	// durable step before the answer is written (research.md §3, FR-313).
	DefaultAnswerTimeout = 25 * time.Second
	// DefaultMaxHeaderBytes is far below the standard library's own 1 MiB default: the
	// only header this listener reads is a signature.
	DefaultMaxHeaderBytes = 16 * 1024
)

// Options are the ingress's declared bounds, each with the plan's default.
type Options struct {
	// HeaderTimeout bounds the time to read the request's headers.
	HeaderTimeout time.Duration
	// RequestTimeout bounds the time to read the whole request, headers included.
	RequestTimeout time.Duration
	// AnswerTimeout bounds the time net/http allows to write the answer, from the
	// moment the headers were read (research.md §3).
	AnswerTimeout time.Duration
	// MaxHeaderBytes bounds the total size of the request's header fields.
	MaxHeaderBytes int
	// DurableStep bounds the handler's own wait for the acceptance to complete
	// (contracts/ingress.md, step 7).
	DurableStep time.Duration
}

// DefaultOptions is the ingress's bounds as the plan decided them.
func DefaultOptions() Options {
	return Options{
		HeaderTimeout:  DefaultHeaderTimeout,
		RequestTimeout: DefaultRequestTimeout,
		AnswerTimeout:  DefaultAnswerTimeout,
		MaxHeaderBytes: DefaultMaxHeaderBytes,
		DurableStep:    DefaultDurableStep,
	}
}

// NewServer builds the http.Server the ingress listens with, its bounds taken from opts
// rather than the standard library's own defaults.
func NewServer(addr string, handler http.Handler, opts Options) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: opts.HeaderTimeout,
		ReadTimeout:       opts.RequestTimeout,
		WriteTimeout:      opts.AnswerTimeout,
		MaxHeaderBytes:    opts.MaxHeaderBytes,
	}
}
