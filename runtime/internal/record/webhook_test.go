package record_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/testsecret"
)

// T009. One parent test, each subtest named for the mutant it is scoped to catch: a
// store from before this feature migrates and reads back with delivery_id empty, a
// delivery and its hand-offs round-trip with a refusal, counting upserts one row per
// key, and the testsecret sentinel reaches neither the database nor the blob directory.
func TestWebhookRecord(t *testing.T) {
	t.Run("MigratesFromTheGuardsSchema", testWebhookRecordMigrates)
	t.Run("ADeliveryItsHandOffsAndARefusalRoundTrip", testADeliveryItsHandOffsAndARefusalRoundTrip)
	t.Run("CountingUpsertsOneRowPerKey", testCountingUpsertsOneRowPerKey)
	t.Run("ASecretInARefusedBodyIsRedactedEverywhere", testASecretInARefusedBodyIsRedactedEverywhere)
}

// A deployment upgrading to the webhook feature already has a record written under the
// guard's schema alone. As TestAStoreFromTheRuntimeCoreMigrates shows for the guard's
// own migration, this asserts a store from before it opens, gains the new tables, and
// reads its existing runs, refusals and waiting triggers back with delivery_id empty.
func testWebhookRecordMigrates(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	guardSchema, err := os.ReadFile("migrations/0002_guard.sql")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	for _, statement := range []string{
		string(initial),
		string(guardSchema),
		`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations VALUES
		     ('0001_initial.sql', '2026-09-01T00:00:00Z'),
		     ('0002_guard.sql', '2026-09-01T00:00:00Z')`,
		`INSERT INTO runs (id, playbook_name, trigger_kind, status, started_at, ended_at)
		 VALUES ('20260901T000000Z-000000000001', 'drift-check', 'schedule', 'succeeded',
		         '2026-09-01T00:00:00Z', '2026-09-01T00:01:00Z')`,
		`INSERT INTO refusals (id, playbook_name, trigger_kind, mechanism, detail, refused_at)
		 VALUES ('refusal-old', 'drift-check', 'manual', 'claim_held', 'held', '2026-09-01T00:00:00Z')`,
		`INSERT INTO waiting_triggers (id, playbook_name, playbook_path, trigger_kind,
		                              accepted_at, expires_at, instance, outcome)
		 VALUES ('waiting-old', 'drift-check', '/srv/playbooks/drift-check.yaml', 'manual',
		         '2026-09-01T00:00:00Z', '2026-09-01T00:30:00Z', 'instance-a', 'wait_expired')`,
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
		t.Fatalf("a store from before the webhook feature did not open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	run, err := store.GetRun(ctx, "20260901T000000Z-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if run.DeliveryID != "" {
		t.Fatalf("a run recorded before the webhook feature reads back with delivery_id %q", run.DeliveryID)
	}

	refusals, err := store.ListRefusals(ctx, 10)
	if err != nil || len(refusals) != 1 || refusals[0].DeliveryID != "" {
		t.Fatalf("refusals = %+v, err = %v", refusals, err)
	}

	waiting, err := store.GetWaitingTrigger(ctx, "waiting-old")
	if err != nil || waiting.DeliveryID != "" {
		t.Fatalf("waiting trigger = %+v, err = %v", waiting, err)
	}
}

// A delivery, its hand-offs and a refusal round-trip. The acceptance transaction itself
// (T049) does not exist yet, so the rows are seeded directly, the way
// TestARunsGuardFieldsRoundTrip seeds a waiting trigger ahead of its own writer.
func testADeliveryItsHandOffsAndARefusalRoundTrip(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	received := time.Date(2026, 9, 10, 6, 35, 12, 0, time.UTC)
	if _, err := rawDB(t, dir).ExecContext(ctx, `
		INSERT INTO deliveries (id, source, identity, identity_kind, received_at, peer,
		                        body_ref, body_sha256, repeats, instance, state)
		VALUES ('delivery-1', 'alerts', 'digest-abc', 'digest', ?, '192.0.2.10',
		        'delivery-1/body', 'sha-abc', 0, 'instance-a', 'accepted')`,
		received.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB(t, dir).ExecContext(ctx, `
		INSERT INTO handoffs (delivery_id, playbook_name, state)
		VALUES ('delivery-1', 'alert-triage', 'pending'), ('delivery-1', 'alert-page', 'pending')`,
	); err != nil {
		t.Fatal(err)
	}

	delivery, err := store.GetDelivery(ctx, "delivery-1")
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Source != "alerts" || delivery.Identity != "digest-abc" ||
		delivery.IdentityKind != record.IdentityDigest || delivery.State != record.DeliveryAccepted {
		t.Fatalf("read back as %+v", delivery)
	}
	if !delivery.ReceivedAt.Equal(received) || delivery.ReceivedAt.Location() != time.UTC {
		t.Fatalf("received_at read back as %v, wrote %v", delivery.ReceivedAt, received)
	}

	listed, err := store.ListDeliveries(ctx, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != "delivery-1" {
		t.Fatalf("listed = %+v, err = %v", listed, err)
	}

	handoffs, err := store.HandOffsOf(ctx, "delivery-1")
	if err != nil || len(handoffs) != 2 {
		t.Fatalf("hand-offs = %+v, err = %v", handoffs, err)
	}
	if handoffs[0].PlaybookName != "alert-page" || handoffs[1].PlaybookName != "alert-triage" {
		t.Fatalf("hand-offs are not ordered by playbook name: %+v", handoffs)
	}
	for _, handoff := range handoffs {
		if handoff.State != record.HandOffPending || !handoff.DecidedAt.IsZero() {
			t.Fatalf("a fresh hand-off read back as %+v", handoff)
		}
	}

	decided := received.Add(2 * time.Second)
	if err := store.SetHandOffState(ctx, "delivery-1", "alert-triage", record.HandOffHandedOff, decided); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeliveryState(ctx, "delivery-1", record.DeliveryHandedOff); err != nil {
		t.Fatal(err)
	}

	after, err := store.HandOffsOf(ctx, "delivery-1")
	if err != nil {
		t.Fatal(err)
	}
	var triage record.HandOff
	for _, handoff := range after {
		if handoff.PlaybookName == "alert-triage" {
			triage = handoff
		}
	}
	if triage.State != record.HandOffHandedOff || !triage.DecidedAt.Equal(decided) {
		t.Fatalf("alert-triage's hand-off read back as %+v", triage)
	}
	again, err := store.GetDelivery(ctx, "delivery-1")
	if err != nil || again.State != record.DeliveryHandedOff {
		t.Fatalf("delivery = %+v, err = %v", again, err)
	}

	// A refusal of one playbook's hand-off for a value, naming the delivery.
	refusalID, err := store.AddDeliveryRefusal(ctx, record.DeliveryRefusal{
		Source: "alerts", Reason: record.ReasonValueNoMatch, ValueName: "alertname",
		PlaybookName: "alert-page", DeliveryID: "delivery-1",
		ReceivedAt: received, Peer: "192.0.2.10",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	refusals, err := store.ListDeliveryRefusals(ctx, 10)
	if err != nil || len(refusals) != 1 {
		t.Fatalf("refusals = %+v, err = %v", refusals, err)
	}
	if refusals[0].ID != refusalID || refusals[0].Reason != record.ReasonValueNoMatch ||
		refusals[0].ValueName != "alertname" || refusals[0].PlaybookName != "alert-page" ||
		refusals[0].DeliveryID != "delivery-1" || refusals[0].BodyRef != "" {
		t.Fatalf("refusal read back as %+v", refusals[0])
	}
}

// Counting one key twice leaves one row whose count is two, and a second reason a second
// row. FR-333: the record grows with the number of distinct keys, not with the number of
// requests.
func testCountingUpsertsOneRowPerKey(t *testing.T) {
	ctx := t.Context()
	store, err := record.Open(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	interval := time.Date(2026, 9, 10, 6, 41, 0, 0, time.UTC)
	first := interval.Add(2 * time.Second)
	second := interval.Add(9 * time.Second)

	for _, at := range []time.Time{first, second} {
		if err := store.CountUnauthenticated(ctx, interval, "unknown_source", "(unconfigured)",
			"192.0.2.7", at); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CountUnauthenticated(ctx, interval, "signature_mismatch", "alerts",
		"192.0.2.7", second); err != nil {
		t.Fatal(err)
	}

	counts, err := store.ListUnauthenticatedCounts(ctx, 10)
	if err != nil || len(counts) != 2 {
		t.Fatalf("counts = %+v, err = %v", counts, err)
	}

	byReason := map[string]record.UnauthenticatedRefusalCount{}
	for _, count := range counts {
		byReason[count.Reason] = count
	}
	unknown := byReason["unknown_source"]
	if unknown.Count != 2 || unknown.SourceBucket != "(unconfigured)" || !unknown.LastAt.Equal(second) {
		t.Fatalf("unknown_source counted as %+v", unknown)
	}
	mismatch := byReason["signature_mismatch"]
	if mismatch.Count != 1 || mismatch.SourceBucket != "alerts" {
		t.Fatalf("signature_mismatch counted as %+v", mismatch)
	}
}

// The testsecret sentinel written into a refused body, and a refusal's value, reach
// neither record.db nor the blob directory: the body goes through the blob store's own
// write boundary, which redacts it exactly as every other blob is redacted.
func testASecretInARefusedBodyIsRedactedEverywhere(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	store, err := record.Open(ctx, dir, record.NewRedactor([]string{testsecret.Value}))
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"alertname":"disk full","token":"` + testsecret.Value + `"}`)
	if _, err := store.AddDeliveryRefusal(ctx, record.DeliveryRefusal{
		Source: "alerts", Reason: record.ReasonBodyNotJSON,
		ReceivedAt: time.Now(), Peer: "192.0.2.10",
	}, body); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	secret := []byte(testsecret.Value)
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // walking a directory this test made
		if readErr != nil {
			return readErr
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
