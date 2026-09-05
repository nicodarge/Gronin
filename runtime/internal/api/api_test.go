package api_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/api"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// FR-036. The refusal is at bind time rather than at request time: a deployment must not
// be one restart away from an unauthenticated listener that can invoke playbooks.
func TestBindingOffLoopbackRequiresACredential(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		if err := api.CheckAddress(address, ""); err != nil {
			t.Errorf("%s was refused with no credential: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "192.0.2.10:8080", "[::]:8080"} {
		if err := api.CheckAddress(address, ""); !errors.Is(err, api.ErrRemoteWithoutCredential) {
			t.Errorf("%s was accepted with no credential: %v", address, err)
		}
		if err := api.CheckAddress(address, "a-token"); err != nil {
			t.Errorf("%s was refused with a credential: %v", address, err)
		}
	}
}

func TestAnUnbindableAddressIsRefused(t *testing.T) {
	if err := api.CheckAddress("", ""); err == nil {
		t.Error("an empty address was accepted")
	}
	if err := api.CheckAddress("no-port", ""); err == nil {
		t.Error("an address with no port was accepted")
	}
}

func TestATokenIsRequiredWhenOneIsConfigured(t *testing.T) {
	store := storeWithARun(t)
	server := httptest.NewServer(api.New(store, "the-token").Handler())
	defer server.Close()

	t.Run("without one", func(t *testing.T) {
		response := get(t, server.URL+"/runs", "")
		if response != http.StatusUnauthorized {
			t.Fatalf("status = %d", response)
		}
	})
	t.Run("with the wrong one", func(t *testing.T) {
		if response := get(t, server.URL+"/runs", "the-toke"); response != http.StatusUnauthorized {
			t.Fatalf("a prefix of the token was accepted: %d", response)
		}
	})
	t.Run("with it", func(t *testing.T) {
		if response := get(t, server.URL+"/runs", "the-token"); response != http.StatusOK {
			t.Fatalf("status = %d", response)
		}
	})
}

func TestTheAPIReadsTheRecordBack(t *testing.T) {
	store := storeWithARun(t)
	server := httptest.NewServer(api.New(store, "").Handler())
	defer server.Close()

	body := getBody(t, server.URL+"/runs")
	runs, _ := body["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %v", body["runs"])
	}
	first, _ := runs[0].(map[string]any)
	if first["playbook"] != "drift-check" || first["status"] != "succeeded" {
		t.Fatalf("run = %v", first)
	}

	shown := getBody(t, server.URL+"/runs/run-1")
	one, _ := shown["run"].(map[string]any)
	if one["id"] != "run-1" {
		t.Fatalf("run = %v", one)
	}
	for _, key := range []string{"gathered_inputs", "tool_calls", "refused_actions", "sink_outcomes"} {
		if _, present := shown[key]; !present {
			t.Errorf("the record's %s are not served", key)
		}
	}
}

func TestAnUnknownRunIsNotFound(t *testing.T) {
	store := storeWithARun(t)
	server := httptest.NewServer(api.New(store, "").Handler())
	defer server.Close()

	if status := get(t, server.URL+"/runs/nope", ""); status != http.StatusNotFound {
		t.Fatalf("status = %d", status)
	}
}

func storeWithARun(t *testing.T) *record.Store {
	t.Helper()
	store, err := record.Open(t.Context(), filepath.Join(t.TempDir(), "record"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := t.Context()
	if err := store.CreateRun(ctx, record.Run{
		ID: "run-1", PlaybookName: "drift-check", TriggerKind: record.TriggerManual,
		Status: record.StatusRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, record.Run{
		ID: "run-1", Status: record.StatusSucceeded, EndedAt: time.Now(), CostUSD: 0.01,
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func get(t *testing.T, url, token string) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func getBody(t *testing.T, url string) map[string]any {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}
