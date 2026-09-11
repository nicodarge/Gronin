package record_test

import (
	"bytes"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/testsecret"
)

// rawDB opens the record's database file beside the store, the way a second process
// would, for what the store's own API cannot write or does not show.
func rawDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "record.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A deployment upgrading to the guard already has a record, written under the runtime
// core's schema alone. The migration is a new file rather than an edit of the first one
// because an edited migration is skipped wherever it already ran — so what is asserted
// is that such a store opens, gains the guard's tables, and reads its old runs back.
func TestAStoreFromTheRuntimeCoreMigrates(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	initial, err := os.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	for _, statement := range []string{
		string(initial),
		`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations VALUES ('0001_initial.sql', '2026-09-01T00:00:00Z')`,
		`INSERT INTO runs (id, playbook_name, trigger_kind, status, started_at, ended_at)
		 VALUES ('20260901T000000Z-000000000001', 'drift-check', 'schedule', 'succeeded',
		         '2026-09-01T00:00:00Z', '2026-09-01T00:01:00Z')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := record.Open(ctx, dir, nil)
	if err != nil {
		t.Fatalf("a store from the runtime core did not open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	old, err := store.GetRun(ctx, "20260901T000000Z-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != record.StatusSucceeded || old.PlaybookName != "drift-check" {
		t.Fatalf("the existing run read back as %+v", old)
	}
	if old.WaitingTriggerID != "" || old.WaitedMS != 0 || old.ClaimReach != "" || old.ClaimToken != 0 {
		t.Fatalf("a run recorded before the guard reads back with guard fields set: %+v", old)
	}
	// The new tables are there, which is what the guard writes next.
	if err := store.RecordRefusal(ctx, record.Refusal{
		PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
		Mechanism: record.MechanismClaimHeld, Detail: "held", RefusedAt: time.Now(),
	}); err != nil {
		t.Fatalf("the migrated store cannot hold a refusal: %v", err)
	}
}

func TestARefusalRoundTripsMostRecentFirst(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Half a second apart within one second: the text of the later one is the shorter
	// in RFC 3339 with trailing zeros dropped, and sorted as text it would come second.
	base := time.Date(2026, 9, 10, 6, 0, 5, 0, time.FixedZone("CEST", 2*60*60))
	due := time.Date(2026, 9, 10, 4, 0, 0, 0, time.UTC)
	first := record.Refusal{
		ID: "refusal-a", PlaybookName: "doc-check", TriggerKind: record.TriggerSchedule,
		DueAt: due, Mechanism: record.MechanismTickAlreadyRan,
		Detail: "tick 04:00:00Z ran on host-b.example.com", RefusedAt: base,
	}
	second := record.Refusal{
		ID: "refusal-b", PlaybookName: "doc-check", TriggerKind: record.TriggerManual,
		WaitingTriggerID: "waiting-1", Mechanism: record.MechanismWaitExpired,
		Detail: "waited 30m", RefusedAt: base.Add(500 * time.Millisecond),
	}
	for _, refusal := range []record.Refusal{first, second} {
		if err := store.RecordRefusal(ctx, refusal); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.ListRefusals(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "refusal-b" || got[1].ID != "refusal-a" {
		t.Fatalf("listed %+v, want refusal-b then refusal-a", got)
	}

	read := got[1]
	if read.PlaybookName != first.PlaybookName || read.TriggerKind != first.TriggerKind ||
		read.Mechanism != first.Mechanism || read.Detail != first.Detail {
		t.Fatalf("read back as %+v, wrote %+v", read, first)
	}
	if !read.DueAt.Equal(due) || read.DueAt.Location() != time.UTC {
		t.Fatalf("due_at read back as %v, wrote %v", read.DueAt, due)
	}
	if !read.RefusedAt.Equal(base) || read.RefusedAt.Location() != time.UTC {
		t.Fatalf("refused_at read back as %v, wrote %v in UTC", read.RefusedAt, base)
	}
	if got[0].WaitingTriggerID != "waiting-1" || !got[0].DueAt.IsZero() {
		t.Fatalf("the manual refusal read back as %+v", got[0])
	}

	// Stored in UTC, not merely read back as UTC.
	var stored string
	if err := rawDB(t, dir).QueryRowContext(ctx,
		`SELECT refused_at FROM refusals WHERE id = 'refusal-a'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stored, "Z") || !strings.HasPrefix(stored, "2026-09-10T04:00:05") {
		t.Fatalf("refused_at is stored as %q, not in UTC", stored)
	}
}

func TestALastTickRoundTripsAndIsReplaced(t *testing.T) {
	ctx := t.Context()
	store, err := record.Open(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, found, err := store.LastTick(ctx, "doc-check"); err != nil || found {
		t.Fatalf("a playbook with no tick recorded read back found=%v err=%v", found, err)
	}

	first := record.Tick{
		DueAt: time.Date(2026, 9, 10, 6, 0, 0, 0, time.UTC),
		Host:  "host-a.example.com", Instance: "instance-a", RunID: "run-a",
	}
	if err := store.SetLastTick(ctx, "doc-check", first); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.LastTick(ctx, "doc-check")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !got.DueAt.Equal(first.DueAt) || got.Host != first.Host ||
		got.Instance != first.Instance || got.RunID != first.RunID {
		t.Fatalf("read back %+v, wrote %+v", got, first)
	}

	next := first
	next.DueAt, next.RunID = first.DueAt.Add(time.Minute), "run-b"
	if err := store.SetLastTick(ctx, "doc-check", next); err != nil {
		t.Fatal(err)
	}
	got, _, err = store.LastTick(ctx, "doc-check")
	if err != nil || !got.DueAt.Equal(next.DueAt) || got.RunID != "run-b" {
		t.Fatalf("after replacing it read back %+v, err = %v", got, err)
	}

	if _, found, _ := store.LastTick(ctx, "other"); found {
		t.Fatal("one playbook's tick was read back for another")
	}
}

func TestARunsGuardFieldsRoundTrip(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The waiting trigger a run points at has to exist; its own writer lands with the
	// waiting slot, so it is written here directly.
	if _, err := rawDB(t, dir).ExecContext(ctx, `
		INSERT INTO waiting_triggers (id, playbook_name, playbook_path, trigger_kind,
		                              accepted_at, expires_at, instance, outcome)
		VALUES ('waiting-1', 'doc-check', '/srv/playbooks/doc-check.yaml', 'manual',
		        '2026-09-10T06:00:00Z', '2026-09-10T06:30:00Z', 'instance-a', 'waiting')`,
	); err != nil {
		t.Fatal(err)
	}

	waitedRun := record.Run{
		ID: "run-waited", PlaybookName: "doc-check", TriggerKind: record.TriggerManual,
		Status: record.StatusRunning, StartedAt: time.Now(),
		WaitingTriggerID: "waiting-1", WaitedMS: 734000,
		ClaimReach: record.ReachCrossHost, ClaimToken: 42,
	}
	plainRun := record.Run{
		ID: "run-plain", PlaybookName: "doc-check", TriggerKind: record.TriggerSchedule,
		Status: record.StatusRunning, StartedAt: time.Now(), ClaimReach: record.ReachSingleHost,
	}
	for _, run := range []record.Run{waitedRun, plainRun} {
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	// Finishing says nothing about the claim, and must not erase what beginning recorded.
	if err := store.FinishRun(ctx, record.Run{ID: "run-waited", Status: record.StatusClaimLost}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, record.Run{ID: "run-plain", Status: record.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetRun(ctx, "run-waited")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusClaimLost || got.WaitingTriggerID != "waiting-1" ||
		got.WaitedMS != 734000 || got.ClaimReach != record.ReachCrossHost || got.ClaimToken != 42 {
		t.Fatalf("read back as %+v", got)
	}

	plain, err := store.GetRun(ctx, "run-plain")
	if err != nil {
		t.Fatal(err)
	}
	if plain.ClaimReach != record.ReachSingleHost || plain.ClaimToken != 0 || plain.WaitingTriggerID != "" {
		t.Fatalf("read back as %+v", plain)
	}
	// A run that did not wait has no time waited, which is not the same as zero.
	var waited, token sql.NullInt64
	if err := rawDB(t, dir).QueryRowContext(ctx,
		`SELECT waited_ms, claim_token FROM runs WHERE id = 'run-plain'`).Scan(&waited, &token); err != nil {
		t.Fatal(err)
	}
	if waited.Valid || token.Valid {
		t.Fatalf("a single-host run that did not wait stored waited_ms=%v claim_token=%v", waited, token)
	}
}

// SC-005 for the guard's own record: a refusal's detail carries whatever the backend
// said, and a backend refusing a credential is likely to repeat it.
func TestNoConfiguredSecretReachesARefusal(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, record.NewRedactor([]string{testsecret.Value}))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRefusal(ctx, record.Refusal{
		PlaybookName: "doc-check", TriggerKind: record.TriggerSchedule,
		DueAt: time.Now(), Mechanism: record.MechanismBackendUnavailable,
		Detail:    "etcd at unix:///run/etcd.sock: authentication failed for " + testsecret.Value,
		RefusedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	refusals, err := store.ListRefusals(ctx, 1)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %v, err = %v", refusals, err)
	}
	if !strings.Contains(refusals[0].Detail, record.Placeholder) {
		t.Fatalf("the detail was not redacted where it was written: %q", refusals[0].Detail)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	secret := []byte(testsecret.Value)
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) //nolint:gosec // walking a directory this test made
		if err != nil {
			return err
		}
		if bytes.Contains(data, secret) {
			t.Errorf("the configured secret is in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
