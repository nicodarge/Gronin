package ingresstest_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nicodarge/Gronin/runtime/internal/ingress/ingresstest"
)

func TestSignIsHexHMACWithThePrefix(t *testing.T) {
	got := ingresstest.Sign("s3cret", "sha256=", []byte(`{"a":1}`))

	if !strings.HasPrefix(got, "sha256=") {
		t.Fatalf("signature %q does not start with the declared prefix", got)
	}
	hexPart := strings.TrimPrefix(got, "sha256=")
	raw, err := hex.DecodeString(hexPart)
	if err != nil {
		t.Fatalf("the remainder is not hexadecimal: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("decoded to %d bytes, want 32 (HMAC-SHA256)", len(raw))
	}
	// Signing the same body under the same secret is deterministic, and changing
	// either the body or the secret changes the signature.
	if again := ingresstest.Sign("s3cret", "sha256=", []byte(`{"a":1}`)); again != got {
		t.Fatalf("signing the same body twice gave %q then %q", got, again)
	}
	if other := ingresstest.Sign("s3cret", "sha256=", []byte(`{"a":2}`)); other == got {
		t.Fatal("a different body produced the same signature")
	}
	if other := ingresstest.Sign("different", "sha256=", []byte(`{"a":1}`)); other == got {
		t.Fatal("a different secret produced the same signature")
	}
}

func TestStallingConnStopsPartway(t *testing.T) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	received := make(chan int, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			received <- -1
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		total := 0
		for {
			m, err := conn.Read(buf)
			total += m
			if err != nil {
				break
			}
		}
		received <- total
	}()

	request := ingresstest.Request("alerts", "X-Signature", "sha256=deadbeef", []byte(`{"x":1}`))
	sender := ingresstest.Dial(t, listener.Addr().String())
	stopAt := len(request) - 3 // stop before the request finishes
	sender.Write(request, stopAt)
	sender.Stop()

	got := <-received
	if got != stopAt {
		t.Fatalf("server read %d bytes, want exactly the %d sent before Stop", got, stopAt)
	}
}

func TestCountingListenerCountsPerConnection(t *testing.T) {
	var listenConfig net.ListenConfig
	inner, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := ingresstest.NewCountingListener(inner)
	t.Cleanup(func() { _ = listener.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 3)
		_, _ = io.ReadFull(conn, buf)
	}()

	var dialer net.Dialer
	conn, err := dialer.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	<-done

	counts := listener.BytesRead()
	if len(counts) != 1 {
		t.Fatalf("%d connections counted, want 1", len(counts))
	}
	if counts[0] != 3 {
		t.Fatalf("bytes read = %d, want 3", counts[0])
	}
}

func TestHoldWritesBlocksAnotherWriter(t *testing.T) {
	dir := t.TempDir()
	release := ingresstest.HoldWrites(t, dir)

	dsn := "file:" + filepath.Join(dir, "record.db") + "?_pragma=busy_timeout(200)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	blocked := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(context.Background(), "BEGIN IMMEDIATE")
		blocked <- err
	}()

	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("a second writer began while HoldWrites held the write lock")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the second writer neither began nor was refused")
	}

	release()

	done := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(context.Background(), "BEGIN IMMEDIATE; ROLLBACK;")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a writer still could not proceed after release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a writer never proceeded after release")
	}
}
