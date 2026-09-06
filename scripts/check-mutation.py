#!/usr/bin/env python3
"""Break a line on purpose and confirm something notices.

A green suite is not evidence that a check ran. The only way to know a test can fail is
to make the thing it covers wrong and watch the exit code flip, and the only way to know
this harness can report a failure is to watch it report zero when there is none.

So it does three things a naive version skips. It runs the command on an unmutated copy
first: a harness whose baseline is already broken can never print zero, and will report a
comfortable number forever. It refuses a mutation whose target text is not found exactly
once, rather than counting an unapplied mutation as killed. And `--self-test` runs it
against two fixtures, one whose test detects the change and one whose test ignores it, so
the count is shown to move in both directions before any real count is read — and against
two it must refuse outright, a broken baseline and a target text that is not there.

Mutations are declared in JSON: a tree to copy, a file inside it, the text to replace,
what to replace it with, and the command that is expected to fail once it has been.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_CONFIG = ROOT / "runtime" / "testdata" / "mutations.json"

# Mutation runs go through the same isolation as the suite. They are test invocations
# like any other, and there is no reason for the one job that grows a test run per
# mutation to be the one that keeps its network.
NO_NETWORK = ROOT / "scripts" / "no-network.sh"


class ConfigError(Exception):
    """A mutation that cannot be applied. Fatal — never counted as killed."""


@dataclass(frozen=True)
class Mutation:
    name: str
    tree: Path
    file: str
    find: str
    replace: str
    command: list[str]


def load(config: Path) -> list[Mutation]:
    raw = json.loads(config.read_text())
    mutations = []
    for entry in raw["mutations"]:
        tree = (config.parent / entry["tree"]).resolve()
        if not tree.is_dir():
            raise ConfigError(f"{entry['name']}: tree {tree} is not a directory")
        mutations.append(
            Mutation(
                name=entry["name"],
                tree=tree,
                file=entry["file"],
                find=entry["find"],
                replace=entry["replace"],
                command=entry["command"],
            )
        )
    return mutations


def run(command: list[str], cwd: Path) -> subprocess.CompletedProcess[str]:
    # GOPROXY=off so a module that is not already cached says so, rather than hanging
    # against a proxy the namespace will never reach.
    env = {**os.environ, "GOPROXY": "off"}
    return subprocess.run(
        [str(NO_NETWORK), *command],
        cwd=cwd,
        capture_output=True,
        text=True,
        check=False,
        env=env,
    )


def survives(mutation: Mutation) -> bool:
    """Apply the mutation to a copy and report whether the command still passed."""
    with tempfile.TemporaryDirectory(prefix="gronin-mutation-") as tmp:
        work = Path(tmp) / mutation.tree.name
        shutil.copytree(mutation.tree, work, symlinks=True)

        baseline = run(mutation.command, work)
        if baseline.returncode != 0:
            raise ConfigError(
                f"{mutation.name}: the command already fails on an unmutated copy, so "
                f"nothing it reports afterwards means anything:\n{baseline.stdout}"
                f"{baseline.stderr}"
            )

        target = work / mutation.file
        if not target.is_file():
            raise ConfigError(
                f"{mutation.name}: {mutation.file} is not in the copied tree"
            )
        text = target.read_text()
        found = text.count(mutation.find)
        if found != 1:
            raise ConfigError(
                f"{mutation.name}: found {found} occurrences of the target text in "
                f"{mutation.file}, expected exactly one"
            )
        target.write_text(text.replace(mutation.find, mutation.replace))

        # A mutant that does not compile exits non-zero exactly like a failing test, so
        # the command below would count it killed without ever running the defect.
        # Refused instead, the way a moved target and a broken baseline already are.
        #
        # `go test -run=^$` builds every test binary and runs none of them. `go build`
        # does not compile _test.go at all, and `go vet` refuses on its own diagnostics —
        # measured, it calls one legitimate mutant here unreachable code.
        #
        # Skipped only for a tree holding no Go at all, which is the self-test's shell
        # subject. Go without a go.mod at the root is refused rather than skipped: a
        # mutation scoped to a package directory would otherwise walk past this check in
        # silence and be counted killed again, which is the whole defect above.
        built = None
        if (work / "go.mod").is_file():
            built = run(["go", "test", "-run=^$", "-count=1", "./..."], work)
        elif any(work.rglob("*.go")):
            raise ConfigError(
                f"{mutation.name}: {mutation.tree} holds Go but no go.mod at its root, "
                f"so the mutated tree cannot be compiled before the command runs; point "
                f"the mutation's tree at the module root"
            )
        if built is not None and built.returncode != 0:
            raise ConfigError(
                f"{mutation.name}: the mutated tree does not compile, so the command "
                f"below would report it killed without ever running the defect:\n"
                f"{built.stdout}{built.stderr}"
            )

        return run(mutation.command, work).returncode == 0


def check(mutations: list[Mutation], *, quiet: bool = False) -> int:
    """Return the number of mutations nothing noticed."""
    survivors = 0
    for mutation in mutations:
        survived = survives(mutation)
        survivors += survived
        if not quiet:
            print(
                f"check-mutation: {'SURVIVED ' if survived else 'killed   '} {mutation.name}"
            )
    if not quiet:
        print(f"check-mutation: {survivors} survivors of {len(mutations)} mutants")
    return survivors


SELF_TEST_SUBJECT = """#!/bin/sh
echo "the answer is 42"
"""

SELF_TEST_ATTENTIVE = """#!/bin/sh
./subject.sh | grep -q "42"
"""

SELF_TEST_INATTENTIVE = """#!/bin/sh
./subject.sh > /dev/null
"""

SELF_TEST_BROKEN = """#!/bin/sh
exit 1
"""

# A Go module, because the compile refusal only applies where there is something to
# compile, and the shell subjects above have nothing. One package, one test, and a
# mutation that leaves an import unused — which is the shape every one of the thirteen
# mutants this refusal was written for had.
SELF_TEST_GO_MOD = """module example.com/selftest

go 1.24
"""

SELF_TEST_GO_SUBJECT = """package selftest

import "strings"

// Answer is what the test below checks, and strings is what the mutation orphans.
func Answer() string { return strings.TrimSpace(" 42 ") }
"""

SELF_TEST_GO_TEST = """package selftest

import "testing"

func TestAnswer(t *testing.T) {
	if Answer() != "42" {
		t.Fatal("wrong")
	}
}
"""


def self_test() -> int:
    """Show the count moving in both directions before any real count is trusted."""
    with tempfile.TemporaryDirectory(prefix="gronin-mutation-selftest-") as tmp:
        root = Path(tmp)
        cases = []
        for name, test in (
            ("attentive", SELF_TEST_ATTENTIVE),
            ("inattentive", SELF_TEST_INATTENTIVE),
        ):
            tree = root / name
            tree.mkdir()
            (tree / "subject.sh").write_text(SELF_TEST_SUBJECT)
            (tree / "subject.sh").chmod(0o755)
            (tree / "test.sh").write_text(test)
            (tree / "test.sh").chmod(0o755)
            cases.append(
                Mutation(
                    name=name,
                    tree=tree,
                    file="subject.sh",
                    find="42",
                    replace="41",
                    command=["./test.sh"],
                )
            )

        killed, survived = cases
        failures = []
        if check([killed], quiet=True) != 0:
            failures.append("a mutation its test detects was counted as a survivor")
        if check([survived], quiet=True) != 1:
            failures.append("a mutation nothing detects was counted as killed")

        # The refusals matter as much as the counts. A harness that treats a broken
        # baseline or an unapplied mutation as a result reports a number for something
        # it never measured, and a denylist probed only on what it accepts is untested.
        broken = root / "broken"
        broken.mkdir()
        (broken / "subject.sh").write_text(SELF_TEST_SUBJECT)
        (broken / "subject.sh").chmod(0o755)
        (broken / "test.sh").write_text(SELF_TEST_BROKEN)
        (broken / "test.sh").chmod(0o755)

        refusals = {
            "a baseline that already fails": Mutation(
                name="broken baseline",
                tree=broken,
                file="subject.sh",
                find="42",
                replace="41",
                command=["./test.sh"],
            ),
            "a target text that is not in the file": Mutation(
                name="absent target",
                tree=killed.tree,
                file="subject.sh",
                find="no such text",
                replace="x",
                command=["./test.sh"],
            ),
        }

        # The compile refusal. Without a case here the harness's newest branch is the one
        # piece of it nothing exercises, which is the shape of defect it exists to catch.
        gomod = root / "gomod"
        gomod.mkdir()
        (gomod / "go.mod").write_text(SELF_TEST_GO_MOD)
        (gomod / "subject.go").write_text(SELF_TEST_GO_SUBJECT)
        (gomod / "subject_test.go").write_text(SELF_TEST_GO_TEST)
        refusals["a mutant that does not compile"] = Mutation(
            name="uncompilable mutant",
            tree=gomod,
            file="subject.go",
            # Orphans the strings import, exactly as the thirteen did.
            find='return strings.TrimSpace(" 42 ")',
            replace='return "41"',
            command=["go", "test", "./...", "-count=1"],
        )

        # Go with no module root. The compile gate can only run where `go test ./...`
        # resolves, and skipping silently there is how the defect above would come back
        # for a mutation scoped to a package directory rather than the module.
        nomod = root / "nomod"
        nomod.mkdir()
        (nomod / "subject.go").write_text(SELF_TEST_GO_SUBJECT)
        refusals["Go with no go.mod at the tree root"] = Mutation(
            name="no module root",
            tree=nomod,
            file="subject.go",
            find='return strings.TrimSpace(" 42 ")',
            replace='return "41"',
            command=["true"],
        )

        for description, mutation in refusals.items():
            try:
                check([mutation], quiet=True)
            except ConfigError:
                continue
            failures.append(f"{description} was counted rather than refused")

        # The third refusal lives in load() rather than in survives(), so it needs a
        # config file to reach: a mutation naming a tree that is not there must be
        # refused before anything is run, not counted as a mutant nothing noticed.
        config = root / "missing-tree.json"
        config.write_text(
            json.dumps(
                {
                    "mutations": [
                        {
                            "name": "missing tree",
                            "tree": "no-such-directory",
                            "file": "subject.sh",
                            "find": "42",
                            "replace": "41",
                            "command": ["./test.sh"],
                        }
                    ]
                }
            )
        )
        try:
            load(config)
        except ConfigError:
            pass
        else:
            failures.append("a mutation naming a tree that is not there was loaded")

        for failure in failures:
            print(f"check-mutation: self-test: {failure}", file=sys.stderr)
        if failures:
            return 1

    print(
        "check-mutation: self-test ok — counts zero and one, and refuses a broken "
        "baseline, an absent target, a mutant that does not compile, Go with no module "
        "root and a missing tree"
    )
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=DEFAULT_CONFIG)
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()

    try:
        if args.self_test:
            return self_test()
        return 1 if check(load(args.config)) else 0
    except ConfigError as err:
        print(f"check-mutation: {err}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
