package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/etcd"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// coordination is the guard's backend for this invocation: which guarantee it has, and
// what to close afterwards.
type coordination struct {
	config      guard.Config
	coordinator guard.Coordinator
	client      *clientv3.Client
	// configured says a backend was named. A backend that is named and cannot be reached
	// is a refusal, never a fall back to the file lock (FR-107).
	configured bool
}

func (c *coordination) close() {
	if c.client != nil {
		_ = c.client.Close()
	}
}

// openCoordination reads coordination.json and builds what it names. Its absence is the
// single-host deployment of FR-109; its presence is etcd, whether or not etcd answers.
func openCoordination(stateDir string, cfg *config.Config, manager *run.Manager) (*coordination, error) {
	resolve := func(text string) (string, error) { return cfg.Interpolate(text, nil) }
	declared, configured, err := guard.LoadConfig(stateDir, resolve)
	if err != nil {
		return nil, err
	}
	if !configured {
		return &coordination{config: declared, coordinator: manager.FileLock()}, nil
	}

	client, err := etcd.NewClient(*declared.Etcd)
	if err != nil {
		return nil, err
	}
	return &coordination{
		config:      declared,
		coordinator: etcd.New(client, etcd.Options{Prefix: declared.Etcd.Prefix}),
		client:      client,
		configured:  true,
	}, nil
}

// describe is what `serve` says about the guard before it arms anything (FR-109). The
// reach comes first, because it is the guarantee, and the single-host line says what
// that guarantee does not cover rather than leaving it to be inferred.
func (c *coordination) describe() string {
	if !c.configured {
		return "guard: single-host — no coordination backend is configured, so two hosts " +
			"running these playbooks would each run them"
	}
	return fmt.Sprintf("guard: cross-host, etcd at %d endpoint(s) under prefix %s, claim expiry %s",
		len(c.config.Etcd.Endpoints), c.config.Etcd.Prefix, c.config.ClaimExpiry)
}

// name is what a refusal calls this backend. Empty for the file lock: a refusal there
// is about this host, which the record already says.
func (c *coordination) name() string {
	if !c.configured {
		return ""
	}
	return "etcd at " + strings.Join(c.config.Etcd.Endpoints, ", ")
}

// reachable asks the backend one question, under the decision bound. A backend that does
// not answer does not stop `serve`: it may be back before the first trigger, and every
// trigger until then is refused naming it.
func (c *coordination) reachable(ctx context.Context) error {
	if c.client == nil {
		return nil
	}
	probe, cancel := context.WithTimeout(ctx, c.config.DecisionBound)
	defer cancel()
	if _, err := c.client.Get(probe, c.config.Etcd.Prefix); err != nil {
		return err
	}
	return nil
}

// unreachable is what `serve` prints when the backend named did not answer at startup.
func (c *coordination) unreachable(err error) string {
	return fmt.Sprintf("guard: cross-host, etcd at %d endpoint(s) — NOT reachable at startup "+
		"(%s); every trigger will be refused until it answers",
		len(c.config.Etcd.Endpoints), strings.TrimSpace(err.Error()))
}

// hostName is the host a claim names as its holder. A name that cannot be read is not
// worth failing over: it costs a refusal its detail, never the claim.
func hostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown-host"
	}
	return name
}

// instanceID distinguishes two processes of one deployment on one host, which is what a
// refusal needs to name when both can run the same playbook.
func instanceID() string {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "unknown-instance"
	}
	return hex.EncodeToString(suffix)
}
