package guard_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// writeCoordination lays coordination.json in a state directory of its own.
func writeCoordination(t *testing.T, document string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, guard.CoordinationFile), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// resolveReference stands for the deployment's own interpolation, which resolves a
// reference and hands back anything else as written. Text without a reference has to pass
// through for the same reason it does there: otherwise the literal a credential field
// must refuse would be refused by the resolver instead, and the refusal that matters
// would never be the one under test.
func resolveReference(text string) (string, error) {
	switch text {
	case "${config.etcd_username}":
		return "gronin", nil
	case "${config.etcd_password}":
		return "REPLACE_ME", nil
	}
	if strings.Contains(text, "${") {
		return "", errors.New(text + " is not configured")
	}
	return text, nil
}

// SC-112, the configuration half: R5's inequality probed on what it must refuse as well
// as on what it must accept, and the refusals contracts/cli.md states for the file.
func TestConfigRefusesDurationsThatCannotHold(t *testing.T) {
	for name, probe := range map[string]struct {
		document string
		refused  bool
		names    []string
	}{
		"the defaults": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"]}}`,
		},
		"durations that fit": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"]},
				"claim_expiry": "40s", "renew_every": "6s", "renew_bound": "5s", "stop_bound": "12s"}`,
		},
		// Refused only because of the stop bound: 5 + 4 + 10 + 2 = 21 against 20. One
		// refused on the other terms alone passes the mutant that drops that term.
		"refused only by the stop bound": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"]}, "claim_expiry": "20s"}`,
			refused:  true,
			names:    []string{"renew_every 5s", "renew_bound 4s", "stop_bound 10s", "claim_expiry 20s"},
		},
		"a renewal bound not below the interval": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"]},
				"renew_every": "4s", "renew_bound": "4s"}`,
			refused: true,
			names:   []string{"renew_bound 4s", "renew_every 4s"},
		},
		"an expiry that is not a whole number of seconds": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"]}, "claim_expiry": "30500ms"}`,
			refused:  true,
			names:    []string{"claim_expiry 30.5s"},
		},
		"an unknown key": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"]}, "claim_expiy": "30s"}`,
			refused:  true,
			names:    []string{"claim_expiy"},
		},
		"an unknown key inside the backend": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"], "prefixe": "gronin/"}}`,
			refused:  true,
			names:    []string{"prefixe"},
		},
		"a literal credential": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"], "password": "REPLACE_ME"}}`,
			refused:  true,
			names:    []string{"etcd.password"},
		},
		"a credential reference": {
			document: `{"etcd": {"endpoints": ["unix:///tmp/example.sock"],
				"username": "${config.etcd_username}", "password": "${config.etcd_password}"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, configured, err := guard.LoadConfig(writeCoordination(t, probe.document), resolveReference)
			if !probe.refused {
				if err != nil {
					t.Fatalf("refused a configuration that holds: %v", err)
				}
				if !configured {
					t.Fatal("a coordination.json that is there was read as absent")
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted, with claim expiry %s", cfg.ClaimExpiry)
			}
			for _, want := range probe.names {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

// The credential reaches the client as a value, and the endpoints and prefix as written.
func TestConfigReadsTheBackend(t *testing.T) {
	dir := writeCoordination(t, `{
		"etcd": {
			"endpoints": ["https://etcd-1.example.com:2379", "https://etcd-2.example.com:2379"],
			"prefix": "gronin/",
			"username": "${config.etcd_username}",
			"password": "${config.etcd_password}",
			"tls": {"ca": "/etc/gronin/ca.pem", "cert": "/etc/gronin/client.pem", "key": "/etc/gronin/client-key.pem"}
		},
		"claim_expiry": "45s", "decision_bound": "3s"
	}`)

	cfg, configured, err := guard.LoadConfig(dir, resolveReference)
	if err != nil || !configured {
		t.Fatalf("configured = %v, err = %v", configured, err)
	}
	if len(cfg.Etcd.Endpoints) != 2 || cfg.Etcd.Prefix != "gronin/" {
		t.Fatalf("endpoints = %v, prefix = %q", cfg.Etcd.Endpoints, cfg.Etcd.Prefix)
	}
	if cfg.Etcd.Username != "gronin" || cfg.Etcd.Password != "REPLACE_ME" {
		t.Fatalf("the credential did not resolve: %q / %q", cfg.Etcd.Username, cfg.Etcd.Password)
	}
	if cfg.Etcd.TLS == nil || cfg.Etcd.TLS.CA != "/etc/gronin/ca.pem" {
		t.Fatalf("tls = %+v", cfg.Etcd.TLS)
	}
	if cfg.ClaimExpiry != 45*time.Second || cfg.DecisionBound != 3*time.Second {
		t.Fatalf("claim expiry %s, decision bound %s", cfg.ClaimExpiry, cfg.DecisionBound)
	}
	// A duration left out keeps its default rather than falling to zero.
	if cfg.RenewEvery != 5*time.Second || cfg.StopBound != 10*time.Second {
		t.Fatalf("renew every %s, stop bound %s", cfg.RenewEvery, cfg.StopBound)
	}
}

// An absent file is the single-host deployment of FR-109, not a failure.
func TestConfigAbsentIsSingleHost(t *testing.T) {
	cfg, configured, err := guard.LoadConfig(t.TempDir(), resolveReference)
	if err != nil {
		t.Fatalf("an absent coordination.json was an error: %v", err)
	}
	if configured {
		t.Fatal("an absent coordination.json was read as a configured backend")
	}
	if cfg.DecisionBound != 5*time.Second {
		t.Fatalf("a single-host deployment got no decision bound: %s", cfg.DecisionBound)
	}
}
