package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// MarginFloor is the one term of FR-123's inequality a deployment cannot configure away.
// It covers a holder acting late on its own deadline — a timer firing late on a starved
// host — and not a host suspended outright, which the fence before each side effect is
// what catches (specs/002-guard/research.md §3).
const MarginFloor = 2 * time.Second

// CoordinationFile is read from the state directory, beside config.json and
// mcp_servers.json. Its absence is the single-host deployment of FR-109; its presence
// with a backend that cannot be reached is a refusal and never a fallback (FR-107).
const CoordinationFile = "coordination.json"

// Config is what a deployment declares about coordination
// (specs/002-guard/contracts/cli.md).
type Config struct {
	Etcd *EtcdConfig

	ClaimExpiry   time.Duration
	RenewEvery    time.Duration
	RenewBound    time.Duration
	StopBound     time.Duration
	DecisionBound time.Duration
}

// EtcdConfig is where the backend is and how to authenticate to it.
type EtcdConfig struct {
	Endpoints []string
	Prefix    string
	Username  string
	Password  string
	TLS       *TLSFiles
}

// TLSFiles are paths on the host running the runtime, never contents.
type TLSFiles struct{ CA, Cert, Key string }

// DefaultConfig is the claim set chosen in specs/002-guard/research.md §3.
func DefaultConfig() Config {
	return Config{
		ClaimExpiry:   30 * time.Second,
		RenewEvery:    5 * time.Second,
		RenewBound:    4 * time.Second,
		StopBound:     10 * time.Second,
		DecisionBound: 5 * time.Second,
	}
}

type document struct {
	Etcd          *etcdDocument `json:"etcd"`
	ClaimExpiry   string        `json:"claim_expiry"`
	RenewEvery    string        `json:"renew_every"`
	RenewBound    string        `json:"renew_bound"`
	StopBound     string        `json:"stop_bound"`
	DecisionBound string        `json:"decision_bound"`
}

type etcdDocument struct {
	Endpoints []string     `json:"endpoints"`
	Prefix    string       `json:"prefix"`
	Username  string       `json:"username"`
	Password  string       `json:"password"`
	TLS       *tlsDocument `json:"tls"`
}

type tlsDocument struct {
	CA   string `json:"ca"`
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

// LoadConfig reads coordination.json from stateDir and reports whether one was
// configured at all. resolve is the deployment's own interpolation, which is how a
// credential reference becomes a value and how a value marked secret is already known to
// the redactor.
func LoadConfig(stateDir string, resolve func(string) (string, error)) (Config, bool, error) {
	path := filepath.Join(stateDir, CoordinationFile)
	data, err := os.ReadFile(path) //nolint:gosec // this deployment's own state directory and a fixed name
	if errors.Is(err, os.ErrNotExist) {
		return DefaultConfig(), false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("reading %s: %w", CoordinationFile, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var declared document
	if err := decoder.Decode(&declared); err != nil {
		return Config{}, false, fmt.Errorf("refused: %s\n  %w", CoordinationFile, err)
	}

	cfg, err := declared.config(resolve)
	if err != nil {
		return Config{}, false, fmt.Errorf("refused: %s\n%w", CoordinationFile, err)
	}
	if err := cfg.Check(); err != nil {
		return Config{}, false, fmt.Errorf("refused: %s\n%w", CoordinationFile, err)
	}
	return cfg, true, nil
}

func (d document) config(resolve func(string) (string, error)) (Config, error) {
	cfg := DefaultConfig()
	var problems []error
	for _, declared := range []struct {
		field string
		text  string
		into  *time.Duration
	}{
		{"claim_expiry", d.ClaimExpiry, &cfg.ClaimExpiry},
		{"renew_every", d.RenewEvery, &cfg.RenewEvery},
		{"renew_bound", d.RenewBound, &cfg.RenewBound},
		{"stop_bound", d.StopBound, &cfg.StopBound},
		{"decision_bound", d.DecisionBound, &cfg.DecisionBound},
	} {
		if declared.text == "" {
			continue
		}
		value, err := time.ParseDuration(declared.text)
		if err != nil {
			problems = append(problems, fmt.Errorf(
				"  %s %q is not a duration\n    accepted: a duration such as 30s or 2m",
				declared.field, declared.text))
			continue
		}
		if value <= 0 {
			problems = append(problems, fmt.Errorf(
				"  %s %s is not positive\n    accepted: a duration above zero", declared.field, value))
			continue
		}
		*declared.into = value
	}

	if d.Etcd != nil {
		etcd, err := d.Etcd.config(resolve)
		if err != nil {
			problems = append(problems, err)
		}
		cfg.Etcd = etcd
	}
	return cfg, errors.Join(problems...)
}

// reference is what a credential field accepts and nothing else: a literal written here
// would sit beside the deployment's other state in clear, while a value resolved from a
// secret configuration entry is already known to the redactor.
var reference = regexp.MustCompile(`^\$\{config\.[A-Za-z0-9_-]+\}$`)

func (d etcdDocument) config(resolve func(string) (string, error)) (*EtcdConfig, error) {
	etcd := &EtcdConfig{Endpoints: d.Endpoints, Prefix: d.Prefix}
	var problems []error
	if len(d.Endpoints) == 0 {
		problems = append(problems, errors.New(
			"  etcd.endpoints is empty\n    accepted: at least one endpoint, such as "+
				"https://etcd-1.example.com:2379"))
	}
	for _, credential := range []struct {
		field string
		text  string
		into  *string
	}{
		{"etcd.username", d.Username, &etcd.Username},
		{"etcd.password", d.Password, &etcd.Password},
	} {
		if credential.text == "" {
			continue
		}
		if !reference.MatchString(credential.text) {
			problems = append(problems, fmt.Errorf(
				"  %s is a literal\n    accepted: a ${config.…} reference, so the value "+
					"lives in the configuration and the redactor knows it", credential.field))
			continue
		}
		value, err := resolve(credential.text)
		if err != nil {
			problems = append(problems, fmt.Errorf("  %s: %w", credential.field, err))
			continue
		}
		*credential.into = value
	}
	if d.TLS != nil {
		etcd.TLS = &TLSFiles{CA: d.TLS.CA, Cert: d.TLS.Cert, Key: d.TLS.Key}
	}
	return etcd, errors.Join(problems...)
}

// Check is R5: durations that cannot hold are refused before anything is armed, naming
// every one of them and the expiry they exceed.
func (c Config) Check() error {
	var problems []error
	sum := c.RenewEvery + c.RenewBound + c.StopBound + MarginFloor
	if sum > c.ClaimExpiry {
		problems = append(problems, fmt.Errorf(
			"  renew_every %s + renew_bound %s + stop_bound %s + margin %s = %s exceeds claim_expiry %s\n"+
				"    accepted: durations whose sum, with the %s margin, fits within claim_expiry",
			c.RenewEvery, c.RenewBound, c.StopBound, MarginFloor, sum, c.ClaimExpiry, MarginFloor))
	}
	if c.RenewBound >= c.RenewEvery {
		problems = append(problems, fmt.Errorf(
			"  renew_bound %s is not below renew_every %s\n"+
				"    accepted: a bound below the interval, so at most one attempt is ever in flight",
			c.RenewBound, c.RenewEvery))
	}
	if c.ClaimExpiry%time.Second != 0 {
		problems = append(problems, fmt.Errorf(
			"  claim_expiry %s is not a whole number of seconds\n"+
				"    accepted: a whole number of seconds, the unit of the backend's own lease",
			c.ClaimExpiry))
	}
	return errors.Join(problems...)
}
