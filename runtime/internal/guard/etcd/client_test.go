package etcd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/testsecret"
)

func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func request(name string) guard.AcquireRequest {
	return guard.AcquireRequest{
		Name:    name,
		Holder:  guard.Holder{Host: "host.example.com", Instance: "instance", RunID: "run-" + name},
		Expiry:  guardtest.MinimumTTL,
		Trigger: guard.TriggerRef{Kind: guard.KindManual},
	}
}

// T042: the two credential forms contracts/cli.md names, each against a server that
// requires it. The certificates are generated in the test and none is committed.
func TestCredentialsReachTheServer(t *testing.T) {
	t.Run("a username and password", func(t *testing.T) {
		server := guardtest.StartAuthenticatedServer(t)

		client, err := NewClient(guard.EtcdConfig{
			Endpoints: []string{server.Endpoint()},
			Username:  guardtest.Username,
			Password:  guardtest.Password,
		})
		if err != nil {
			t.Fatalf("a client carrying the credentials: %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })

		held, err := New(client, Options{Prefix: "gronin/"}).Acquire(bounded(t), request("auth-ok"))
		if err != nil {
			t.Fatalf("taking a claim with the configured credentials: %v", err)
		}
		if err := held.Release(bounded(t)); err != nil {
			t.Fatalf("releasing: %v", err)
		}

		// The other half: the server, not the adapter, is what refuses. A client with no
		// credentials reaches the same socket and is turned away.
		anonymous := New(guardtest.NewClient(t, server.Endpoint()), Options{Prefix: "gronin/"})
		if _, err := anonymous.Acquire(bounded(t), request("auth-anonymous")); err == nil {
			t.Fatal("a client with no credentials took a claim on an authenticated server")
		}
	})

	t.Run("a TLS client certificate", func(t *testing.T) {
		server, files := guardtest.StartTLSServer(t)

		client, err := NewClient(guard.EtcdConfig{Endpoints: []string{server.Endpoint()}, TLS: &files})
		if err != nil {
			t.Fatalf("a client presenting its certificate: %v", err)
		}
		t.Cleanup(func() { _ = client.Close() })

		held, err := New(client, Options{Prefix: "gronin/"}).Acquire(bounded(t), request("tls-ok"))
		if err != nil {
			t.Fatalf("taking a claim with the configured certificate: %v", err)
		}
		if err := held.Release(bounded(t)); err != nil {
			t.Fatalf("releasing: %v", err)
		}

		// The certificate is what gets in: a client presenting none is refused in the
		// handshake.
		without, err := NewClient(guard.EtcdConfig{
			Endpoints: []string{server.Endpoint()},
			TLS:       &guard.TLSFiles{CA: files.CA},
		})
		if err != nil {
			t.Fatalf("building a client with no certificate: %v", err)
		}
		t.Cleanup(func() { _ = without.Close() })
		if _, err := New(without, Options{Prefix: "gronin/"}).
			Acquire(bounded(t), request("tls-none")); err == nil {
			t.Fatal("a client presenting no certificate took a claim")
		}
	})
}

// A password marked secret reaches neither the record nor the log: the redactor the
// deployment already seeds from its secret configuration keys is what covers it, and this
// is where that is checked rather than assumed.
func TestCredentialsDoNotReachTheRecord(t *testing.T) {
	store, err := record.Open(t.Context(), filepath.Join(t.TempDir(), "record"),
		record.NewRedactor([]string{testsecret.Value}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.RecordRefusal(t.Context(), record.Refusal{
		PlaybookName: "drift-check",
		TriggerKind:  record.TriggerSchedule,
		Mechanism:    record.MechanismBackendUnavailable,
		Detail: "etcd at unix:///run/gronin/etcd.sock refused: authentication failed for " +
			testsecret.Value,
		RefusedAt: time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	refusals, err := store.ListRefusals(t.Context(), 10)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %v, err = %v", refusals, err)
	}
	if strings.Contains(refusals[0].Detail, testsecret.Value) {
		t.Fatalf("the password reached the record: %q", refusals[0].Detail)
	}
	if !strings.Contains(refusals[0].Detail, record.Placeholder) {
		t.Fatalf("the detail lost the refusal along with the secret: %q", refusals[0].Detail)
	}
}
