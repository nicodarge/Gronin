package sources_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/sources"
	"github.com/nicodarge/Gronin/runtime/internal/testsecret"
)

// testConfig is a configuration holding one secret value and one that is not marked
// secret, which every probe below is judged against.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("alerts_hook_secret", config.Value{Value: testsecret.Value, Secret: true}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("ops_channel", config.Value{Value: "#ops"}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// write puts a catalogue in a state directory of its own and returns it.
func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sources.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A deployment with no sources.json configures no source, and a webhook trigger naming
// one is refused at load rather than this failing.
func TestSourcesAbsentIsAnEmptyCatalogue(t *testing.T) {
	catalog, err := sources.Load(t.TempDir(), testConfig(t))
	if err != nil {
		t.Fatalf("an absent catalogue was an error: %v", err)
	}
	if names := catalog.Names(); len(names) != 0 {
		t.Fatalf("an absent catalogue holds %v", names)
	}
}

// The three shapes contracts/cli.md documents, with the defaults filled in.
func TestSourcesAcceptsTheDocumentedShapes(t *testing.T) {
	dir := write(t, `{
	  "alerts": {
	    "secret": "${config.alerts_hook_secret}",
	    "signature_header": "X-Grafana-Alerting-Signature"
	  },
	  "forge": {
	    "secret": "${config.alerts_hook_secret}",
	    "signature_header": "X-Forgejo-Signature",
	    "identity": "/delivery_uuid",
	    "replay_window": "30m"
	  },
	  "github": {
	    "secret": "${config.alerts_hook_secret}",
	    "signature_header": "X-Hub-Signature-256",
	    "signature_prefix": "sha256="
	  }
	}`)
	catalog, err := sources.Load(dir, testConfig(t))
	if err != nil {
		t.Fatalf("the documented shapes were refused: %v", err)
	}
	if names := catalog.Names(); len(names) != 3 {
		t.Fatalf("names = %v", names)
	}

	alerts, ok := catalog.Get("alerts")
	if !ok || alerts.SignatureHeader != "X-Grafana-Alerting-Signature" ||
		alerts.SignaturePrefix != "" || alerts.Identity != "" ||
		alerts.ReplayWindow != sources.DefaultReplayWindow {
		t.Fatalf("alerts resolved as %+v", alerts)
	}

	forge, ok := catalog.Get("forge")
	if !ok || forge.Identity != "/delivery_uuid" || forge.ReplayWindow != 30*time.Minute {
		t.Fatalf("forge resolved as %+v", forge)
	}

	github, ok := catalog.Get("github")
	if !ok || github.SignaturePrefix != "sha256=" {
		t.Fatalf("github resolved as %+v", github)
	}
}

// An absent file is no source, not an error — the corpus above already shows what one
// configures.
func TestSourcesAbsentFileIsNoSource(t *testing.T) {
	catalog, err := sources.Load(filepath.Join(t.TempDir(), "not-created-yet"), testConfig(t))
	if err != nil {
		t.Fatalf("an absent catalogue was an error: %v", err)
	}
	if names := catalog.Names(); len(names) != 0 {
		t.Fatalf("an absent catalogue holds %v", names)
	}
}

// Every refusal T010 names, probed one at a time on an otherwise valid entry, so each is
// shown refused for its own reason and none for another's.
func TestSourcesRefusesEachProbeForItsOwnReason(t *testing.T) {
	valid := map[string]any{
		"secret":           "${config.alerts_hook_secret}",
		"signature_header": "X-Grafana-Alerting-Signature",
	}

	for name, probe := range map[string]struct {
		field string
		says  string
	}{
		"an unknown key": {"", "unknown field"},
		"a name that is not a slug": {
			"", "is not a lowercase slug",
		},
		"a missing signature header": {
			"signature_header", "is missing",
		},
		"a malformed signature header": {
			"signature_header", "is not a valid header field name",
		},
		"a literal secret": {
			"secret", "is not a single ${config.x} reference",
		},
		"a secret naming an unconfigured key": {
			"secret", "is not configured",
		},
		"a secret naming a key not marked secret": {
			"secret", "is not marked secret",
		},
		"an identity that is not a JSON Pointer": {
			"identity", "is not a JSON Pointer",
		},
		"a replay window that does not parse": {
			"replay_window", "is not a duration",
		},
		"a replay window that is not positive": {
			"replay_window", "is not positive",
		},
	} {
		t.Run(name, func(t *testing.T) {
			entry := map[string]any{}
			for k, v := range valid {
				entry[k] = v
			}
			sourceName := "alerts"
			switch name {
			case "an unknown key":
				entry["mode"] = "async"
			case "a name that is not a slug":
				sourceName = "Alerts"
			case "a missing signature header":
				delete(entry, "signature_header")
			case "a malformed signature header":
				entry["signature_header"] = "bad header\nname"
			case "a literal secret":
				entry["secret"] = testsecret.Value
			case "a secret naming an unconfigured key":
				entry["secret"] = "${config.does_not_exist}"
			case "a secret naming a key not marked secret":
				entry["secret"] = "${config.ops_channel}"
			case "an identity that is not a JSON Pointer":
				entry["identity"] = "delivery_uuid"
			case "a replay window that does not parse":
				entry["replay_window"] = "10 minutes"
			case "a replay window that is not positive":
				entry["replay_window"] = "0m"
			}

			body, err := json.Marshal(map[string]any{sourceName: entry})
			if err != nil {
				t.Fatal(err)
			}
			dir := write(t, string(body))
			_, err = sources.Load(dir, testConfig(t))
			if err == nil {
				t.Fatalf("%q was accepted", name)
			}
			if !strings.Contains(err.Error(), probe.says) {
				t.Fatalf("refused, but not for its reason.\n  want: %s\n  got:  %v", probe.says, err)
			}
			if probe.field != "" && !strings.Contains(err.Error(), probe.field) {
				t.Fatalf("refused, but does not name the field %q: %v", probe.field, err)
			}
		})
	}
}

// No Summary contains the sentinel set as a source's secret (SC-316's shape half): a
// listing must not resolve what it prints.
func TestSummaryNeverContainsTheSecret(t *testing.T) {
	dir := write(t, `{"alerts": {
	  "secret": "${config.alerts_hook_secret}",
	  "signature_header": "X-Grafana-Alerting-Signature"
	}}`)
	catalog, err := sources.Load(dir, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(catalog.Summary("alerts"), testsecret.Value) {
		t.Fatal("the summary holds the secret")
	}

	secret, err := catalog.Secret("alerts")
	if err != nil || secret != testsecret.Value {
		t.Fatalf("Secret(\"alerts\") = %q, %v; want %q, nil", secret, err, testsecret.Value)
	}
}
