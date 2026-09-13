package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/nicodarge/Gronin/runtime/internal/collections"
	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/logging"
	"github.com/nicodarge/Gronin/runtime/internal/mcpcatalog"
	"github.com/nicodarge/Gronin/runtime/internal/playbook"
	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
	"github.com/nicodarge/Gronin/runtime/internal/sink"
	"github.com/nicodarge/Gronin/runtime/internal/sources"
	"github.com/nicodarge/Gronin/runtime/internal/stage/retrieve"
)

// deployment is everything one invocation needs: where this deployment keeps its state,
// what it knows about itself, and the record it writes to.
type deployment struct {
	config          *config.Config
	catalog         *mcpcatalog.Catalog
	store           *record.Store
	manager         *run.Manager
	executor        *run.Executor
	coordination    *coordination
	log             *slog.Logger
	stateDir        string
	agentExecutable string
	// instance is held by a process that lets a trigger wait, for as long as it is open.
	instance *guard.Instance
}

func (d *deployment) close() {
	if d.instance != nil {
		_ = d.instance.Close()
	}
	if d.coordination != nil {
		d.coordination.close()
	}
	if d.store != nil {
		_ = d.store.Close()
	}
}

// acceptWaiting lets this process's manual triggers wait for the run they collide with
// (FR-110). It holds the instance lock that says this process is alive until the deployment
// is closed, so a waiting row it writes is never read as dropped while it lives; and it
// reads a waiting trigger's playbook again through the same gate the directory was loaded
// through (FR-121).
func (d *deployment) acceptWaiting(
	cfg *config.Config, catalog *mcpcatalog.Catalog, declared *collections.Catalog,
	srcs *sources.Catalog,
) error {
	held, err := guard.HoldInstance(d.stateDir, d.executor.Guard.Instance)
	if err != nil {
		return err
	}
	d.instance = held
	d.executor.Guard.Slot = &guard.WaitSlot{
		Instance: held,
		Reload: func(path string) (*playbook.Playbook, error) {
			return playbook.LoadFile(path, capabilities(cfg, catalog, declared, srcs))
		},
		RunID: run.NewRunID,
	}
	return nil
}

// reconcile marks dropped every waiting trigger whose process is gone (FR-113).
func (d *deployment) reconcile(ctx context.Context) (int, error) {
	return guard.Reconcile(ctx, d.stateDir, d.store, nil)
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
	backend, err := openCoordination(stateDir, cfg, manager)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	guarded := &guard.Guard{
		Coordinator: backend.coordinator,
		Store:       store,
		Config:      backend.config,
		Host:        hostName(),
		Instance:    instanceID(),
		Backend:     backend.name(),
		Log:         log,
	}
	return &deployment{
		config:          cfg,
		catalog:         catalog,
		store:           store,
		manager:         manager,
		coordination:    backend,
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
			Guard:   guarded,
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
func capabilities(
	cfg *config.Config, catalog *mcpcatalog.Catalog, declared *collections.Catalog,
	srcs *sources.Catalog,
) playbook.Deployment {
	var keys []string
	if cfg != nil {
		keys = cfg.Keys()
	}
	var servers []string
	if catalog != nil {
		servers = catalog.Names()
	}
	var declaredCollections []string
	if declared != nil {
		declaredCollections = declared.Names()
	}
	var configuredSources []string
	if srcs != nil {
		configuredSources = srcs.Names()
	}
	return playbook.Deployment{
		// What the deployment can resolve. spec.md asks for a reference that resolves to
		// nothing to be refused at load rather than at trigger time, and the gate cannot
		// answer that without knowing which keys exist.
		ConfigKeys: keys,
		// The servers this deployment's own catalogue provides. A name outside it is
		// refused here rather than at delivery, after a full agent run has been paid for.
		MCPServers: servers,
		// The collections this deployment's catalogue declares. A retrieval naming
		// anything else is refused at load rather than when the run reaches for it.
		Collections:   declaredCollections,
		SinkTypes:     sink.Types(),
		CreatingSinks: sink.CreatingTypes(),
		// The sources this deployment configures. A webhook trigger naming anything else
		// is refused at load (FR-310), once validateWebhook (T025) reads this.
		Sources: configuredSources,
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
	declared *collections.Catalog, srcs *sources.Catalog,
) (playbook.Loaded, error) {
	dir := playbooksDir(cmd)
	loaded, err := playbook.Load(dir, capabilities(cfg, catalog, declared, srcs))
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

// openSources reads the source catalogue once per invocation, beside the MCP one. It
// takes the configuration already read rather than reading it again, the way
// openResolvableCatalog does: a source's secret reference is checked against it at load.
func openSources(cmd *cobra.Command, cfg *config.Config) (*sources.Catalog, error) {
	return sources.Load(stateDirOf(cmd), cfg)
}

// indexDir is where every collection's index lives: in the state directory, as derived
// data a walk of the sources restores (FR-216).
func indexDir(stateDir string) string {
	return filepath.Join(stateDir, "index")
}

// retrieving hands the executor the retrieve stage over the collections this deployment
// declares. Only a command that runs a playbook calls it, so a malformed collections.json
// withholds nothing from one that reads run history.
func (d *deployment) retrieving(declared *collections.Catalog) {
	d.executor.Retrieve = &retrieve.Stage{
		Catalog:  declared,
		IndexDir: indexDir(d.stateDir),
		Config:   d.config,
		Redactor: d.store.Redactor(),
	}
}

// openCollections reads the collection catalogue once per invocation, the way openCatalog
// reads the MCP one.
//
// Only a command that loads playbooks takes this path, for the reason
// openResolvableCatalog states below: a malformed collections.json must not withhold run
// history, which is what is most wanted right after something broke.
func openCollections(cmd *cobra.Command) (*collections.Catalog, error) {
	return collections.Load(stateDirOf(cmd))
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
