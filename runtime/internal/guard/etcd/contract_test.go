package etcd

import (
	"context"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
)

// The contract, against a real etcd server embedded in this process and reached over a
// unix socket the test created. This is what makes the fake honest: a clause the fake
// satisfies and etcd does not fails here, inside the gate, rather than in a deployment.
//
// Each node has a proxy of its own, which is what holds the backend for C4 — a severed
// connection fails fast and would let an unbounded call pass.
func TestEtcdContract(t *testing.T) {
	server := guardtest.StartServer(t)
	runtime := guardtest.NewClock(time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC))
	raw := guardtest.NewClient(t, server.Endpoint())

	guardtest.Contract(t, guardtest.Subject{
		New: func(t *testing.T, seam guard.Seam) guardtest.Node {
			t.Helper()
			proxy := server.Proxy(t)
			client := guardtest.NewClient(t, proxy.Endpoint())
			return guardtest.Node{
				Coordinator: New(client, Options{Prefix: "gronin/", Clock: runtime, Seam: seam}),
				Hold:        proxy.Hold,
			}
		},
		Seams: true,
		NewWatching: func(t *testing.T, watching func(name string)) guardtest.Node {
			t.Helper()
			proxy := server.Proxy(t)
			client := guardtest.NewClient(t, proxy.Endpoint())
			return guardtest.Node{
				Coordinator: New(client, Options{Prefix: "gronin/", Clock: runtime, Watching: watching}),
				Hold:        proxy.Hold,
			}
		},
		Unreachable: func(t *testing.T) guard.Coordinator {
			t.Helper()
			return New(guardtest.NewClient(t, server.Unreachable()), Options{Prefix: "gronin/", Clock: runtime})
		},
		// Revoked out of band, which is what the server does when a lease expires. Waited
		// out instead, every clause needing a lapsed claim would cost the whole expiry.
		Lapse: func(t *testing.T, held guard.Claim) {
			t.Helper()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := raw.Revoke(ctx, held.(*claim).lease); err != nil {
				t.Fatalf("revoking the lease of a claim: %v", err)
			}
		},
		Elapse:      func(_ *testing.T, d time.Duration) { time.Sleep(d) },
		StepRuntime: runtime.Advance,
		// The server grants at least a second and a half, rounded up, whatever it is
		// asked, so the refusing half of C12 cannot be reached through it. TestGrantedExpiry
		// is where the adapter's own comparison is shown able to fail.
		LongGrant: func(t *testing.T) (guard.Coordinator, time.Duration, time.Duration) {
			t.Helper()
			client := guardtest.NewClient(t, server.Endpoint())
			return New(client, Options{Prefix: "gronin/", Clock: runtime}), time.Second, guardtest.MinimumTTL
		},
		Expiry: guardtest.MinimumTTL,
		Slack:  2 * time.Second,
	})
}

// The adapter must not depend on a server that grants more than it is asked for, which is
// the only thing the embedded one can do to it.
func TestTheRawClientIsTheServers(t *testing.T) {
	server := guardtest.StartServer(t)
	client := guardtest.NewClient(t, server.Endpoint())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lease, err := client.Grant(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(guardtest.MinimumTTL / time.Second); lease.TTL != want {
		t.Fatalf("asked for a lease of 1s and got %ds; the suite assumes %ds", lease.TTL, want)
	}
}
