package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/logging"
	"github.com/nicodarge/Gronin/runtime/internal/mcpcatalog"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
	"github.com/nicodarge/Gronin/runtime/internal/sink"
)

// deployment is everything one invocation needs: where this deployment keeps its state,
// what it knows about itself, and the record it writes to.
type deployment struct {
	config          *config.Config
	catalog         *mcpcatalog.Catalog
	store           *record.Store
	manager         *run.Manager
	executor        *run.Executor
	log             *slog.Logger
	stateDir        string
	agentExecutable string
}

func (d *deployment) close() {
	if d.store != nil {
		_ = d.store.Close()
	}
}

// agentEnvVars are the variables the agent child inherits from this process. It is a
// named list rather than the whole environment: the child is a language model with tools,
// and handing it everything this process holds is the failure FR-008 describes for
// interpolation, one layer down.
var agentEnvVars = []string{
	"PATH", "HOME", "TMPDIR", "LANG",
	// Credential sources. research.md established which exist; their resolution order is
	// the CLI's, and the runtime reports what the child says it used rather than
	// asserting one.
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
	"AWS_REGION", "AWS_PROFILE", "GOOGLE_CLOUD_PROJECT",
}

// openDeployment builds everything a command needs around an already-loaded
// configuration. It takes the configuration rather than reading it because the gate needs
// the same one — and a command that read it twice would read the same file twice per
// invocation.
func openDeployment(
	cmd *cobra.Command, cfg *config.Config, catalog *mcpcatalog.Catalog,
) (*deployment, error) {
	stateDir := stateDirOf(cmd)

	store, err := record.Open(cmd.Context(), filepath.Join(stateDir, "record"),
		record.NewRedactor(cfg.Secrets()))
	if err != nil {
		return nil, err
	}

	executable, err := cmd.Flags().GetString("agent")
	if err != nil || executable == "" {
		executable = "claude"
	}

	// The redactor seeds the log as well as the store. A credential is likeliest to
	// surface in the line describing what failed, because it is usually the thing that
	// failed to authenticate.
	log := logging.New(cmd.ErrOrStderr(), record.NewRedactor(cfg.Secrets()), logLevel(cmd))

	manager := run.NewManager(store, filepath.Join(stateDir, "work"), filepath.Join(stateDir, "locks"))
	return &deployment{
		config:          cfg,
		catalog:         catalog,
		store:           store,
		manager:         manager,
		log:             log,
		stateDir:        stateDir,
		agentExecutable: executable,
		executor: &run.Executor{
			Manager:         manager,
			Store:           store,
			Config:          cfg,
			Catalog:         catalog,
			AgentExecutable: executable,
			AgentEnv:        inheritedEnv(agentEnvVars),
			// A gather step gets a path and nothing else. It is a command a playbook
			// author wrote, and this process holds the deployment's credentials.
			StepEnv: inheritedEnv([]string{"PATH", "HOME", "TMPDIR", "LANG"}),
			Log:     log,
			Now:     func() time.Time { return time.Now().UTC() },
		},
	}, nil
}

func inheritedEnv(names []string) []string {
	var env []string
	for _, name := range names {
		if value, set := os.LookupEnv(name); set {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// capabilities is what this deployment can do, which is half of whether a playbook is
// safe. A name the gate cannot resolve is refused here rather than at delivery, after a
// full agent run has been paid for.
func capabilities(cfg *config.Config, catalog *mcpcatalog.Catalog) playbook.Deployment {
	var keys []string
	if cfg != nil {
		keys = cfg.Keys()
	}
	var servers []string
	if catalog != nil {
		servers = catalog.Names()
	}
	return playbook.Deployment{
		// What the deployment can resolve. spec.md asks for a reference that resolves to
		// nothing to be refused at load rather than at trigger time, and the gate cannot
		// answer that without knowing which keys exist.
		ConfigKeys: keys,
		// The servers this deployment's own catalogue provides. A name outside it is
		// refused here rather than at delivery, after a full agent run has been paid for.
		MCPServers:    servers,
		SinkTypes:     sink.Types(),
		CreatingSinks: sink.CreatingTypes(),
	}
}

// playbooksDir is where this deployment keeps its playbooks.
func playbooksDir(cmd *cobra.Command) string {
	if dir, err := cmd.Flags().GetString("playbooks"); err == nil && dir != "" {
		return dir
	}
	return filepath.Join(stateDirOf(cmd), "playbooks")
}

// loadPlaybooks reads the directory and refuses the whole set if any of it is refused.
// The last line is deliberate: a gate that refuses two out of six and starts anyway is
// the failure the design exists to prevent, so the output says nothing was armed.
func loadPlaybooks(
	cmd *cobra.Command, cfg *config.Config, catalog *mcpcatalog.Catalog,
) (playbook.Loaded, error) {
	dir := playbooksDir(cmd)
	loaded, err := playbook.Load(dir, capabilities(cfg, catalog))
	if err != nil {
		return loaded, err
	}
	if loaded.OK() {
		return loaded, nil
	}

	// The shape of this output is the contract in contracts/cli.md: the playbook, the
	// field, what was found, and what would be accepted.
	out := cmd.ErrOrStderr()
	for _, refusal := range loaded.Refusals {
		_, _ = fmt.Fprintf(out, "refused: %s\n", filepath.Base(refusal.Path))
		if refusal.Reason != nil {
			_, _ = fmt.Fprintf(out, "  %s\n", refusal.Reason)
		}
		for _, problem := range refusal.Problems {
			_, _ = fmt.Fprintf(out, "  %s\n", problem.Error())
		}
		_, _ = fmt.Fprintln(out)
	}
	// Deliberate: a refusal that cannot be printed is still a refusal, and the exit code
	// below carries it either way.
	_, _ = fmt.Fprintf(out, "%d playbook(s) refused, %d accepted. Nothing was armed.\n",
		len(loaded.Refusals), len(loaded.Playbooks))
	return loaded, fmt.Errorf("%d playbook(s) refused", len(loaded.Refusals))
}

// logLevel is how much the deployment says. Info by default: a runtime that says nothing
// until it breaks leaves an operator reconstructing what it did from the record alone.
func logLevel(cmd *cobra.Command) slog.Level {
	if verbose, err := cmd.Flags().GetBool("verbose"); err == nil && verbose {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// openConfig reads the deployment's configuration. Every command that needs it reads it
// once here and hands it on, so the gate and the deployment share one read of one file.
func openConfig(cmd *cobra.Command) (*config.Config, error) {
	return config.Load(stateDirOf(cmd))
}

// openCatalog reads the MCP server catalogue once per invocation, the way openConfig
// reads config.json.
func openCatalog(cmd *cobra.Command) (*mcpcatalog.Catalog, error) {
	return mcpcatalog.Load(stateDirOf(cmd))
}

// openResolvableCatalog also refuses a catalogue this deployment cannot resolve, so a
// mistyped key is found at load rather than at the first trigger.
//
// Only a command that can reach an agent takes this path. `gronin config set` must not:
// it is how a missing key gets set, and refusing it because a key is missing leaves an
// operator told to run the command that just failed. Reading run history must not
// either — that is most wanted right after something broke, and an unrelated catalogue
// fault is no reason to withhold it.
func openResolvableCatalog(cmd *cobra.Command, cfg *config.Config) (*mcpcatalog.Catalog, error) {
	catalog, err := openCatalog(cmd)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return catalog, nil
	}
	if err := catalog.CheckReferences(func(text string) (string, error) {
		return cfg.Interpolate(text, nil)
	}); err != nil {
		return nil, err
	}
	return catalog, nil
}
