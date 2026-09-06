package sink_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/sink"
)

// forge stands in for GitHub: it holds the open issues, records what was created, and
// can be told to fail after n creations.
type forge struct {
	mu        sync.Mutex
	open      []string
	created   []string
	failAfter int
	listCode  int
	pages     int
	// label is what this forge expects the sink to list and create against. Empty means
	// the Marker, which is what a playbook naming no label gets.
	label string
}

func (f *forge) wants() string {
	if f.label == "" {
		return sink.Marker
	}
	return f.label
}

func (f *forge) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		switch r.Method {
		case http.MethodGet:
			if f.listCode != 0 {
				w.WriteHeader(f.listCode)
				return
			}
			// The cap counts what this runtime opened, which is what the label is for.
			if r.URL.Query().Get("labels") != f.wants() {
				http.Error(w, "the sink listed issues it did not label", http.StatusBadRequest)
				return
			}
			// Paginated like the real endpoint, so a sink that reads one page is caught
			// here rather than on the repository where it matters.
			page := 1
			if raw := r.URL.Query().Get("page"); raw != "" {
				page, _ = strconv.Atoi(raw)
			}
			size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			if size <= 0 {
				size = 30
			}
			from := (page - 1) * size
			issues := make([]map[string]any, 0, size)
			for at := from; at < from+size && at < len(f.open); at++ {
				issues = append(issues, map[string]any{"title": f.open[at]})
			}
			f.pages++
			_ = json.NewEncoder(w).Encode(issues)

		case http.MethodPost:
			if f.failAfter > 0 && len(f.created) >= f.failAfter {
				http.Error(w, "the forge is down", http.StatusInternalServerError)
				return
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var issue struct {
				Title  string   `json:"title"`
				Labels []string `json:"labels"`
			}
			_ = json.Unmarshal(body, &issue)
			if len(issue.Labels) == 0 || issue.Labels[0] != f.wants() {
				http.Error(w, "an issue was created without the marker", http.StatusBadRequest)
				return
			}
			f.created = append(f.created, issue.Title)
			f.open = append(f.open, issue.Title)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": len(f.created)})

		default:
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		}
	})
}

func (f *forge) createdTitles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...)
}

func issueSink(t *testing.T, ceiling int, prepare func(*forge)) (*forge, sink.Sink) {
	t.Helper()
	got := &forge{}
	if prepare != nil {
		prepare(got)
	}
	server := httptest.NewServer(got.handler())
	t.Cleanup(server.Close)
	return got, sink.NewGitHub("owner/repo", "t0ken", got.label, ceiling, true,
		server.URL, server.Client())
}

func findings(count int) []byte {
	items := make([]map[string]any, 0, count)
	for at := range count {
		items = append(items, map[string]any{
			"title": fmt.Sprintf("finding %d", at+1),
			"body":  "what was found",
		})
	}
	report, err := json.Marshal(map[string]any{"findings": items})
	if err != nil {
		panic(err)
	}
	return report
}

// T059, FR-024. The cap is the point: an uncapped creator gets muted within a month, and
// the useful signal goes with the noise.
func TestSevenFindingsAgainstACapOfThreeCreatesThree(t *testing.T) {
	got, one := issueSink(t, 3, nil)

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "drift-check", RunID: "run-1", Report: findings(7),
	})
	if err != nil {
		t.Fatal(err)
	}

	if outcome.ItemsCreated != 3 {
		t.Fatalf("created %d, want 3", outcome.ItemsCreated)
	}
	if outcome.ItemsSkipped != 4 {
		t.Fatalf("skipped %d, want 4", outcome.ItemsSkipped)
	}
	if len(got.createdTitles()) != 3 {
		t.Fatalf("the forge saw %d issues", len(got.createdTitles()))
	}
	if outcome.Status != sink.StatusCreated {
		t.Fatalf("status = %q", outcome.Status)
	}
	if !strings.Contains(outcome.Detail, "cap") {
		t.Fatalf("the outcome does not say it stopped at the cap: %q", outcome.Detail)
	}
}

// T060. Capped is not failed: the cap doing its job is the system working as declared,
// and recording it as a failure trains an operator to ignore the status.
func TestWithTheCapAlreadyOpenNothingIsCreated(t *testing.T) {
	got, one := issueSink(t, 3, func(f *forge) {
		f.open = []string{"an older finding", "another", "a third"}
	})

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(5),
	})
	if err != nil {
		t.Fatal(err)
	}

	if outcome.Status != sink.StatusCapped {
		t.Fatalf("status = %q, want capped", outcome.Status)
	}
	if outcome.ItemsCreated != 0 || len(got.createdTitles()) != 0 {
		t.Fatalf("created %d", outcome.ItemsCreated)
	}
	if outcome.ItemsSkipped != 5 {
		t.Fatalf("skipped %d, want all five", outcome.ItemsSkipped)
	}
	if !strings.Contains(outcome.Detail, "already open") {
		t.Fatalf("the outcome does not say why: %q", outcome.Detail)
	}
}

// FR-024 counts what is already open, so a partly-full repository leaves partial room.
func TestTheCapCountsWhatIsAlreadyOpen(t *testing.T) {
	_, one := issueSink(t, 3, func(f *forge) { f.open = []string{"an older finding"} })

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ItemsCreated != 2 {
		t.Fatalf("created %d, want 2 — one was already open against a cap of three",
			outcome.ItemsCreated)
	}
}

// A runtime that reopens the same issue every night is the noise the cap exists for.
func TestAFindingAlreadyOpenIsNotOpenedAgain(t *testing.T) {
	got, one := issueSink(t, 5, func(f *forge) { f.open = []string{"finding 1", "finding 2"} })

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ItemsCreated != 1 {
		t.Fatalf("created %d, want 1", outcome.ItemsCreated)
	}
	if titles := got.createdTitles(); len(titles) != 1 || titles[0] != "finding 3" {
		t.Fatalf("created %v", titles)
	}
	if !strings.Contains(outcome.Detail, "already open") {
		t.Fatalf("the outcome does not say which were skipped: %q", outcome.Detail)
	}
}

// T061. What was created stays created, and the outcome says which items got through —
// without that an operator cannot tell what to do next.
func TestAFailureMidwayRecordsWhatWasCreated(t *testing.T) {
	got, one := issueSink(t, 5, func(f *forge) { f.failAfter = 2 })

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(5),
	})
	if err != nil {
		t.Fatal(err)
	}

	if outcome.Status != sink.StatusFailed {
		t.Fatalf("status = %q", outcome.Status)
	}
	if outcome.ItemsCreated != 2 {
		t.Fatalf("created %d, want the two that got through", outcome.ItemsCreated)
	}
	if len(got.createdTitles()) != 2 {
		t.Fatalf("the forge saw %d", len(got.createdTitles()))
	}
	if outcome.ItemsCreated+outcome.ItemsSkipped != 5 {
		t.Fatalf("%d created and %d skipped does not account for five findings",
			outcome.ItemsCreated, outcome.ItemsSkipped)
	}
	if !strings.Contains(outcome.Detail, "failed") {
		t.Fatalf("the outcome does not name the failure: %q", outcome.Detail)
	}
}

// A creating sink does not narrate a failure into a repository.
func TestARefusedReportCreatesNothing(t *testing.T) {
	got, one := issueSink(t, 3, nil)

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1",
		Failure: fmt.Errorf("the report does not satisfy the declared output schema"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != sink.StatusSkipped || len(got.createdTitles()) != 0 {
		t.Fatalf("status = %q, created %v", outcome.Status, got.createdTitles())
	}
}

func TestAReportWithNoFindingsCreatesNothing(t *testing.T) {
	got, one := issueSink(t, 3, nil)

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: []byte(`{"findings":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != sink.StatusSkipped || len(got.createdTitles()) != 0 {
		t.Fatalf("status = %q, created %v", outcome.Status, got.createdTitles())
	}
}

// A cap cannot be checked against a repository that will not answer, and creating
// against an unknown count is exactly what FR-024 forbids.
func TestAnUnreadableRepositoryCreatesNothing(t *testing.T) {
	got, one := issueSink(t, 3, func(f *forge) { f.listCode = http.StatusForbidden })

	_, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(3),
	})
	if err == nil {
		t.Fatal("the sink created against a count it could not read")
	}
	if len(got.createdTitles()) != 0 {
		t.Fatalf("created %v", got.createdTitles())
	}
}

func TestTheIssueSinkDeclaresItselfCreatingAndCapped(t *testing.T) {
	_, one := issueSink(t, 3, nil)

	if !one.Creates() {
		t.Fatal("the issue sink does not report that it creates things")
	}
	ceiling, declared := one.Cap()
	if !declared || ceiling != 3 {
		t.Fatalf("cap = %d, declared = %v", ceiling, declared)
	}
	if err := sink.CheckCap(one); err != nil {
		t.Fatalf("a declared cap was refused: %v", err)
	}
}

// FR-006 through the builder, which is where a playbook's declaration arrives.
func TestBuildRefusesAnIssueSinkWithNoCap(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{{
		Type:   "github",
		Config: map[string]any{"repo": "owner/repo", "token": "t"},
	}}, sink.BuildOptions{})

	if len(problems) != 1 {
		t.Fatalf("problems = %v", problems)
	}
	if !strings.Contains(problems[0].Error(), "cap") {
		t.Fatalf("the refusal does not name the cap: %v", problems[0])
	}
}

// A cap read from YAML is an int and one that came through JSON is a float64. A bound
// lost to which decoder produced it is a bound lost silently.
func TestACapIsReadWhicheverDecoderProducedIt(t *testing.T) {
	for name, value := range map[string]any{"int": 3, "float64": float64(3), "int64": int64(3)} {
		sinks, problems := sink.Build([]sink.Declaration{{
			Type:   "github",
			Config: map[string]any{"repo": "owner/repo", "token": "t", "cap": value},
		}}, sink.BuildOptions{})
		if len(problems) != 0 {
			t.Errorf("%s: %v", name, problems)
			continue
		}
		if ceiling, declared := sinks[0].Cap(); !declared || ceiling != 3 {
			t.Errorf("%s: cap = %d, declared = %v", name, ceiling, declared)
		}
	}
}

func TestBuildRefusesARepositoryThatIsNotOwnerName(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{{
		Type:   "github",
		Config: map[string]any{"repo": "just-a-name", "token": "t", "cap": 3},
	}}, sink.BuildOptions{})

	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "owner/name") {
		t.Fatalf("problems = %v", problems)
	}
}

// The hole a review found: one page of a hundred, and past that the count came back
// short, room came back larger than the truth, and the sink created MORE than its cap.
func TestTheCapCountsBeyondTheFirstPage(t *testing.T) {
	const alreadyOpen = 150

	existing := make([]string, 0, alreadyOpen)
	for at := range alreadyOpen {
		existing = append(existing, fmt.Sprintf("an older finding %d", at+1))
	}
	got, one := issueSink(t, 100, func(f *forge) { f.open = existing })

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(5),
	})
	if err != nil {
		t.Fatal(err)
	}

	if outcome.Status != sink.StatusCapped {
		t.Fatalf("status = %q; 150 open against a cap of 100 is saturated", outcome.Status)
	}
	if len(got.createdTitles()) != 0 {
		t.Fatalf("created %d issues past a cap already exceeded", len(got.createdTitles()))
	}
	got.mu.Lock()
	pages := got.pages
	got.mu.Unlock()
	if pages < 2 {
		t.Fatalf("the sink read %d page(s); the count stops at a hundred on one", pages)
	}
}

// Counting and de-duplicating are different questions. Two open issues sharing a title
// are two issues against the cap, however many distinct titles that is.
func TestTwoOpenIssuesSharingATitleCountTwice(t *testing.T) {
	got, one := issueSink(t, 3, func(f *forge) {
		f.open = []string{"the same finding", "the same finding", "another"}
	})

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != sink.StatusCapped {
		t.Fatalf("status = %q; three open against a cap of three is saturated", outcome.Status)
	}
	if len(got.createdTitles()) != 0 {
		t.Fatalf("created %v", got.createdTitles())
	}
}

// The cap is claimed only when it stopped something. Saying it on a run where every skip
// was a duplicate tells an operator the ceiling is the bottleneck when it is not.
func TestTheCapIsOnlyBlamedWhenItBit(t *testing.T) {
	_, one := issueSink(t, 3, func(f *forge) {
		f.open = []string{"finding 1"}
	})

	// Two findings: one already open, one new. Room is two, one is created — equal to
	// nothing being left on the floor by the cap.
	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outcome.Detail, "stopped at the cap") {
		t.Fatalf("the cap was blamed for a skip it did not cause: %q", outcome.Detail)
	}
	if !strings.Contains(outcome.Detail, "already open") {
		t.Fatalf("the outcome does not say what actually happened: %q", outcome.Detail)
	}
}

// Declared and empty is not absent: a playbook that looks configured and is not would
// build an unauthenticated client with nothing said at load time.
func TestATokenThatResolvesToNothingIsRefused(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{{
		Type:   "github",
		Config: map[string]any{"repo": "owner/repo", "token": "${config.github_token}", "cap": 3},
	}}, sink.BuildOptions{
		Interpolate: func(text string) (string, error) {
			// The deployment holds the key and its value is empty, which is not the same
			// as the key not being there.
			if text == "${config.github_token}" {
				return "", nil
			}
			return text, nil
		},
	})

	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "resolves to nothing") {
		t.Fatalf("problems = %v", problems)
	}

	// And omitting the field entirely is still allowed, for a runner carrying its own.
	if _, problems := sink.Build([]sink.Declaration{{
		Type:   "github",
		Config: map[string]any{"repo": "owner/repo", "cap": 3},
	}}, sink.BuildOptions{}); len(problems) != 0 {
		t.Fatalf("an absent token was refused: %v", problems)
	}
}

// The mirror of the pagination fix, and the one my own comment claimed was already
// handled while the code did the opposite: past what the walk covers, the count is a
// floor rather than a total, and creating against it is exactly what FR-024 forbids.
func TestMoreOpenIssuesThanTheWalkCoversCreatesNothing(t *testing.T) {
	const beyond = 2500

	existing := make([]string, 0, beyond)
	for at := range beyond {
		existing = append(existing, fmt.Sprintf("an older finding %d", at+1))
	}
	// A cap deliberately larger than what the walk can count, so a truncated count would
	// leave room and create.
	got, one := issueSink(t, 2200, func(f *forge) { f.open = existing })

	_, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(5),
	})
	if !errors.Is(err, sink.ErrTooManyOpen) {
		t.Fatalf("err = %v, want ErrTooManyOpen", err)
	}
	if len(got.createdTitles()) != 0 {
		t.Fatalf("created %d issues against a count it could not complete",
			len(got.createdTitles()))
	}
}

// A repository name is interpolated into the endpoint, so its shape is checked rather
// than assumed. "owner/name?state=closed" contains a slash too.
func TestARepositoryNameIsCheckedAgainstItsShape(t *testing.T) {
	for _, repo := range []string{
		"owner/name?state=closed", "owner/name/extra", "owner/name#fragment",
		"../../etc/passwd", "owner /name", "/name", "owner/",
		// A trailing dot or hyphen in either segment. Refusing these was written and
		// untested: a reviewer reverted the tightening and the whole suite stayed green.
		"owner-/name", "owner/name.", "owner./name", "owner/name-",
	} {
		_, problems := sink.Build([]sink.Declaration{{
			Type:   "github",
			Config: map[string]any{"repo": repo, "cap": 3},
		}}, sink.BuildOptions{})
		if len(problems) == 0 {
			t.Errorf("repo %q was accepted", repo)
		}
	}
	for _, repo := range []string{"owner/name", "nicodarge/Gronin", "a-b.c/d_e.f"} {
		_, problems := sink.Build([]sink.Declaration{{
			Type:   "github",
			Config: map[string]any{"repo": repo, "cap": 3},
		}}, sink.BuildOptions{})
		if len(problems) != 0 {
			t.Errorf("repo %q was refused: %v", repo, problems)
		}
	}
}

// The boundary the last fix moved rather than covered: at exactly what the walk can
// count, the final page is full and means "that was all", not "more follow". Refusing
// there fails closed, but it refuses a count it actually has.
func TestExactlyAsManyOpenAsTheWalkCoversIsStillCounted(t *testing.T) {
	const exactly = 2000 // maxPages * perPage

	existing := make([]string, 0, exactly)
	for at := range exactly {
		existing = append(existing, fmt.Sprintf("an older finding %d", at+1))
	}
	got, one := issueSink(t, 2200, func(f *forge) { f.open = existing })

	outcome, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1", Report: findings(5),
	})
	if err != nil {
		t.Fatalf("a count the walk could complete was refused: %v", err)
	}
	if outcome.ItemsCreated != 5 {
		t.Fatalf("created %d of 5 with 200 of the cap unused", outcome.ItemsCreated)
	}
	if len(got.createdTitles()) != 5 {
		t.Fatalf("the forge saw %d", len(got.createdTitles()))
	}
}

// A playbook names the label its issues carry, and the cap is counted against that label
// rather than against a constant. Two playbooks opening issues on one repository
// otherwise share one cap, and the busier of them silences the other.
//
// The forge refuses a listing or a creation carrying any other label, so this fails if
// the sink counts one set and creates into another.
func TestTheCapIsCountedAgainstTheDeclaredLabel(t *testing.T) {
	forge, issues := issueSink(t, 3, func(f *forge) {
		f.label = "doc-drift"
		f.open = []string{"already open"}
	})

	outcome, err := issues.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "doc-check", RunID: "run-1", Report: findings(4),
	})
	if err != nil {
		t.Fatal(err)
	}
	// One is already open against a cap of three, so two rooms are left.
	if outcome.ItemsCreated != 2 {
		t.Fatalf("created %d, and two rooms were left under the cap: %+v",
			outcome.ItemsCreated, outcome)
	}
	if got := len(forge.createdTitles()); got != 2 {
		t.Fatalf("the forge saw %d creations", got)
	}
}

// A playbook naming no label gets the Marker, which is what every playbook written before
// the field existed relies on.
func TestNoDeclaredLabelIsTheMarker(t *testing.T) {
	forge, issues := issueSink(t, 2, nil)

	if _, err := issues.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "r", Report: findings(1),
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(forge.createdTitles()); got != 1 {
		t.Fatalf("the forge, which refuses anything but the marker, saw %d creations", got)
	}
}

// The refusing half. A label is interpolated into a URL query and into the created
// issue's labels, and the two have to name the same thing — so the shapes where they
// would not are refused rather than escaped.
func TestBuildRefusesALabelThatWouldSplitTheCap(t *testing.T) {
	for name, declared := range map[string]any{
		"a comma is two labels to GitHub": "doc-drift,urgent",
		"empty drops the filter entirely": "",
		"whitespace is empty":             "   ",
		"not a string":                    42,
	} {
		t.Run(name, func(t *testing.T) {
			_, problems := sink.Build([]sink.Declaration{{
				Type: "github",
				Config: map[string]any{
					"repo": "owner/repo", "cap": 1, "label": declared,
				},
			}}, sink.BuildOptions{})

			if len(problems) != 1 {
				t.Fatalf("problems = %v, and this label is meant to be refused", problems)
			}
			if !strings.Contains(problems[0].Error(), "label") {
				t.Errorf("the refusal does not name the field: %v", problems[0])
			}
		})
	}
}

// The accepting half, so the refusal above is not an allowlist of one.
func TestBuildAcceptsALabelAPlaybookMayReasonablyWant(t *testing.T) {
	for _, label := range []string{"doc-drift", "puppet-drift", "area/docs", "P1: urgent"} {
		built, problems := sink.Build([]sink.Declaration{{
			Type:   "github",
			Config: map[string]any{"repo": "owner/repo", "cap": 1, "label": label},
		}}, sink.BuildOptions{})
		if len(problems) != 0 {
			t.Errorf("label %q was refused: %v", label, problems)
		}
		if len(built) != 1 {
			t.Errorf("label %q built no sink", label)
		}
	}
}

// A label is a reference like every other value a playbook holds, so the deployment is
// what supplies it.
func TestALabelResolvesThroughTheDeployment(t *testing.T) {
	built, problems := sink.Build([]sink.Declaration{{
		Type: "github",
		Config: map[string]any{
			"repo": "owner/repo", "cap": 1, "label": "${config.drift_label}",
		},
	}}, sink.BuildOptions{
		// Only the reference resolves. A resolver answering the same value for every
		// input made this fail on the repo rather than pass on the label.
		Interpolate: func(raw string) (string, error) {
			if raw == "${config.drift_label}" {
				return "doc-drift", nil
			}
			return raw, nil
		},
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if len(built) != 1 {
		t.Fatal("no sink was built")
	}
}
