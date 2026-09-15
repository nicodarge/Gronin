package ingress

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/sources"
)

// routePrefix is the one path the ingress serves (contracts/ingress.md, "The route").
const routePrefix = "/hooks/"

// Handler is the ingress: contracts/ingress.md's one route, and nothing else. Every
// field is required except Alive, which stands in for what cmd/gronin (T054) wires the
// rest of the deployment through.
type Handler struct {
	Store   *record.Store
	Sources *sources.Catalog
	// Secret resolves a configured source's secret; only ever called for a name Sources
	// holds. Kept separate from Sources itself, which never resolves one (FR-306).
	Secret func(sourceName string) (string, error)
	Loaded playbook.Loaded
	// Dispatcher is the hand-off's reach into the guard and the executor, behind the
	// interface this package declares (plan.md, *Project Structure*).
	Dispatcher Dispatcher
	// Clock is the runtime's own, for every timestamp this handler records or compares
	// (FR-320) — the guard's Clock so a test can inject the same fake it injects there.
	Clock guard.Clock
	// Instance is this process's own, recorded on every delivery it accepts.
	Instance string
	Options  Options

	// Alive is the liveness probe Accept needs to reconcile a delivery whose accepting
	// process may be gone (T049). Left nil answers every such question "still alive",
	// which is safe on its own — it only ever widens what a retry could recover, never
	// lets a live process's delivery be double-run — until cmd/gronin (T054) wires the
	// guard's own instance check through it.
	Alive record.Alive
}

// acceptance is what Accept returns, threaded through a channel so the handler can
// answer as soon as it completes or the durable step's bound passes, whichever is first
// (contracts/ingress.md, step 7; A1, A2, A3).
type acceptance struct {
	delivery record.Delivery
	isNew    bool
	err      error
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sourceName, ok := strings.CutPrefix(r.URL.Path, routePrefix)
	if !ok || sourceName == "" || strings.Contains(sourceName, "/") {
		writeNotFound(w)
		return
	}
	if r.Method != http.MethodPost {
		// contracts/ingress.md: any other method on this path is 404, not 405, which
		// would confirm the path exists.
		writeNotFound(w)
		return
	}

	source, found := h.Sources.Get(sourceName)
	if !found {
		// FR-309's constant-work answer for an unconfigured name is T076's; this is
		// still the right answer, just not yet the same amount of work to reach it.
		writeRefused(w)
		return
	}
	secret, err := h.Secret(sourceName)
	if err != nil {
		writeRefused(w)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeRefused(w)
		return
	}

	if !VerifySignature(r.Header, source, secret, body) {
		writeRefused(w)
		return
	}

	now := h.Clock.Wall()
	peer := peerOf(r)

	identity, kind, outcome := Identity(source, body)
	if outcome != IdentityFound {
		reason := record.ReasonIdentityAbsent
		if outcome == IdentityNotSingle {
			reason = record.ReasonIdentityNotSingle
		}
		if _, err := h.Store.AddDeliveryRefusal(r.Context(), record.DeliveryRefusal{
			Source: sourceName, Reason: reason, ReceivedAt: now, Peer: peer,
		}, body); err != nil {
			writeNotAccepted(w)
			return
		}
		writeBadRequest(w, reason)
		return
	}

	h.accept(w, r, sourceName, identity, kind, body, peer, now, source)
}

// accept runs step 7 through 9: the acceptance, bounded by the durable step and answered
// as soon as it completes or the bound passes, whichever is first (A1, A2), and the
// hand-off started by the acceptance completing rather than by the answer being written
// (A3) — so a write that lands after its own 503 still runs.
func (h *Handler) accept(
	w http.ResponseWriter, r *http.Request, sourceName, identity string, kind record.IdentityKind,
	body []byte, peer string, now time.Time, source sources.Source,
) {
	bound := boundPlaybookNames(h.Loaded, sourceName)

	// Detached from the request (research.md §3): a client disconnecting, or the
	// handler giving up at the durable step's bound, must not abort a write that may
	// already be about to commit.
	acceptCtx := context.WithoutCancel(r.Context())

	done := make(chan acceptance, 1)
	go func() { //nolint:gosec // deliberately detached (research.md §3): a late hand-off must outlive this request
		delivery, isNew, err := record.Accept(acceptCtx, h.Store, record.AcceptParams{
			Source: sourceName, Identity: identity, IdentityKind: kind, Body: body,
			Peer: peer, ReceivedAt: now, Instance: h.Instance, ReplayWindow: source.ReplayWindow,
			Playbooks: bound,
		}, h.Alive)
		done <- acceptance{delivery: delivery, isNew: isNew, err: err}
		if err == nil && isNew {
			_ = HandOff(context.Background(), h.Store, h.Dispatcher, h.Loaded, delivery, h.Clock.Wall)
		}
	}()

	timer := time.NewTimer(h.durableStep())
	defer timer.Stop()

	select {
	case result := <-done:
		if result.err != nil {
			writeNotAccepted(w)
			return
		}
		writeAccepted(w)
	case <-timer.C:
		writeNotAccepted(w)
	}
}

func (h *Handler) durableStep() time.Duration {
	if h.Options.DurableStep > 0 {
		return h.Options.DurableStep
	}
	return DefaultDurableStep
}

// boundPlaybookNames is every loaded playbook bound to sourceName (FR-321): nothing in
// the body, the headers or the query selects among them.
func boundPlaybookNames(loaded playbook.Loaded, sourceName string) []string {
	var names []string
	for _, book := range loaded.Playbooks {
		if book.Trigger.Type == "webhook" && book.Trigger.Source == sourceName {
			names = append(names, book.Name)
		}
	}
	return names
}

// peerOf is the connection's own address, never a forwarded one (FR-334): T072 is what
// adds the header-spoofing corpus this must keep refusing.
func peerOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("not found"))
}

func writeRefused(w http.ResponseWriter) {
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("refused"))
}

func writeBadRequest(w http.ResponseWriter, reason record.DeliveryRefusalReason) {
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte("refused: " + string(reason)))
}

func writeAccepted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("accepted"))
}

func writeNotAccepted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("not accepted; retry"))
}
