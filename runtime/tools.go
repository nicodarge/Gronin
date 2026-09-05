//go:build tools

// Package tools pins the dependencies plan.md commits to, before the code that uses
// them exists. `go mod tidy` reads imports under every build tag, so the versions are
// recorded and reproducible; the build tag keeps them out of the executable.
//
// The SQLite driver is the pure-Go one. The better-known mattn/go-sqlite3 needs a C
// toolchain, and one cgo dependency forfeits the static binary and SC-007 with it.
package tools

import (
	_ "github.com/robfig/cron/v3"
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "github.com/spf13/cobra"
	_ "gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)
