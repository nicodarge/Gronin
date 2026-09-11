package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nicodarge/Gronin/runtime/internal/config"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// newConfigCommand is FR-040, and it is part of the contract rather than a convenience.
//
// A playbook holds `${config.ops_webhook}`; the value lives on the deployment. That
// separation is the whole of what makes a playbook committable and shareable, so the
// command that maintains it is as much a part of the product as the runtime.
func newConfigCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Set and inspect the values playbooks interpolate against",
	}

	set := &cobra.Command{
		Use:   "set <key>",
		Short: "Set a deployment configuration value, read from standard input",
		Args:  setArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Checked before anything reads the value: a mistyped key is otherwise found
			// only after the operator has typed or piped the value at a prompt.
			if err := config.ValidKey(args[0]); err != nil {
				return err
			}
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			catalog, err := openCatalog(cmd)
			if err != nil {
				return err
			}
			deployment, err := openDeployment(cmd, cfg, catalog)
			if err != nil {
				return err
			}
			defer deployment.close()

			secret, err := cmd.Flags().GetBool("secret")
			if err != nil {
				return err
			}
			value, err := readConfigValue(cmd)
			if err != nil {
				return err
			}
			if err := deployment.config.Set(args[0], config.Value{Value: value, Secret: secret}); err != nil {
				return err
			}
			// The value is not echoed. It may be the secret that was just set, and this
			// command is run in a shell whose history keeps what it is told.
			cmd.Printf("set %s\n", args[0])
			return nil
		},
	}
	// Marked rather than guessed: a heuristic reading "token" and "webhook" as secret and
	// "endpoint" as not is wrong on the first deployment that disagrees, and wrong
	// silently.
	set.Flags().Bool("secret", false, "treat the value as a secret: redact it everywhere")

	list := &cobra.Command{
		Use:   "list",
		Short: "List configuration keys, secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			catalog, err := openCatalog(cmd)
			if err != nil {
				return err
			}
			deployment, err := openDeployment(cmd, cfg, catalog)
			if err != nil {
				return err
			}
			defer deployment.close()

			keys := deployment.config.Keys()
			if len(keys) == 0 {
				cmd.Printf("no configuration values in %s\n", deployment.stateDir)
				return nil
			}
			for _, key := range keys {
				value, _ := deployment.config.Get(key)
				shown := value.Value
				if value.Secret {
					shown = record.Placeholder
				}
				cmd.Println(fmt.Sprintf("%-24s %s", key, shown))
			}
			return nil
		},
	}

	command.AddCommand(set, list)
	return command
}

// setArgs accepts the key alone. A second argument is where the value used to go, so it
// is refused by name rather than by cobra's generic argument-count message: the
// constitution's Secrets constraint is that a value MUST NOT appear on a command line —
// commands are journalled and shipped to log aggregation, where it then sits for the
// whole retention window.
func setArgs(_ *cobra.Command, args []string) error {
	switch len(args) {
	case 0:
		return fmt.Errorf("accepts 1 arg(s), the key, received 0")
	case 1:
		return nil
	default:
		return fmt.Errorf(
			"the value must not be given on the command line; accepted: "+
				"`printf '%%s' \"$VALUE\" | gronin config set %s` or `gronin config set %s < file`",
			args[0], args[0])
	}
}

// readConfigValue reads the value `config set` stores, never from argv. A terminal is
// prompted without echo, the way every other credential prompt on this operator's shell
// works; anything else — a pipe, a redirected file — is read whole, with exactly one
// trailing newline stripped, so both `printf '%s' "$VALUE" | gronin config set key` and
// `gronin config set key < file` round-trip the value the operator meant. An empty read
// stores an empty value rather than failing: `printf "" | gronin config set key` is how a
// key is declared-and-empty, which sink/build.go tells apart from not configured at all.
func readConfigValue(cmd *cobra.Command) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return promptTerminal(cmd.ErrOrStderr(), f)
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("reading the value from standard input: %w", err)
	}
	return stripOneTrailingNewline(string(data)), nil
}

// stripOneTrailingNewline removes exactly one trailing newline — "\n", or the "\r\n" a
// CRLF-terminated file leaves — so `gronin config set key < file` round-trips the file's
// content rather than keeping a byte the operator did not put there. A bare TrimSuffix on
// "\n" alone leaves that "\r" in the stored value, invisibly, on the same path a secret
// travels.
func stripOneTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return s[:len(s)-2]
	}
	return strings.TrimSuffix(s, "\n")
}

// promptTerminal is the no-echo prompt readConfigValue uses on a terminal.
//
// term.ReadPassword restores echo itself when it returns, but it leaves ISIG set, so a
// Ctrl-C during the read still raises SIGINT — and with no handler installed, Go's
// default disposition kills the process before that restore runs, leaving the
// operator's terminal without echo until they run `stty sane`. Caught here instead: the
// terminal state is captured before the read and restored explicitly on the way out, on
// an interrupt as well as a clean return.
//
// Echo goes off before the label is written, not inside ReadPassword: otherwise a value
// already on its way when the label appears is echoed. ReadPassword then restores the
// echo-off state it found, so the state captured first is restored explicitly.
//
// completed narrows a race between the read finishing and a signal landing, rather than
// closing it outright: without it, an interrupt arriving in the gap between
// ReadPassword returning a value and the goroutine below noticing could still discard a
// value the operator had already typed. Setting it the instant the read returns shrinks
// that gap to the two statements between the read and the store — small enough that a
// human pressing Ctrl-C cannot land in it, and even if something did, the outcome is the
// same deliberate "discard and exit" the interrupted path already takes.
func promptTerminal(errOut io.Writer, f *os.File) (string, error) {
	fd := int(f.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", fmt.Errorf("reading the value from the terminal: %w", err)
	}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interrupted)
	done := make(chan struct{})
	defer close(done)
	var completed atomic.Bool
	go func() {
		select {
		case <-interrupted:
			if completed.CompareAndSwap(false, true) {
				_ = term.Restore(fd, state)
				os.Exit(1)
			}
		case <-done:
		}
	}()

	if err := disableEcho(fd); err != nil {
		return "", fmt.Errorf("reading the value from the terminal: %w", err)
	}
	_, _ = fmt.Fprint(errOut, "value: ")
	raw, err := term.ReadPassword(fd)
	completed.Store(true)
	_ = term.Restore(fd, state)
	_, _ = fmt.Fprintln(errOut)
	if err != nil {
		return "", fmt.Errorf("reading the value from the terminal: %w", err)
	}
	return string(raw), nil
}
