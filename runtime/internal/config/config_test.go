package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/config"
)

func TestSetPersistsAndLoadReadsBack(t *testing.T) {
	dir := t.TempDir()

	first, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Set("channel", config.Value{Value: "#ops"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Set("webhook", config.Value{Value: "https://example.com/hook", Secret: true}); err != nil {
		t.Fatal(err)
	}

	second, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Keys(); !slices.Equal(got, []string{"channel", "webhook"}) {
		t.Fatalf("keys = %v", got)
	}
	value, ok := second.Get("webhook")
	if !ok || value.Value != "https://example.com/hook" || !value.Secret {
		t.Fatalf("webhook read back as %+v (present: %v)", value, ok)
	}
}

func TestLoadOfADeploymentWithNoConfigurationIsNotAnError(t *testing.T) {
	c, err := config.Load(filepath.Join(t.TempDir(), "not-created-yet"))
	if err != nil {
		t.Fatalf("a deployment that has configured nothing is not an error: %v", err)
	}
	if got := c.Keys(); len(got) != 0 {
		t.Fatalf("keys = %v, want none", got)
	}
}

// The file holds the deployment's secret values.
func TestTheConfigurationFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	c, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Set("webhook", config.Value{Value: "s", Secret: true}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("mode is %o; anything readable beyond the owner is too wide", mode)
	}
}

func TestSecretsSeedsTheRedactorWithSecretsOnly(t *testing.T) {
	c := configWith(t, map[string]config.Value{
		"channel": {Value: "#ops"},
		"webhook": {Value: "https://example.com/hook", Secret: true},
		"token":   {Value: "t0ken", Secret: true},
		"blank":   {Value: "", Secret: true},
	})

	got := c.Secrets()
	if !slices.Equal(got, []string{"t0ken", "https://example.com/hook"}) &&
		!slices.Equal(got, []string{"https://example.com/hook", "t0ken"}) {
		t.Fatalf("secrets = %v", got)
	}
	// An empty secret would make the redactor replace every position in every record.
	if slices.Contains(got, "") {
		t.Fatal("an empty value was offered to the redactor")
	}
}

func TestSetRefusesAKeyNoReferenceCouldName(t *testing.T) {
	c := configWith(t, nil)

	for _, key := range []string{"", "with space", "with.dot", "with${brace}", "with/slash"} {
		if err := c.Set(key, config.Value{Value: "x"}); err == nil {
			t.Errorf("key %q was accepted; no ${config.%s} could ever read it", key, key)
		}
	}
	for _, key := range []string{"channel", "discord_webhook", "a-b", "A1"} {
		if err := c.Set(key, config.Value{Value: "x"}); err != nil {
			t.Errorf("key %q was refused: %v", key, err)
		}
	}
}
