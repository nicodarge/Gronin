package record_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

func openStore(t *testing.T, secrets ...string) *record.Store {
	t.Helper()
	store, err := record.Open(t.Context(), t.TempDir(), record.NewRedactor(secrets))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestARunRoundTrips(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	started := time.Now().UTC().Truncate(time.Millisecond)

	if err := store.CreateRun(ctx, record.Run{
		ID: "run-1", PlaybookName: "drift-check", TriggerKind: record.TriggerSchedule,
		Status: record.StatusRunning, StartedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, record.Run{
		ID: "run-1", Status: record.StatusSucceeded, EndedAt: started.Add(time.Minute),
		CostUSD: 0.42, Tokens: 1234, AgentSessionID: "sess-1", CredentialSource: "ANTHROPIC_API_KEY",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusSucceeded || got.CostUSD != 0.42 || got.Tokens != 1234 {
		t.Fatalf("run read back as %+v", got)
	}
	if !got.StartedAt.Equal(started) {
		t.Fatalf("started_at round-tripped as %v, want %v", got.StartedAt, started)
	}
	if got.CredentialSource != "ANTHROPIC_API_KEY" {
		t.Fatalf("credential source = %q", got.CredentialSource)
	}
}

func TestAnUnknownRunIsNotFound(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)

	if _, err := store.GetRun(ctx, "nope"); !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := store.FinishRun(ctx, record.Run{ID: "nope", Status: record.StatusFailed}); !errors.Is(err, record.ErrNotFound) {
		t.Fatalf("finishing an unknown run gave %v, want ErrNotFound", err)
	}
}

// FR-037: a refused run and a failed one are different states, and the record has to
// keep them apart or a playbook refused every night looks like a nightly incident.
func TestRefusedIsNotFailed(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)

	for id, status := range map[string]record.Status{
		"refused-1": record.StatusRefused,
		"failed-1":  record.StatusFailed,
	} {
		if err := store.CreateRun(ctx, record.Run{
			ID: id, PlaybookName: "p", TriggerKind: record.TriggerManual, Status: record.StatusRunning,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, record.Run{ID: id, Status: status, EndedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	refused, err := store.GetRun(ctx, "refused-1")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.GetRun(ctx, "failed-1")
	if err != nil {
		t.Fatal(err)
	}
	if refused.Status == failed.Status {
		t.Fatal("refused and failed came back as the same status")
	}
}

func TestListRunsIsMostRecentFirst(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	base := time.Now().UTC()

	for i, id := range []string{"old", "middle", "new"} {
		if err := store.CreateRun(ctx, record.Run{
			ID: id, PlaybookName: "p", TriggerKind: record.TriggerManual,
			Status: record.StatusRunning, StartedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	runs, err := store.ListRuns(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].ID != "new" || runs[2].ID != "old" {
		t.Fatalf("order = %v", []string{runs[0].ID, runs[1].ID, runs[2].ID})
	}
}

// FR-031. A run the database still calls running cannot be, because this process has
// just started; and it is not restarted, because that would spend money on a decision
// nobody made.
func TestStartupMarksAnInFlightRunInterrupted(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	first, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CreateRun(ctx, record.Run{
		ID: "run-1", PlaybookName: "p", TriggerKind: record.TriggerSchedule, Status: record.StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := record.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()

	count, err := second.MarkRunningAsInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("marked %d runs interrupted, want 1", count)
	}
	got, err := second.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusInterrupted {
		t.Fatalf("status = %q", got.Status)
	}
	if got.Error == "" {
		t.Fatal("an interrupted run says nothing about why")
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	for range 3 {
		store, err := record.Open(t.Context(), dir, nil)
		if err != nil {
			t.Fatalf("reopening the record: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheChildTablesRecordAndReadBack(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)
	if err := store.CreateRun(ctx, record.Run{
		ID: "run-1", PlaybookName: "p", TriggerKind: record.TriggerManual, Status: record.StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.AddGatheredInput(ctx, "run-1", record.GatheredInput{
		Name: "facts.json", BlobRef: "run-1/facts.json", Bytes: 12, Truncated: true, ExitCode: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddToolCall(ctx, "run-1", record.ToolCall{
		Sequence: 1, Name: "Read", StartedAt: time.Now(), DurationMS: 3, Outcome: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRefusedAction(ctx, "run-1", record.RefusedAction{
		Sequence: 1, Tool: "Bash", Asked: `{"command":"rm -rf /"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSinkOutcome(ctx, "run-1", record.SinkOutcome{
		Sink: "discord", Status: "delivered", ItemsCreated: 1,
	}); err != nil {
		t.Fatal(err)
	}

	inputs, err := store.GatheredInputs(ctx, "run-1")
	if err != nil || len(inputs) != 1 || !inputs[0].Truncated {
		t.Fatalf("inputs = %+v, err = %v", inputs, err)
	}
	refusals, err := store.RefusedActions(ctx, "run-1")
	if err != nil || len(refusals) != 1 || refusals[0].Tool != "Bash" {
		t.Fatalf("refusals = %+v, err = %v", refusals, err)
	}
	outcomes, err := store.SinkOutcomes(ctx, "run-1")
	if err != nil || len(outcomes) != 1 || outcomes[0].Sink != "discord" {
		t.Fatalf("sink outcomes = %+v, err = %v", outcomes, err)
	}
}

func TestBlobsRefuseAReferenceLeavingTheirDirectory(t *testing.T) {
	dir := t.TempDir()
	blobs, err := record.OpenBlobs(filepath.Join(dir, "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "outside"), []byte("not yours"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{"../outside", "run-1/../../outside", "/etc/hostname"} {
		if data, err := blobs.Get(ref); err == nil {
			t.Errorf("reference %q was read: %q", ref, data)
		}
	}
}

func TestBlobsRoundTripAndRemove(t *testing.T) {
	blobs, err := record.OpenBlobs(filepath.Join(t.TempDir(), "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}

	ref, err := blobs.Put("run-1", "transcript.jsonl", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := blobs.Get(ref)
	if err != nil || string(got) != "hello" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if err := blobs.Remove("run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Get(ref); err == nil {
		t.Fatal("the blob survived its run being removed")
	}
}

// A name that cannot be a path segment is hashed rather than sanitised: two names that
// sanitise to the same thing would overwrite each other's record.
func TestBlobsDoNotCollideOnUnsafeNames(t *testing.T) {
	blobs, err := record.OpenBlobs(filepath.Join(t.TempDir(), "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}

	first, err := blobs.Put("run-1", "../escape", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := blobs.Put("run-1", "..%2Fescape", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two names shared the reference %q", first)
	}
	if !strings.HasPrefix(first, "run-1"+string(filepath.Separator)) {
		t.Fatalf("reference %q leaves the run's directory", first)
	}
	data, err := blobs.Get(first)
	if err != nil || string(data) != "one" {
		t.Fatalf("first blob read back as %q, err %v", data, err)
	}
}

// The guard nobody had probed. Resume calls FinishRun a second time on a run that has
// already recorded its cost and its credential source, and the first version wrote every
// column unconditionally — so the second call zeroed all of it, silently.
func TestASecondFinishKeepsWhatTheFirstRecorded(t *testing.T) {
	ctx := t.Context()
	store := openStore(t)

	if err := store.CreateRun(ctx, record.Run{
		ID: "run-1", PlaybookName: "p", TriggerKind: record.TriggerSchedule,
		Status: record.StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, record.Run{
		ID: "run-1", Status: record.StatusFailed, EndedAt: time.Now(),
		CostUSD: 0.42, Tokens: 1234, AgentSessionID: "sess-1",
		CredentialSource: "ANTHROPIC_API_KEY", ReportRef: "run-1/report.json",
		Error: "a sink failed",
	}); err != nil {
		t.Fatal(err)
	}

	// What a resume writes: a new status, and nothing else it knows about.
	if err := store.FinishRun(ctx, record.Run{
		ID: "run-1", Status: record.StatusSucceeded, EndedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != record.StatusSucceeded {
		t.Fatalf("status = %q; the second call is meant to set it", got.Status)
	}
	// The error describes the terminal state this call is writing, so a resume that
	// succeeds clears the failure it followed. Keeping it left a succeeded run reading
	// as failed forever, which the first version of this fix did.
	if got.Error != "" {
		t.Errorf("error = %q after a successful second finish; it should be cleared", got.Error)
	}
	for _, kept := range []struct {
		name string
		got  any
		want any
	}{
		{"cost", got.CostUSD, 0.42},
		{"tokens", got.Tokens, int64(1234)},
		{"session id", got.AgentSessionID, "sess-1"},
		{"credential source", got.CredentialSource, "ANTHROPIC_API_KEY"},
		{"report ref", got.ReportRef, "run-1/report.json"},
	} {
		if kept.got != kept.want {
			t.Errorf("%s = %v after a second finish, want %v kept", kept.name, kept.got, kept.want)
		}
	}
}
