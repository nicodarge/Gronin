package guardtest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
)

// What makes the embedded server hermetic is that it listens on nothing but sockets the
// test created. Read from the server's own listeners, not from /proc/net/tcp: the whole
// suite shares one network namespace, and other packages' loopback servers would show
// there.
func TestTheEmbeddedServerListensOnUnixSocketsOnly(t *testing.T) {
	server := guardtest.StartServer(t)

	listeners := server.Listeners()
	// A client listener and a peer listener. An empty list passes every check below.
	if len(listeners) < 2 {
		t.Fatalf("the server reports %d listeners; it opens a client and a peer one", len(listeners))
	}
	for _, addr := range listeners {
		if addr.Network() != "unix" {
			t.Errorf("the embedded server listens on %s %s", addr.Network(), addr.String())
		}
	}
}

// The proxy is what the contract holds a backend with. A hold that closed the connection
// would be a sever, and would let an unbounded call pass; one that forwarded anyway would
// hold nothing.
func TestTheProxyHoldsAndSevers(t *testing.T) {
	server := guardtest.StartServer(t)
	proxy := server.Proxy(t)
	client := guardtest.NewClient(t, proxy.Endpoint())

	put := func(within time.Duration) (time.Duration, error) {
		ctx, cancel := context.WithTimeout(t.Context(), within)
		defer cancel()
		began := time.Now()
		_, err := client.Put(ctx, "guardtest/key", "value")
		return time.Since(began), err
	}

	if _, err := put(10 * time.Second); err != nil {
		t.Fatalf("a put through the proxy failed: %v", err)
	}

	proxy.Hold()
	took, err := put(500 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || took < 450*time.Millisecond {
		t.Fatalf("a put through a held proxy returned after %s with %v; it should wait out its deadline", took, err)
	}

	proxy.Resume()
	if _, err := put(10 * time.Second); err != nil {
		t.Fatalf("a put after the hold was lifted failed: %v", err)
	}

	proxy.Sever()
	if _, err := put(500 * time.Millisecond); err == nil {
		t.Fatal("a put through a severed proxy succeeded")
	}
	proxy.Resume()
	if _, err := put(10 * time.Second); err != nil {
		t.Fatalf("a put after the sever was lifted failed: %v", err)
	}
}
