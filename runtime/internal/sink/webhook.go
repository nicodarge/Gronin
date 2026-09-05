package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// requestTimeout bounds one request, whether that is a message being delivered or one
// page of a listing being read. A sink that hangs holds the run open long after the
// agent stage has finished paying for itself.
const requestTimeout = 30 * time.Second

// maxMessage is the largest body a messaging sink sends. Both destinations refuse more,
// and a report that is longer is better read in the record than truncated into a chat
// channel — so the message says where to look rather than trying to carry everything.
const maxMessage = 1800

// webhook is the shared half of the two messaging sinks: both post JSON to a URL the
// deployment configured, and differ only in the field name that carries the text.
type webhook struct {
	name      string
	url       string
	textField string
	client    *http.Client
}

func (w webhook) Name() string     { return w.name }
func (w webhook) Creates() bool    { return false }
func (w webhook) Cap() (int, bool) { return 0, false }

func (w webhook) Deliver(ctx context.Context, delivery Delivery) (Outcome, error) {
	body, err := json.Marshal(map[string]any{w.textField: message(delivery)})
	if err != nil {
		return Outcome{}, fmt.Errorf("encoding the message: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return Outcome{}, fmt.Errorf("building the request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	client := w.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		// The URL is a configured secret in most deployments, so it is not in the
		// error: the record's redactor would catch it, but a log line written before
		// the store sees it would not.
		return Outcome{}, fmt.Errorf("posting to %s: the request failed", w.name)
	}
	defer func() { _ = response.Body.Close() }()
	// Read and discard so the connection can be reused rather than dropped.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Outcome{Detail: fmt.Sprintf("HTTP %d", response.StatusCode)},
			fmt.Errorf("%s refused the message with HTTP %d", w.name, response.StatusCode)
	}
	return Outcome{Status: StatusDelivered}, nil
}

// message is what a messaging sink says. When the runtime refused the report, it sends
// the refusal and not the content: a chat channel carrying output the runtime has just
// decided it cannot read is a bad action produced by a bad run.
func message(delivery Delivery) string {
	var out strings.Builder
	fmt.Fprintf(&out, "%s — run %s\n", delivery.PlaybookName, delivery.RunID)

	if delivery.Failure != nil {
		fmt.Fprintf(&out, "\nThe run produced no usable report:\n%s", delivery.Failure)
		return truncate(out.String())
	}

	out.WriteString("\n")
	out.Write(delivery.Report)
	return truncate(out.String())
}

// truncate cuts on a rune boundary. A byte cut lands mid-character on any report holding
// accented text, and what reaches the destination is then invalid UTF-8 rather than a
// shortened message.
func truncate(text string) string {
	if len(text) <= maxMessage {
		return text
	}
	cut := maxMessage
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… truncated; the full report is in the run record."
}
