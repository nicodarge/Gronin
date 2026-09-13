// Package ingresstest holds what the ingress's tests run against: a correct sender's
// signature, a raw connection that can stall or stop partway through a request, a
// listener that counts what the server actually reads, and a way to hold the record
// store's write lock from outside it. Imported by _test.go files only — the shipped
// binary linking any of it is not a thing anything here checks for, but nothing under
// runtime/ imports it outside a test (runtime/internal/ingress/imports_test.go).
package ingresstest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the pure-Go driver record.Store itself opens
)

// Sign returns the header value a correctly signing sender would send for body under
// secret: prefix followed by the hex-encoded HMAC-SHA256 of the exact bytes of body
// (specs/004-webhook/contracts/ingress.md, "The route").
func Sign(secret, prefix string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return prefix + hex.EncodeToString(mac.Sum(nil))
}

// Request builds the raw bytes of an HTTP/1.1 POST to /hooks/<source>, carrying header
// with value signature, so a test can hand the whole thing to a StallingConn and stop it
// at any byte.
func Request(source, header, signature string, body []byte) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "POST /hooks/%s HTTP/1.1\r\n", source)
	fmt.Fprintf(&buf, "Host: ingresstest\r\n")
	fmt.Fprintf(&buf, "%s: %s\r\n", header, signature)
	fmt.Fprintf(&buf, "Content-Length: %d\r\n", len(body))
	buf.WriteString("Connection: close\r\n")
	buf.WriteString("\r\n")
	buf.Write(body)
	return buf.Bytes()
}

// StallingConn is a raw TCP connection to the ingress that writes what it is given one
// byte at a time, so a test can stop after any prefix of the headers or the body and
// observe the bound that ends a sender that goes quiet.
type StallingConn struct {
	t    *testing.T
	conn net.Conn
}

// Dial opens a raw connection to addr. It is closed on test cleanup if the test never
// calls Stop itself.
func Dial(t *testing.T, addr string) *StallingConn {
	t.Helper()
	var dialer net.Dialer
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("dialing %s: %v", addr, err)
	}
	c := &StallingConn{t: t, conn: conn}
	t.Cleanup(c.Stop)
	return c
}

// Write sends the first n bytes of data, one byte at a time. n may be less than
// len(data) to stop partway through the headers or the body; the connection is left
// open, mid-request, for the bound under test to act on.
func (c *StallingConn) Write(data []byte, n int) {
	c.t.Helper()
	if n > len(data) {
		n = len(data)
	}
	for _, b := range data[:n] {
		if _, err := c.conn.Write([]byte{b}); err != nil {
			// The bound this type exists to test can itself close the connection
			// mid-write; that is a result, not a test failure.
			return
		}
	}
}

// Read waits up to timeout for whatever the server has answered so far.
func (c *StallingConn) Read(timeout time.Duration) []byte {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4096)
	n, err := c.conn.Read(buf)
	if n == 0 && err != nil {
		return nil
	}
	return buf[:n]
}

// Stop ends the connection without finishing whatever was being sent.
func (c *StallingConn) Stop() {
	_ = c.conn.Close()
}

// CountingListener wraps a net.Listener so a test can read how many bytes the server has
// consumed from each connection it accepted, oldest first — what shows a length bound is
// enforced before the rest of a body ever arrives, rather than after it does.
type CountingListener struct {
	net.Listener

	mu    sync.Mutex
	conns []*countingConn
}

// NewCountingListener wraps inner.
func NewCountingListener(inner net.Listener) *CountingListener {
	return &CountingListener{Listener: inner}
}

// Accept implements net.Listener.
func (l *CountingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	cc := &countingConn{Conn: conn}
	l.mu.Lock()
	l.conns = append(l.conns, cc)
	l.mu.Unlock()
	return cc, nil
}

// BytesRead returns how many bytes the server has read so far from each connection
// accepted so far, oldest first.
func (l *CountingListener) BytesRead() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]int64, len(l.conns))
	for i, c := range l.conns {
		out[i] = c.bytesRead()
	}
	return out
}

type countingConn struct {
	net.Conn

	mu sync.Mutex
	n  int64
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.mu.Lock()
	c.n += int64(n)
	c.mu.Unlock()
	return n, err
}

func (c *countingConn) bytesRead() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// HoldWrites takes the record store's write lock — SQLite's reserved lock under WAL,
// taken with BEGIN IMMEDIATE — from a connection of its own, opened directly against
// record.db under recordDir, and returns the func that releases it. A test uses it to
// assert that no answer reaches a sender while the write its answer depends on cannot
// land (specs/004-webhook/contracts/ingress.md, A1).
func HoldWrites(t *testing.T, recordDir string) (release func()) {
	t.Helper()
	if err := os.MkdirAll(recordDir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", recordDir, err)
	}
	dsn := "file:" + filepath.Join(recordDir, "record.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("opening the record to hold its write lock: %v", err)
	}
	// One connection, held for the whole test: a pool that handed the transaction's
	// connection back would let a later query on the same *sql.DB open a second one and
	// see no lock at all.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("taking a connection to hold the write lock: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("taking the write lock: %v", err)
	}
	return func() {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		_ = conn.Close()
		_ = db.Close()
	}
}
