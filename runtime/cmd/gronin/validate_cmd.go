package main

import (
	"github.com/spf13/cobra"
)

// newValidateCommand is FR-035: the load gate alone, exiting non-zero on refusal, with
// no credential and nothing armed — so it runs wherever playbooks are reviewed.
//
// It is the same code path `serve` uses, not a second implementation. A validator that
// can disagree with the runtime is worse than none: an author would trust the one that
// passes and ship what the other refuses.
func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [dir]",
		Short: "Run the load gate and exit, arming nothing",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := cmd.Flags().Set("playbooks", args[0]); err != nil {
					return err
				}
			}
			cfg, err := openConfig(cmd)
			if err != nil {
				return err
			}
			loaded, err := loadPlaybooks(cmd, cfg)
			if err != nil {
				return errSilent{err}
			}
			cmd.Printf("%d playbook(s) accepted from %s.\n",
				len(loaded.Playbooks), playbooksDir(cmd))
			return nil
		},
	}
}

// errSilent carries a non-zero exit without printing again. The refusals have already
// been written in the shape contracts/cli.md specifies, and cobra would otherwise append
// its own one-line summary underneath them.
type errSilent struct{ inner error }

func (e errSilent) Error() string { return e.inner.Error() }
