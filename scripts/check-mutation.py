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
the count is shown to move in both directions before any real count is read.

Mutations are declared in JSON: a tree to copy, a file inside it, the text to replace,
what to replace it with, and the command that is expected to fail once it has been.
"""

from __future__ import annotations

import argparse
import json
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_CONFIG = ROOT / "runtime" / "testdata" / "mutations.json"


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
    return subprocess.run(command, cwd=cwd, capture_output=True, text=True, check=False)


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

        for failure in failures:
            print(f"check-mutation: self-test: {failure}", file=sys.stderr)
        if failures:
            return 1

    print(
        "check-mutation: self-test ok — reports zero when a test notices, one when it does not"
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
