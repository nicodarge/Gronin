package sources

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/config"
)

// Source is one sender the deployment has agreed to hear from
// (specs/004-webhook/data-model.md, *Source*), resolved from sources.json.
type Source struct {
	Name string
	// SignatureHeader is where the sender puts its signature (FR-306).
	SignatureHeader string
	// SignaturePrefix is text the header value starts with before the hexadecimal MAC,
	// matched exactly. Empty by default.
	SignaturePrefix string
	// Identity is a JSON Pointer into the body, or empty for the default: the SHA-256
	// digest of the exact body bytes (FR-316).
	Identity string
	// ReplayWindow is how long an identity is remembered as a repeat (FR-317).
	ReplayWindow time.Duration

	// secretKey is the configuration key the secret reference names, resolved only by
	// Secret — never by Summary, which is what a listing calls.
	secretKey string
}

// DefaultReplayWindow is what a source gets when it declares none (plan.md, *Constraints*).
const DefaultReplayWindow = 10 * time.Minute

// Catalog is what this deployment configures.
type Catalog struct {
	entries map[string]Source
	cfg     *config.Config
}

const catalogFile = "sources.json"

// name is what a source may be called: the shape contracts/cli.md and the webhook
// trigger's own `source` property both declare.
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// headerName is what an HTTP header field may be called (RFC 7230 token characters).
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// jsonPointer is the shape a JSON Pointer may take: the empty string, naming the whole
// document, or a sequence of "/"-prefixed reference tokens. The same pattern the webhook
// trigger's schema holds `values.<name>.at` to.
var jsonPointer = regexp.MustCompile(`^(/([^~/]|~[01])*)*$`)

// secretReference is the only form a source's secret may take: a single ${config.x}
// reference, never a literal (FR-306).
var secretReference = regexp.MustCompile(`^\$\{config\.([^{}.]+)\}$`)

// entry is the catalogue as it is written, before its refusals are applied.
type entry struct {
	Secret          string `json:"secret"`
	SignatureHeader string `json:"signature_header"`
	SignaturePrefix string `json:"signature_prefix"`
	Identity        string `json:"identity"`
	ReplayWindow    string `json:"replay_window"`
}

// Load reads the source catalogue from the deployment's state directory, beside
// config.json, the way internal/mcpcatalog reads mcp_servers.json. Absent is not an
// error: it is a deployment that configures no source, and a webhook trigger naming one
// is then refused at load (FR-310).
//
// Every refusal is reported at once, naming the source and the field: fixing one round
// trip at a time is how a gate gets switched off.
func Load(stateDir string, cfg *config.Config) (*Catalog, error) {
	// The path is this deployment's own state directory and a fixed name; there is no
	// caller-supplied component in it.
	data, err := os.ReadFile(filepath.Join(stateDir, catalogFile)) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return &Catalog{entries: map[string]Source{}, cfg: cfg}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the source catalogue: %w", err)
	}

	raw := map[string]entry{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	// An unknown key is refused like an unknown playbook key: a misspelled one that is
	// ignored reads, in review, as a setting that applies.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing the source catalogue: %w", err)
	}

	names := make([]string, 0, len(raw))
	for sourceName := range raw {
		names = append(names, sourceName)
	}
	sort.Strings(names)

	var problems []error
	entries := make(map[string]Source, len(raw))
	for _, sourceName := range names {
		resolved, found := raw[sourceName].resolve(sourceName, cfg)
		problems = append(problems, found...)
		entries[sourceName] = resolved
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return &Catalog{entries: entries, cfg: cfg}, nil
}

func (e entry) resolve(sourceName string, cfg *config.Config) (Source, []error) {
	var problems []error
	refuse := func(field, found, accepted string) {
		problems = append(problems, fmt.Errorf(
			"source %q: %s %s; accepted: %s", sourceName, field, found, accepted))
	}

	if !name.MatchString(sourceName) {
		refuse("the name", "is not a lowercase slug",
			"letters, digits and hyphens, starting with a letter or digit")
	}

	resolved := Source{Name: sourceName, SignaturePrefix: e.SignaturePrefix, Identity: e.Identity}

	switch {
	case e.SignatureHeader == "":
		refuse("signature_header", "is missing", "the header a sender's signature arrives in")
	case !headerName.MatchString(e.SignatureHeader):
		refuse("signature_header", fmt.Sprintf("%q is not a valid header field name", e.SignatureHeader),
			"a token such as X-Hub-Signature-256")
	default:
		resolved.SignatureHeader = e.SignatureHeader
	}

	switch match := secretReference.FindStringSubmatch(e.Secret); {
	case e.Secret == "":
		refuse("secret", "is missing", "a ${config.x} reference to a value set with `gronin config set --secret`")
	case match == nil:
		refuse("secret", fmt.Sprintf("%q is not a single ${config.x} reference", e.Secret),
			"${config.x} naming a value marked secret, never a literal")
	default:
		key := match[1]
		value, configured := cfg.Get(key)
		switch {
		case !configured:
			refuse("secret", fmt.Sprintf("names %q, which is not configured", key),
				fmt.Sprintf("set it with `gronin config set --secret %s`", key))
		case !value.Secret:
			refuse("secret", fmt.Sprintf("names %q, which is not marked secret", key),
				"a value set with `gronin config set --secret`, so `gronin config list` never prints it")
		default:
			resolved.secretKey = key
		}
	}

	if e.Identity != "" && !jsonPointer.MatchString(e.Identity) {
		refuse("identity", fmt.Sprintf("%q is not a JSON Pointer", e.Identity),
			"a JSON Pointer into the body, such as /delivery_uuid")
	}

	resolved.ReplayWindow = DefaultReplayWindow
	if e.ReplayWindow != "" {
		window, err := time.ParseDuration(e.ReplayWindow)
		switch {
		case err != nil:
			refuse("replay_window", fmt.Sprintf("%q is not a duration", e.ReplayWindow),
				"a duration such as 10m or 30m")
		case window <= 0:
			refuse("replay_window", fmt.Sprintf("%q is not positive", e.ReplayWindow),
				"a duration above zero; a window of nothing repeats detection cannot use")
		default:
			resolved.ReplayWindow = window
		}
	}

	return resolved, problems
}

// Names is every source this deployment configures, ordered. This is what the load gate
// (playbook.Deployment.Sources) checks a webhook trigger's source against.
func (c *Catalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for sourceName := range c.entries {
		names = append(names, sourceName)
	}
	sort.Strings(names)
	return names
}

// Get is one source's declared shape by name, its secret never resolved.
func (c *Catalog) Get(sourceName string) (Source, bool) {
	source, ok := c.entries[sourceName]
	return source, ok
}

// Summary is what `gronin sources list` prints for one source, appended straight after
// its name (FR-311): its header, its identity location, its replay window, and never its
// secret. It never calls Secret, so it cannot print what that resolves. The field widths
// are the layout contracts/cli.md documents, e.g.:
//
//	alerts   header X-Grafana-Alerting-Signature   identity digest of the body   window 10m
func (c *Catalog) Summary(sourceName string) string {
	source, ok := c.entries[sourceName]
	if !ok {
		return ""
	}
	identity := "digest of the body"
	if source.Identity != "" {
		identity = source.Identity
	}
	return fmt.Sprintf("header %-30s identity %-20s window %s",
		source.SignatureHeader, identity, humanDuration(source.ReplayWindow))
}

// humanDuration mirrors internal/guard's HumanDuration exactly (contracts/cli.md's
// layout, unit by unit, largest first, no zero unit) rather than importing internal/guard
// here: this package stays a leaf catalogue beside internal/mcpcatalog, and plan.md's
// project structure does not have it reach into the guard.
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return "0s"
	}
	var text strings.Builder
	for _, part := range []struct {
		count time.Duration
		unit  string
	}{{d / time.Hour, "h"}, {d % time.Hour / time.Minute, "m"}, {d % time.Minute / time.Second, "s"}} {
		if part.count > 0 {
			text.WriteString(strconv.FormatInt(int64(part.count), 10) + part.unit)
		}
	}
	return text.String()
}

// Secret resolves a source's secret value. Only the ingress calls this, when it is
// built — a listing (Summary) never does, so a source cannot leak through it.
func (c *Catalog) Secret(sourceName string) (string, error) {
	source, ok := c.entries[sourceName]
	if !ok {
		return "", fmt.Errorf("source %q is not in the catalogue", sourceName)
	}
	value, configured := c.cfg.Get(source.secretKey)
	if !configured {
		return "", fmt.Errorf("source %q: %q is no longer configured", sourceName, source.secretKey)
	}
	return value.Value, nil
}
