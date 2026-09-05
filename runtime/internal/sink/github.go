package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultGitHubAPI is where a deployment's issues are created.
const DefaultGitHubAPI = "https://api.github.com"

// Marker is put on every issue this runtime opens, so the cap can count what it created
// before without counting what a human opened by hand. Counting every open issue in a
// repository would make one busy week silence the runtime entirely.
const Marker = "gronin"

// GitHub creates issues, and is the one sink here that brings anything into existence.
//
// FR-024 is the whole of its design: it MUST NOT create more than its cap, counting the
// items it created before that are still open. An uncapped creator gets muted within a
// month, and the useful signal is lost along with the noise — so the cap counts against
// reality rather than against one run.
type GitHub struct {
	repo   string
	token  string
	cap    int
	capSet bool
	api    string
	client *http.Client
}

// NewGitHub returns the issue sink.
func NewGitHub(repo, token string, ceiling int, capSet bool, api string, client *http.Client) *GitHub {
	if api == "" {
		api = DefaultGitHubAPI
	}
	return &GitHub{repo: repo, token: token, cap: ceiling, capSet: capSet, api: api, client: client}
}

// Name is how this sink appears in the record.
func (g *GitHub) Name() string { return "github" }

// Creates is what makes the cap mandatory.
func (g *GitHub) Creates() bool { return true }

// Cap is the ceiling the playbook declared, and whether it declared one.
func (g *GitHub) Cap() (int, bool) { return g.cap, g.capSet }

// finding is one item the agent asked for. The shape is the contract between a creating
// sink and a playbook's output schema: a title, and a body.
type finding struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type report struct {
	Findings []finding `json:"findings"`
}

// Deliver creates issues up to the cap.
//
// The order is deliberate. What is already open is counted first, because the cap is a
// ceiling on what exists and not on what one run does; then a finding whose title is
// already open is skipped rather than opened again, because a runtime that reopens the
// same issue nightly is the noise the cap exists to prevent.
func (g *GitHub) Deliver(ctx context.Context, delivery Delivery) (Outcome, error) {
	if delivery.Failure != nil {
		// There is no report, so there is nothing to create. Saying so is the outcome;
		// a creating sink does not narrate a failure into a repository.
		// nilerr reads delivery.Failure as this function's own error. It is the opposite:
		// the run's failure is the input, and a sink reporting that it created nothing is
		// an outcome rather than a failure of its own.
		return Outcome{ //nolint:nilerr
			Status: StatusSkipped,
			Detail: "no report: " + delivery.Failure.Error(),
		}, nil
	}

	var decoded report
	if err := json.Unmarshal(delivery.Report, &decoded); err != nil {
		return Outcome{}, fmt.Errorf("the report is not a shape this sink can create from: %w", err)
	}
	if len(decoded.Findings) == 0 {
		return Outcome{Status: StatusSkipped, Detail: "the report holds no findings"}, nil
	}

	open, err := g.openIssues(ctx)
	if err != nil {
		return Outcome{}, err
	}

	room := g.cap - len(open)
	if room <= 0 {
		// Capped is not failed. The cap doing its job is the system working as declared,
		// and recording it as a failure would train an operator to ignore the status.
		return Outcome{
			Status:       StatusCapped,
			ItemsSkipped: len(decoded.Findings),
			Detail: fmt.Sprintf("%d issue(s) already open against a cap of %d",
				len(open), g.cap),
		}, nil
	}

	outcome := Outcome{Status: StatusCreated}
	var details []string
	for _, item := range decoded.Findings {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			outcome.ItemsSkipped++
			details = append(details, "a finding with no title")
			continue
		}
		if open[title] {
			outcome.ItemsSkipped++
			details = append(details, fmt.Sprintf("%q is already open", title))
			continue
		}
		if outcome.ItemsCreated >= room {
			outcome.ItemsSkipped++
			continue
		}

		if err := g.create(ctx, title, item.Body, delivery); err != nil {
			// Partial failure: what was created stays created, and the outcome says
			// which items got through. Returning here without the counts would leave an
			// operator unable to tell what to do next.
			outcome.Status = StatusFailed
			// Everything not created is skipped, the one that failed included. Counting
			// the remainder and forgetting the failing item left the outcome unable to
			// account for the report it was given, which is the number an operator uses
			// to decide what to do next.
			outcome.ItemsSkipped = len(decoded.Findings) - outcome.ItemsCreated
			details = append(details, fmt.Sprintf("%q failed: %s", title, err))
			outcome.Detail = strings.Join(details, "; ")
			return outcome, nil
		}
		outcome.ItemsCreated++
		open[title] = true
	}

	if outcome.ItemsCreated == 0 {
		outcome.Status = StatusSkipped
	}
	if outcome.ItemsSkipped > 0 && outcome.ItemsCreated == room {
		details = append(details, fmt.Sprintf("stopped at the cap of %d", g.cap))
	}
	outcome.Detail = strings.Join(details, "; ")
	return outcome, nil
}

// openIssues is what this runtime created and has not closed, by title.
func (g *GitHub) openIssues(ctx context.Context) (map[string]bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/issues?state=open&labels=%s&per_page=100",
		g.api, g.repo, url.QueryEscape(Marker))

	body, err := g.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var issues []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(body, &issues); err != nil {
		return nil, fmt.Errorf("listing open issues: the answer was not a list of issues")
	}
	open := make(map[string]bool, len(issues))
	for _, issue := range issues {
		open[issue.Title] = true
	}
	return open, nil
}

func (g *GitHub) create(ctx context.Context, title, body string, delivery Delivery) error {
	payload, err := json.Marshal(map[string]any{
		"title": title,
		"body": fmt.Sprintf("%s\n\n---\nOpened by the playbook `%s`, run `%s`.",
			body, delivery.PlaybookName, delivery.RunID),
		"labels": []string{Marker},
	})
	if err != nil {
		return err
	}
	_, err = g.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/issues", g.api, g.repo), payload)
	return err
}

func (g *GitHub) do(ctx context.Context, method, endpoint string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, postTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		request.Header.Set("Authorization", "Bearer "+g.token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	client := g.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		// The repository is not named in the error either: it is a configured value, and
		// a log line written before the store's redactor sees it would carry it.
		return nil, fmt.Errorf("the request to GitHub failed")
	}
	defer func() { _ = response.Body.Close() }()

	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading GitHub's answer: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub answered HTTP %d", response.StatusCode)
	}
	return answer, nil
}
