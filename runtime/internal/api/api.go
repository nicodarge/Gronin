// Package api serves the local HTTP API the operator commands talk to.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// ErrRemoteWithoutCredential refuses to bind anywhere but loopback until a credential is
// configured (FR-036).
//
// An unauthenticated listener that can invoke playbooks is a remote execution surface,
// and the default must not be one step away from being one. The refusal is at bind time
// rather than at request time so a deployment cannot be one restart away from exposure.
var ErrRemoteWithoutCredential = errors.New(
	"binding the API anywhere but loopback requires a credential to be configured first")

// Reader is what the API needs from the record. An interface rather than the store
// itself, so the surface this exposes is visible in one place.
type Reader interface {
	ListRuns(ctx context.Context, limit int) ([]record.Run, error)
	GetRun(ctx context.Context, id string) (record.Run, error)
	GatheredInputs(ctx context.Context, id string) ([]record.GatheredInput, error)
	SinkOutcomes(ctx context.Context, id string) ([]record.SinkOutcome, error)
	RefusedActions(ctx context.Context, id string) ([]record.RefusedAction, error)
	ToolCalls(ctx context.Context, id string) ([]record.ToolCall, error)
}

// Server is the local API.
type Server struct {
	reader Reader
	token  string
}

// New returns a server. token is empty for a loopback-only deployment.
func New(reader Reader, token string) *Server {
	return &Server{reader: reader, token: token}
}

// CheckAddress applies FR-036 before anything listens.
func CheckAddress(address, token string) error {
	if address == "" {
		return errors.New("no address to bind")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%q is not an address to bind: %w", address, err)
	}
	if isLoopback(host) {
		return nil
	}
	if token == "" {
		return fmt.Errorf("%w; %q is not loopback", ErrRemoteWithoutCredential, host)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	// An unspecified address is every interface, which is the case this exists to stop.
	if host == "0.0.0.0" || host == "::" {
		return false
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

// Handler is the API's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runs", s.listRuns)
	mux.HandleFunc("GET /runs/{id}", s.showRun)
	return s.authenticated(mux)
}

// authenticated is a no-op on a loopback-only deployment and a bearer check otherwise.
// The token is compared in full rather than by prefix, and a missing one is refused
// rather than treated as absent-and-therefore-fine.
func (s *Server) authenticated(inner http.Handler) http.Handler {
	if s.token == "" {
		return inner
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offered := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if offered == "" || offered != s.token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.reader.ListRuns(r.Context(), 50)
	if err != nil {
		http.Error(w, "the record could not be read", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"runs": summaries(runs)})
}

func (s *Server) showRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	one, err := s.reader.GetRun(r.Context(), id)
	if errors.Is(err, record.ErrNotFound) {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "the record could not be read", http.StatusInternalServerError)
		return
	}

	inputs, _ := s.reader.GatheredInputs(r.Context(), id)
	outcomes, _ := s.reader.SinkOutcomes(r.Context(), id)
	refused, _ := s.reader.RefusedActions(r.Context(), id)
	calls, _ := s.reader.ToolCalls(r.Context(), id)

	writeJSON(w, map[string]any{
		"run":             summary(one),
		"gathered_inputs": inputs,
		"tool_calls":      calls,
		"refused_actions": refused,
		"sink_outcomes":   outcomes,
	})
}

func summaries(runs []record.Run) []map[string]any {
	out := make([]map[string]any, 0, len(runs))
	for _, one := range runs {
		out = append(out, summary(one))
	}
	return out
}

func summary(one record.Run) map[string]any {
	held := map[string]any{
		"id":         one.ID,
		"playbook":   one.PlaybookName,
		"status":     string(one.Status),
		"trigger":    string(one.TriggerKind),
		"started_at": one.StartedAt.UTC().Format(time.RFC3339Nano),
		"cost_usd":   one.CostUSD,
		"tokens":     one.Tokens,
	}
	if !one.EndedAt.IsZero() {
		held["ended_at"] = one.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	for key, value := range map[string]string{
		"parent_run_id": one.ParentRunID, "error": one.Error,
		"credential_source": one.CredentialSource, "report_ref": one.ReportRef,
		"prompt_ref": one.PromptRef, "resolved_playbook_ref": one.ResolvedPlaybookRef,
	} {
		if value != "" {
			held[key] = value
		}
	}
	return held
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
