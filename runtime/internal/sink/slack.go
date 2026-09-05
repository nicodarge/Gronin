package sink

import "net/http"

// NewSlack returns the Slack messaging sink. Slack's incoming webhook takes the text in
// `text` — the only thing that differs from Discord, which is why they share a body.
func NewSlack(url string, client *http.Client) Sink {
	return webhook{name: "slack", url: url, textField: "text", client: client}
}
