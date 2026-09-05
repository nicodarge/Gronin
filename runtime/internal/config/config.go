// Package config holds the deployment configuration, the state directory, and the
// values a playbook may interpolate against. It never reads the process environment on
// a playbook's behalf.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Value is one deployment configuration entry. Secret is set by the operator when the
// value is one, rather than guessed from its name: a heuristic that reads "token" and
// "webhook" as secret and "endpoint" as not is wrong on the first deployment that
// disagrees, and it is wrong silently.
type Value struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

// Config is what a deployment knows about itself. Playbooks interpolate against it and
// carry none of it.
type Config struct {
	stateDir string
	values   map[string]Value
}

const configFile = "config.json"

// Load reads the configuration under stateDir, creating neither the directory nor the
// file. A deployment with no configuration yet is not an error: it is a deployment
// whose playbooks interpolate nothing.
func Load(stateDir string) (*Config, error) {
	c := &Config{stateDir: stateDir, values: map[string]Value{}}

	// The path is this deployment's own state directory and a fixed name; there is no
	// caller-supplied component in it.
	data, err := os.ReadFile(filepath.Join(stateDir, configFile)) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading configuration: %w", err)
	}
	if err := json.Unmarshal(data, &c.values); err != nil {
		return nil, fmt.Errorf("parsing configuration: %w", err)
	}
	return c, nil
}

// StateDir is the one directory this deployment writes to.
func (c *Config) StateDir() string { return c.stateDir }

// Set stores a value and writes the configuration out.
func (c *Config) Set(key string, value Value) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	c.values[key] = value
	return c.save()
}

// Keys returns every configured key, ordered.
func (c *Config) Keys() []string {
	keys := make([]string, 0, len(c.values))
	for key := range c.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Get returns a value and whether it is configured.
func (c *Config) Get(key string) (Value, bool) {
	value, ok := c.values[key]
	return value, ok
}

// Secrets returns the configured secret values, which is what seeds the redactor.
// Empty values are left out: redacting the empty string would replace every boundary
// between characters in every record.
func (c *Config) Secrets() []string {
	var secrets []string
	for _, key := range c.Keys() {
		if value := c.values[key]; value.Secret && value.Value != "" {
			secrets = append(secrets, value.Value)
		}
	}
	return secrets
}

func (c *Config) save() error {
	if err := os.MkdirAll(c.stateDir, 0o700); err != nil {
		return fmt.Errorf("creating the state directory: %w", err)
	}
	data, err := json.MarshalIndent(c.values, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding configuration: %w", err)
	}
	// 0600 rather than 0644: this file holds the deployment's secret values, and it is
	// written by a command an operator runs by hand.
	return os.WriteFile(filepath.Join(c.stateDir, configFile), append(data, '\n'), 0o600)
}
