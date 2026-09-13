package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
)

// An update holds the index for as long as it takes, and a process stopped mid-update holds
// it until it is killed. A listing behind it says so within its bound instead of waiting with
// it: `list` on the collection's line, `show` as its non-zero exit. The lock is held here by
// a connection of the test's own, exclusively, which is what keeps a reader out.
func TestAListingDoesNotWaitForeverBehindAnUpdate(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "runbooks")
	writeDocuments(t, directory, threeRunbooks)
	deployment := newRetrievingDeployment(t, directory, oneRetrievalPlaybook)
	if got := bintest.Run(t, "collections", "rebuild", "runbooks", "--state-dir", deployment.state); got.ExitCode != 0 {
		t.Fatalf("gronin collections rebuild exited %d: %s", got.ExitCode, got.Stderr)
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(deployment.state, "index", "runbooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
		_ = db.Close()
	})

	const held = "the index is held by another connection"

	listing := bintest.Start(t, "collections", "list", "--state-dir", deployment.state)
	code, err := listing.Wait(30 * time.Second)
	if err != nil {
		t.Fatalf("gronin collections list was still waiting for the index after 30s: %v", err)
	}
	if code != 0 {
		t.Errorf("gronin collections list exited %d behind a held index: %s", code, listing.Stderr())
	}
	if line := listing.Expect(t, "runbooks", time.Second); !strings.Contains(line, "cannot be listed: "+held) {
		t.Errorf("the listing does not say the index is held: %q", line)
	}

	shown := bintest.Start(t, "collections", "show", "runbooks", "--state-dir", deployment.state)
	code, err = shown.Wait(30 * time.Second)
	if err != nil {
		t.Fatalf("gronin collections show was still waiting for the index after 30s: %v", err)
	}
	if code == 0 {
		t.Errorf("gronin collections show exited 0 behind a held index")
	}
	if !strings.Contains(shown.Stderr(), held) {
		t.Errorf("gronin collections show does not say the index is held: %q", shown.Stderr())
	}
}
