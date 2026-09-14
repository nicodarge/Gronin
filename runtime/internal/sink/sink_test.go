package sink_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/sink"
)

type captured struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (c *captured) record(body map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, body)
}

func (c *captured) all() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.bodies...)
}

// A destination on loopback: the suite has no route anywhere else, which is the point.
func destination(t *testing.T, status int) (*captured, *http.Client, string) {
	t.Helper()
	got := &captured{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		got.record(body)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return got, server.Client(), server.URL
}

func TestTheMessagingSinksPostTheReport(t *testing.T) {
	for name, build := range map[string]struct {
		make  func(string, *http.Client) sink.Sink
		field string
	}{
		"discord": {sink.NewDiscord, "content"},
		"slack":   {sink.NewSlack, "text"},
	} {
		t.Run(name, func(t *testing.T) {
			got, client, url := destination(t, http.StatusNoContent)
			one := build.make(url, client)

			outcome, err := one.Deliver(t.Context(), sink.Delivery{
				PlaybookName: "drift-check", RunID: "run-1",
				Report: []byte(`{"findings":[{"id":"one"}]}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != sink.StatusDelivered {
				t.Fatalf("status = %q", outcome.Status)
			}
			if one.Creates() {
				t.Fatal("a messaging sink reported that it creates things")
			}

			bodies := got.all()
			if len(bodies) != 1 {
				t.Fatalf("%d requests", len(bodies))
			}
			text, _ := bodies[0][build.field].(string)
			if !strings.Contains(text, "drift-check") || !strings.Contains(text, "run-1") {
				t.Fatalf("the message does not say which run it is: %q", text)
			}
			if !strings.Contains(text, "findings") {
				t.Fatalf("the report is not in the message: %q", text)
			}
		})
	}
}

// FR-014's second half, and Principle II. When the runtime refused the report, what is
// sent is the refusal — not the content the runtime has just decided it cannot read.
func TestARefusedReportSendsTheRefusalAndNotTheContent(t *testing.T) {
	got, client, url := destination(t, http.StatusNoContent)
	one := sink.NewDiscord(url, client)

	_, err := one.Deliver(t.Context(), sink.Delivery{
		PlaybookName: "drift-check", RunID: "run-1",
		Failure: errors.New("the report does not satisfy the declared output schema: findings is missing"),
	})
	if err != nil {
		t.Fatal(err)
	}

	text, _ := got.all()[0]["content"].(string)
	if !strings.Contains(text, "does not satisfy") {
		t.Fatalf("the refusal is not in the message: %q", text)
	}
	if strings.Contains(text, "no usable report") == false {
		t.Fatalf("the message does not say the run produced nothing usable: %q", text)
	}
}

func TestASinkFailureIsRecordedRatherThanRaised(t *testing.T) {
	_, client, url := destination(t, http.StatusInternalServerError)

	outcomes, err := sink.DeliverAll(t.Context(), []sink.Sink{sink.NewDiscord(url, client)},
		sink.Delivery{PlaybookName: "p", RunID: "run-1", Report: []byte(`{}`)}, nil)
	if err != nil {
		t.Fatalf("no gate refused, yet: %v", err)
	}

	if len(outcomes) != 1 {
		t.Fatalf("%d outcomes", len(outcomes))
	}
	if outcomes[0].Status != sink.StatusFailed {
		t.Fatalf("status = %q", outcomes[0].Status)
	}
	if !strings.Contains(outcomes[0].Detail, "500") {
		t.Fatalf("the outcome does not say what happened: %q", outcomes[0].Detail)
	}
}

// FR-025. One sink's outage is not the other sink's problem, and the report is not
// discarded because a destination was down.
func TestOneSinkFailingDoesNotStopTheOthers(t *testing.T) {
	_, badClient, badURL := destination(t, http.StatusInternalServerError)
	goodGot, goodClient, goodURL := destination(t, http.StatusOK)

	outcomes, err := sink.DeliverAll(t.Context(), []sink.Sink{
		sink.NewDiscord(badURL, badClient),
		sink.NewSlack(goodURL, goodClient),
	}, sink.Delivery{PlaybookName: "p", RunID: "run-1", Report: []byte(`{"findings":[]}`)}, nil)
	if err != nil {
		t.Fatalf("no gate refused, yet: %v", err)
	}

	if len(outcomes) != 2 {
		t.Fatalf("%d outcomes", len(outcomes))
	}
	if outcomes[0].Status != sink.StatusFailed || outcomes[1].Status != sink.StatusDelivered {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if len(goodGot.all()) != 1 {
		t.Fatal("the second sink was never reached")
	}
}

func TestAVeryLongReportIsTruncatedWithAPointerToTheRecord(t *testing.T) {
	got, client, url := destination(t, http.StatusOK)

	_, err := sink.NewSlack(url, client).Deliver(t.Context(), sink.Delivery{
		PlaybookName: "p", RunID: "run-1",
		Report: []byte(strings.Repeat("x", 100_000)),
	})
	if err != nil {
		t.Fatal(err)
	}

	text, _ := got.all()[0]["text"].(string)
	if len(text) > 4000 {
		t.Fatalf("the message is %d bytes; both destinations refuse that", len(text))
	}
	if !strings.Contains(text, "run record") {
		t.Fatalf("a truncated message does not say where the rest is: %q", text[len(text)-120:])
	}
}

func TestBuildRefusesATypeThisDeploymentDoesNotImplement(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{
		{Type: "carrier-pigeon", Config: map[string]any{"webhook": "https://example.com"}},
	}, sink.BuildOptions{})

	if len(problems) != 1 {
		t.Fatalf("problems = %v", problems)
	}
	if !errors.Is(problems[0], sink.ErrUnknownType) {
		t.Fatalf("err = %v, want ErrUnknownType", problems[0])
	}
	// A refusal says what would be accepted, not only what was not.
	for _, known := range sink.Types() {
		if !strings.Contains(problems[0].Error(), known) {
			t.Errorf("the refusal does not name %q as an accepted type: %v", known, problems[0])
		}
	}
}

func TestBuildReportsEveryProblemRatherThanTheFirst(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{
		{Type: "nope", Config: map[string]any{}},
		{Type: "discord", Config: map[string]any{}},
		{Type: "also-nope", Config: map[string]any{}},
	}, sink.BuildOptions{})

	if len(problems) != 3 {
		t.Fatalf("%d problems reported, want 3: %v", len(problems), problems)
	}
}

// A playbook holds the reference; the deployment holds the value. That separation is
// what makes a playbook committable.
func TestBuildResolvesTheWebhookThroughTheDeployment(t *testing.T) {
	sinks, problems := sink.Build([]sink.Declaration{
		{Type: "discord", Config: map[string]any{"webhook": "${config.discord_webhook}"}},
	}, sink.BuildOptions{
		Interpolate: func(text string) (string, error) {
			if text != "${config.discord_webhook}" {
				return "", errors.New("unexpected: " + text)
			}
			return "https://example.com/hook", nil
		},
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if len(sinks) != 1 || sinks[0].Name() != "discord" {
		t.Fatalf("sinks = %v", sinks)
	}
}

func TestBuildRefusesASinkWithNoDestination(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{
		{Type: "slack", Config: map[string]any{}},
	}, sink.BuildOptions{})

	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "webhook") {
		t.Fatalf("problems = %v", problems)
	}
}

// The cap contract exists on the interface before a creating sink does, so the load gate
// has something to refuse against.
func TestAMessagingSinkDeclaresNoCapAndNeedsNone(t *testing.T) {
	for _, one := range []sink.Sink{sink.NewDiscord("u", nil), sink.NewSlack("u", nil)} {
		if one.Creates() {
			t.Errorf("%s says it creates things", one.Name())
		}
		if _, declared := one.Cap(); declared {
			t.Errorf("%s declares a cap it does not need", one.Name())
		}
	}
}

// A sink that creates things, which this deployment does not yet ship. It exists so the
// cap contract is probed against what it refuses rather than described in a comment.
type creating struct {
	cap      int
	declared bool
}

func (creating) Name() string  { return "creating" }
func (creating) Creates() bool { return true }
func (c creating) Cap() (int, bool) {
	return c.cap, c.declared
}
func (creating) Deliver(context.Context, sink.Delivery) (sink.Outcome, error) {
	return sink.Outcome{Status: sink.StatusCreated, ItemsCreated: 1}, nil
}

// FR-006. An uncapped creator gets muted within a month, and the useful signal is lost
// along with the noise.
func TestACreatingSinkWithoutACapIsRefused(t *testing.T) {
	if err := sink.CheckCap(creating{declared: false}); !errors.Is(err, sink.ErrNoCap) {
		t.Fatalf("err = %v, want ErrNoCap", err)
	}
	// A ceiling that was never declared, which is the only case the "not declared" branch
	// answers on its own: every other undeclared cap is also zero, so the ceiling check
	// below answers those and this branch could be deleted without a test noticing. It
	// was — a mutant disabling it survived, and this is the case that kills it. A sink
	// whose own default ceiling is not zero is exactly what it guards against.
	if err := sink.CheckCap(creating{cap: 3, declared: false}); !errors.Is(err, sink.ErrNoCap) {
		t.Fatalf("a ceiling nobody declared was accepted: %v", err)
	}
	if err := sink.CheckCap(creating{cap: 0, declared: true}); !errors.Is(err, sink.ErrNoCap) {
		t.Fatalf("a ceiling of zero was accepted: %v", err)
	}
	if err := sink.CheckCap(creating{cap: 3, declared: true}); err != nil {
		t.Fatalf("a declared ceiling was refused: %v", err)
	}
	// And a sink that creates nothing needs none.
	if err := sink.CheckCap(sink.NewDiscord("u", nil)); err != nil {
		t.Fatalf("a messaging sink was asked for a cap: %v", err)
	}
}

func TestASinkThatNamesNoTypeIsRefusedByName(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{{Type: "", Config: map[string]any{}}},
		sink.BuildOptions{})

	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "names no type") {
		t.Fatalf("problems = %v", problems)
	}
}

// SC-315, the sink half. FR-325: a webhook playbook's sink may not reference the
// payload, and the sink refuses it again when built rather than trusting the load gate
// alone. The same declarations build for a manual playbook, which may still interpolate
// the trigger everywhere but the label (the sink's pre-existing rule, unrelated to this
// option).
func TestASinkRefusesATriggerReferenceForAWebhookPlaybook(t *testing.T) {
	declared := []sink.Declaration{
		{Type: "discord", Config: map[string]any{"webhook": "${trigger.url}"}},
		{Type: "slack", Config: map[string]any{"webhook": "${trigger.url}"}},
		{Type: "github", Config: map[string]any{"repo": "${trigger.repo}", "cap": 3}},
		{Type: "github", Config: map[string]any{"repo": "o/r", "token": "${trigger.tok}", "cap": 3}},
	}

	_, problems := sink.Build(declared, sink.BuildOptions{RefusePayloadReference: true})
	if len(problems) != len(declared) {
		t.Fatalf("problems = %v, want one per declaration", problems)
	}
	for at, problem := range problems {
		if !strings.Contains(problem.Error(), "${trigger.") {
			t.Errorf("declaration %d: refused for another reason: %v", at, problem)
		}
	}

	// The option is what changes, not the deployment's interpolation: the same
	// declarations build for a manual playbook.
	interpolate := func(string) (string, error) { return "https://example.com/hook", nil }
	sinks, problems := sink.Build(declared[:2], sink.BuildOptions{Interpolate: interpolate})
	if len(problems) != 0 || len(sinks) != 2 {
		t.Fatalf("sinks = %v, problems = %v", sinks, problems)
	}
}

func TestASinkWhoseValueIsNotAMappingIsRefusedByShapeNotByField(t *testing.T) {
	_, problems := sink.Build([]sink.Declaration{{Type: "discord", Config: nil}},
		sink.BuildOptions{})

	if len(problems) != 1 {
		t.Fatalf("problems = %v", problems)
	}
	if !strings.Contains(problems[0].Error(), "not a mapping") {
		t.Fatalf("the refusal blames a missing field rather than the shape: %v", problems[0])
	}
	if !strings.Contains(problems[0].Error(), "discord") {
		t.Fatalf("the refusal does not name which sink: %v", problems[0])
	}
}
