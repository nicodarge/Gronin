package sink

import "net/http"

// NewDiscord returns the Discord messaging sink. Discord's webhook takes the text in
// `content`.
func NewDiscord(url string, client *http.Client) Sink {
	return webhook{name: "discord", url: url, textField: "content", client: client}
}
