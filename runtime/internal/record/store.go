package record

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // the pure-Go driver; cgo would forfeit the static binary
)

// Status is a run's terminal state, or `running` while it is one.
type Status string

// Refused is not Failed. A refused run never started — its bounds receipt did not match,
// or a gather step failed — so it costs nothing and is not an incident. It still has to
// be visible: a playbook refused every night is broken in a way one shared status hides.
const (
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusTimedOut    Status = "timed_out"
	StatusCapped      Status = "capped"
	StatusInterrupted Status = "interrupted"
	StatusRefused     Status = "refused"
	// StatusClaimLost is a run stopped because its claim could no longer be proven held
	// (FR-105). Not failed — nothing in the run went wrong, the deployment lost its
	// authority — and not interrupted, which is a run the next process found running.
	StatusClaimLost Status = "claim_lost"
)

// Reach is which guarantee a run ran under (FR-109).
type Reach string

// The two reaches a claim can have.
const (
	ReachCrossHost  Reach = "cross-host"
	ReachSingleHost Reach = "single-host"
)

// Mechanism is what refused a trigger that did not become a run (FR-117).
type Mechanism string

// Every mechanism a refusal record can name, as data-model.md lists them.
const (
	MechanismClaimHeld          Mechanism = "claim_held"
	MechanismTickAlreadyRan     Mechanism = "tick_already_ran"
	MechanismWaitingSlotFull    Mechanism = "waiting_slot_full"
	MechanismWaitExpired        Mechanism = "wait_expired"
	MechanismRateLimited        Mechanism = "rate_limited"
	MechanismBackendUnavailable Mechanism = "backend_unavailable"
	MechanismPlaybookChanged    Mechanism = "playbook_changed"
	MechanismDropped            Mechanism = "dropped"
)

// TriggerKind is how a run came to exist. Deliberately not the playbook's trigger type:
// a playbook is triggered by cron or by hand, while replay and resume are ways a run
// begins that no playbook can declare.
type TriggerKind string

// How a run began. Replay and resume are ways a run starts that no playbook declares.
const (
	TriggerSchedule TriggerKind = "schedule"
	TriggerManual   TriggerKind = "manual"
	TriggerReplay   TriggerKind = "replay"
	TriggerResume   TriggerKind = "resume"
)

// Run is one execution of one playbook.
type Run struct {
	ID                  string
	PlaybookName        string
	ResolvedPlaybookRef string
	ReportRef           string
	PromptRef           string
	TriggerKind         TriggerKind
	ParentRunID         string
	Status              Status
	StartedAt           time.Time
	EndedAt             time.Time
	CostUSD             float64
	Tokens              int64
	AgentSessionID      string
	CredentialSource    string
	Error               string

	// WaitingTriggerID is the waiting trigger this run started from, empty for a run that
	// did not wait; WaitedMS is how long it waited, and is only recorded beside one.
	WaitingTriggerID string
	WaitedMS         int64
	ClaimReach       Reach
	// ClaimToken is the fencing token the run held; zero on a single-host deployment,
	// which has none.
	ClaimToken int64
}

// Store is the run record: a SQLite database for what is queried, and a directory of
// blobs for what is only ever read whole.
type Store struct {
	db       *sql.DB
	blobs    *Blobs
	redactor *Redactor
}

// Open opens or creates the record under dir. The redactor is held here rather than
// passed to each call: it is applied at this boundary, and a caller that has to remember
// to redact will eventually forget.
func Open(ctx context.Context, dir string, redactor *Redactor) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the record directory: %w", err)
	}

	// WAL so a reader — an operator running `gronin runs` — does not block the scheduler
	// writing. busy_timeout because two processes on one file is the normal case here.
	dsn := "file:" + filepath.Join(dir, "record.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening the record: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening the record: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	blobs, err := OpenBlobs(filepath.Join(dir, "blobs"), redactor)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, blobs: blobs, redactor: redactor}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Blobs is the artifact store this record writes through.
func (s *Store) Blobs() *Blobs { return s.blobs }

// ErrNotFound is returned for a run identifier nothing holds.
var ErrNotFound = errors.New("no such run")

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
