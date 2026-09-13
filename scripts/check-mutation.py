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
two it must refuse outright, a broken baseline and a target text that is not there. It also
proves `--shard K/N` partitions the declared mutants into disjoint shards whose union is
the full list, and refuses a shard that is malformed or selects none.

Mutations are declared in JSON: a tree to copy, a file inside it, the text to replace,
what to replace it with, and the command that is expected to fail once it has been.
The tree's repository is copied by its tracked files, laid out as the repository has
them, so a test that reads outside the tree by a relative path sees what CI sees. The
copy is each file's working-tree content, not its committed blob, so this and CI agree
only on a clean tree.
"""

from __future__ import annotations

import argparse
import contextlib
import fcntl
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
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


# ASCII digits only, matched with a regex rather than str.isdigit(): isdigit() also
# accepts Unicode digits whose behaviour under int() is not uniform. A superscript
# ('²') is a digit by isdigit() but raises ValueError from int() -- a crash, not an
# acceptance. A fullwidth or Arabic-Indic digit ('１', '٣') is instead silently
# accepted by int() and converted to its ASCII value. A regex anchored to [0-9]
# takes neither path.
_SHARD_RE = re.compile(r"([0-9]+)/([0-9]+)")


def parse_shard(value: str) -> tuple[int, int]:
    """Parse "K/N" into (K, N), refusing anything that is not exactly that shape.

    K is 1-based. Refused rather than clamped or defaulted: a malformed shard
    argument on a CI matrix leg is a configuration mistake, and running the wrong
    slice of mutants silently is worse than the job failing loudly.
    """
    match = _SHARD_RE.fullmatch(value)
    if match is None:
        raise ConfigError(f"--shard {value!r}: expected K/N of ASCII digits")
    k, n = int(match.group(1)), int(match.group(2))
    if n < 1:
        raise ConfigError(f"--shard {value!r}: N must be at least 1")
    if not 1 <= k <= n:
        raise ConfigError(f"--shard {value!r}: K must be between 1 and N")
    return k, n


def select_shard(mutations: list[Mutation], k: int, n: int) -> list[Mutation]:
    """The mutants whose position in the declared list is k-1 modulo n.

    Deterministic and disjoint by construction: every mutant lands in exactly one
    shard, and the union of all N shards is the input list. Refused if empty rather
    than returning nothing to check silently -- a shard with nothing in it would
    otherwise print "0 survivors of 0" and exit 0, indistinguishable from a real
    all-clear.
    """
    selected = [m for i, m in enumerate(mutations) if i % n == k - 1]
    if not selected:
        raise ConfigError(
            f"--shard {k}/{n} selects no mutants out of {len(mutations)} declared"
        )
    return selected


def apply_shard(
    mutations: list[Mutation], shard: tuple[int, int] | None
) -> list[Mutation]:
    """Apply an already-parsed (K, N) shard to a loaded list, or return it unchanged.

    main()'s only use of --shard; the self-test's end-to-end check runs the CLI
    itself to prove that wiring, not just this function.
    """
    if shard is None:
        return mutations
    k, n = shard
    return select_shard(mutations, k, n)


def load(config: Path) -> list[Mutation]:
    raw = json.loads(config.read_text())
    mutations = []
    for entry in raw["mutations"]:
        tree = (config.parent / entry["tree"]).resolve()
        if not tree.is_dir():
            raise ConfigError(f"{entry['name']}: tree {tree} is not a directory")
        # An absolute file, or one escaping with "..", would write to the real repository.
        file_path = (tree / entry["file"]).resolve()
        if not file_path.is_relative_to(tree):
            raise ConfigError(
                f"{entry['name']}: file {entry['file']!r} resolves to {file_path}, "
                f"outside its tree {tree}"
            )
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
    # start_new_session puts the child in a process group of its own so the whole
    # tree can be signalled at once: no-network.sh forks before its unshare chain,
    # so the command that actually matters is a grandchild and signalling the
    # immediate pid would leave it running. Unwinding the interpreter does not
    # reap it either -- a child outlives a parent that exits, verified.
    with subprocess.Popen(
        [str(NO_NETWORK), *command],
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        start_new_session=True,
    ) as proc:
        try:
            out, err = proc.communicate()
        except BaseException:
            _kill_group(proc)
            raise
    return subprocess.CompletedProcess(proc.args, proc.returncode, out, err)


def _kill_group(proc: subprocess.Popen) -> None:
    """SIGKILL the group started by run(), if it is still running.

    start_new_session makes the child a group leader, so its pid is the group id and
    no getpgid lookup is needed. A child already reaped is left alone: its pid can
    have been recycled by then, and signalling it would reach an unrelated group.
    """
    if proc.poll() is not None:
        return
    with contextlib.suppress(OSError):
        os.killpg(proc.pid, signal.SIGKILL)


def enclosing_module(tree: Path) -> Path | None:
    """The root of the Go module the tree sits under, or None if it sits under none."""
    for candidate in tree.parents:
        if (candidate / "go.mod").is_file():
            return candidate
    return None


def repository_root(tree: Path) -> Path | None:
    """The git work tree tree sits inside, or None (the self-test's plain fixtures)."""
    proc = subprocess.run(
        ["git", "-C", str(tree), "rev-parse", "--show-toplevel"],
        capture_output=True,
        text=True,
    )
    return Path(proc.stdout.strip()).resolve() if proc.returncode == 0 else None


def tracked_files(repo_root: Path) -> list[str]:
    """Every path git tracks at repo_root, repository-relative."""
    listing = subprocess.run(
        ["git", "-C", str(repo_root), "ls-files", "-z"],
        capture_output=True,
        text=True,
        check=True,
    ).stdout
    return [path for path in listing.split("\0") if path]


def copy_tree(tree: Path, dest_root: Path) -> Path:
    """Copy what tree's tests can reach into dest_root; return the copy of tree itself.

    A mutant must see what CI sees: every file git tracks in tree's repository, laid
    out exactly as the repository has it, so a test that reads outside the tree by a
    relative path finds what it expects instead of silently skipping. A tree that is
    not inside a git work tree falls back to copying only the tree.
    """
    repo_root = repository_root(tree)
    if repo_root is None:
        work = dest_root / tree.name
        shutil.copytree(tree, work, symlinks=True)
        return work
    for relpath in tracked_files(repo_root):
        source = repo_root / relpath
        if not (source.is_file() or source.is_symlink()):
            continue
        target = dest_root / relpath
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target, follow_symlinks=False)
    return dest_root / tree.relative_to(repo_root)


def survives(mutation: Mutation) -> bool:
    """Apply the mutation to a copy and report whether the command still passed."""
    with tempfile.TemporaryDirectory(prefix="gronin-mutation-") as tmp:
        work = copy_tree(mutation.tree, Path(tmp))

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
        # Skipped only for a tree that is not Go, which is the self-test's shell
        # subject. A tree inside a module but not at its root is refused rather than
        # skipped: a mutation scoped to a package directory would otherwise walk past
        # this check in silence and be counted killed again, which is the whole defect
        # above. The question asked is whether a module encloses the tree, not whether
        # the tree holds a .go file — the second answers yes for Go stored as data,
        # which is a fixture rather than something to compile.
        built = None
        if (work / "go.mod").is_file():
            built = run(["go", "test", "-run=^$", "-count=1", "./..."], work)
        elif enclosing_module(mutation.tree) is not None:
            raise ConfigError(
                f"{mutation.name}: {mutation.tree} is inside a Go module but is not its "
                f"root, so the mutated tree cannot be compiled before the command runs; "
                f"point the mutation's tree at the module root"
            )
        if built is not None and built.returncode != 0:
            raise ConfigError(
                f"{mutation.name}: the mutated tree does not compile, so the command "
                f"below would report it killed without ever running the defect:\n"
                f"{built.stdout}{built.stderr}"
            )

        return run(mutation.command, work).returncode == 0


def check(
    mutations: list[Mutation],
    *,
    quiet: bool = False,
    shard: tuple[int, int] | None = None,
    declared: int | None = None,
) -> int:
    """Return the number of mutations nothing noticed.

    `shard` and `declared` only change the summary line's wording, for a run over a
    shard rather than the whole list -- so it reads as a count of that shard, not a
    silently partial report of the total.
    """
    survivors = 0
    for mutation in mutations:
        survived = survives(mutation)
        survivors += survived
        if not quiet:
            print(
                f"check-mutation: {'SURVIVED ' if survived else 'killed   '} {mutation.name}"
            )
    if not quiet:
        if shard is not None:
            k, n = shard
            print(
                f"check-mutation: shard {k}/{n}: {survivors} survivors of "
                f"{len(mutations)} mutants ({declared} declared)"
            )
        else:
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

SELF_TEST_TRACKED_FILES_TEST = """#!/bin/sh
grep -q ok ../NOTES.md && grep -q ok ../docs/guide.txt && [ ! -e ../SECRET.md ]
"""

SELF_TEST_BROKEN = """#!/bin/sh
exit 1
"""

# A Go module, because the compile refusal only applies where there is something to
# compile, and the shell subjects above have nothing. One package, one test, and a
# mutation that leaves an import unused — which is the shape the mutants this
# refusal was written for had.
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

        # copy_tree: a tracked file outside the tree is reachable, an untracked one is not.
        tracked_repo = root / "tracked-repo"
        (tracked_repo / "docs").mkdir(parents=True)
        (tracked_repo / "docs" / "guide.txt").write_text("ok\n")
        (tracked_repo / "NOTES.md").write_text("ok\n")
        tracked_tree = tracked_repo / "tree"
        tracked_tree.mkdir()
        (tracked_tree / "subject.sh").write_text(SELF_TEST_SUBJECT)
        (tracked_tree / "subject.sh").chmod(0o755)
        (tracked_tree / "test.sh").write_text(SELF_TEST_TRACKED_FILES_TEST)
        (tracked_tree / "test.sh").chmod(0o755)
        subprocess.run(["git", "init", "-q"], cwd=tracked_repo, check=True)
        subprocess.run(
            ["git", "add", "docs", "NOTES.md", "tree"], cwd=tracked_repo, check=True
        )
        subprocess.run(
            [
                "git",
                "-c",
                "user.email=t@example.com",
                "-c",
                "user.name=t",
                "commit",
                "-q",
                "-m",
                "x",
            ],
            cwd=tracked_repo,
            check=True,
        )
        (tracked_repo / "SECRET.md").write_text("not tracked\n")

        tracked_mutation = Mutation(
            name="tracked files reachable",
            tree=tracked_tree,
            file="subject.sh",
            find="42",
            replace="41",
            command=["./test.sh"],
        )
        try:
            check([tracked_mutation], quiet=True)
        except ConfigError as err:
            failures.append(
                f"a tracked file a test reads outside the mutation tree was not "
                f"copied, or an untracked one was: {err}"
            )

        # The old behavior, proven insufficient rather than assumed to be.
        tree_only = root / "tracked-repo-tree-only"
        shutil.copytree(tracked_tree, tree_only)
        naive = subprocess.run(
            ["./test.sh"], cwd=tree_only, capture_output=True, text=True
        )
        if naive.returncode == 0:
            failures.append(
                "copying only the tree still satisfied a test that reads outside it"
            )

        # load()'s refusal of a file naming a path outside its tree.
        escape_config = root / "escaping-file.json"
        escape_config.write_text(
            json.dumps(
                {
                    "mutations": [
                        {
                            "name": "escaping file",
                            "tree": str(killed.tree),
                            "file": "../outside.txt",
                            "find": "x",
                            "replace": "y",
                            "command": ["true"],
                        }
                    ]
                }
            )
        )
        try:
            load(escape_config)
        except ConfigError:
            pass
        else:
            failures.append("a file resolving outside its tree was loaded")

        # The same refusal, escaping through a symlink rather than a literal "..".
        symlink_tree = root / "symlink-tree"
        symlink_tree.mkdir()
        outside_target = root / "outside.txt"
        outside_target.write_text("secret\n")
        (symlink_tree / "escaping-link").symlink_to(outside_target)
        symlink_config = root / "escaping-symlink.json"
        symlink_config.write_text(
            json.dumps(
                {
                    "mutations": [
                        {
                            "name": "escaping symlink",
                            "tree": str(symlink_tree),
                            "file": "escaping-link",
                            "find": "x",
                            "replace": "y",
                            "command": ["true"],
                        }
                    ]
                }
            )
        )
        try:
            load(symlink_config)
        except ConfigError:
            pass
        else:
            failures.append("a file that is a symlink escaping its tree was loaded")

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
            # Orphans the strings import, exactly as this refusal's mutants do.
            find='return strings.TrimSpace(" 42 ")',
            replace='return "41"',
            command=["go", "test", "./...", "-count=1"],
        )

        # A tree inside a module but below its root. The compile gate can only run
        # where `go test ./...` resolves, and skipping silently there is how the defect
        # above would come back for a mutation scoped to a package directory rather
        # than the module.
        package = root / "belowroot" / "pkg"
        package.mkdir(parents=True)
        (package.parent / "go.mod").write_text(SELF_TEST_GO_MOD)
        (package / "subject.go").write_text(SELF_TEST_GO_SUBJECT)
        refusals["a tree inside a module but below its root"] = Mutation(
            name="below the module root",
            tree=package,
            file="subject.go",
            find='return strings.TrimSpace(" 42 ")',
            replace='return "41"',
            command=["true"],
        )

        # And the other side of it: Go under no module at all is a fixture, not
        # something to compile, and is skipped rather than refused. Probed here because
        # a refusal tested only on what it refuses is an allowlist in disguise — this
        # is the case the previous "does the tree hold a .go file" test got wrong.
        fixture = root / "fixture"
        fixture.mkdir()
        (fixture / "subject.go").write_text(SELF_TEST_GO_SUBJECT)
        try:
            check(
                [
                    Mutation(
                        name="go under no module",
                        tree=fixture,
                        file="subject.go",
                        find='return strings.TrimSpace(" 42 ")',
                        replace='return "41"',
                        command=["true"],
                    )
                ],
                quiet=True,
            )
        except ConfigError:
            failures.append("Go under no module at all was refused rather than skipped")

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

        # Sharding. The union of every shard must equal the unsharded selection, no
        # mutant may appear in two shards, and a shard with nothing in it -- out of
        # range, or N larger than the list -- must be refused rather than reported
        # as a clean run over zero mutants.
        universe = [
            Mutation(
                name=f"m{i}",
                tree=killed.tree,
                file="subject.sh",
                find="42",
                replace="41",
                command=["./test.sh"],
            )
            for i in range(13)
        ]
        n = 6
        try:
            shards = [apply_shard(universe, (k, n)) for k in range(1, n + 1)]
        except ConfigError as err:
            failures.append(f"sharding the self-test universe was refused: {err}")
            shards = []

        if shards:
            if sorted(m.name for shard in shards for m in shard) != sorted(
                m.name for m in universe
            ):
                failures.append("the union of every shard does not equal the full list")
            seen: set[str] = set()
            for shard in shards:
                names = {m.name for m in shard}
                if seen & names:
                    failures.append("two shards selected the same mutant")
                seen |= names

        if parse_shard("2/6") != (2, 6):
            failures.append("a well-formed --shard value was not parsed as K, N")
        for malformed in (
            "0/6",
            "7/6",
            "1/0",
            "x/6",
            "1/-1",
            "1",
            "1/6/2",
            "-1/6",
            "²/6",  # crashed the old path with ValueError
            "１/6",  # accepted outright by the old isdigit()+int() path
            "٣/6",  # accepted outright by the old isdigit()+int() path
        ):
            try:
                parse_shard(malformed)
            except ConfigError:
                continue
            failures.append(f"--shard {malformed!r} was accepted rather than refused")

        try:
            select_shard(universe, len(universe) + 1, len(universe) + 1)
        except ConfigError:
            pass
        else:
            failures.append(
                "a shard selecting no mutants was accepted rather than refused"
            )

        # End to end: run the real CLI per shard -- the checks above never call main().
        # One fixture mutant is inattentive rather than killed, so the exit code is
        # checked against what each shard should report, not only against "did not
        # crash" -- a main() that stopped propagating check()'s result as its exit
        # code would otherwise go unnoticed as long as it happened not to crash.
        e2e_dir = root / "e2e"
        e2e_tree = e2e_dir / "tree"
        e2e_tree.mkdir(parents=True)
        (e2e_tree / "subject.sh").write_text(SELF_TEST_SUBJECT)
        (e2e_tree / "subject.sh").chmod(0o755)
        (e2e_tree / "test.sh").write_text(SELF_TEST_ATTENTIVE)
        (e2e_tree / "test.sh").chmod(0o755)
        e2e_survivor_tree = e2e_dir / "survivor-tree"
        e2e_survivor_tree.mkdir()
        (e2e_survivor_tree / "subject.sh").write_text(SELF_TEST_SUBJECT)
        (e2e_survivor_tree / "subject.sh").chmod(0o755)
        (e2e_survivor_tree / "test.sh").write_text(SELF_TEST_INATTENTIVE)
        (e2e_survivor_tree / "test.sh").chmod(0o755)

        e2e_survivor_name = "e2e-survivor"
        e2e_names = [e2e_survivor_name] + [f"e2e-{i}" for i in range(6)]
        e2e_config = e2e_dir / "mutations.json"
        e2e_config.write_text(
            json.dumps(
                {
                    "mutations": [
                        {
                            "name": name,
                            "tree": "survivor-tree"
                            if name == e2e_survivor_name
                            else "tree",
                            "file": "subject.sh",
                            "find": "42",
                            "replace": "41",
                            "command": ["./test.sh"],
                        }
                        for name in e2e_names
                    ]
                }
            )
        )
        e2e_n = 6
        # e2e_survivor_name is first in the declared list (position 0), so it lands
        # in the shard where 0 % e2e_n == k - 1.
        e2e_survivor_shard = 0 % e2e_n + 1
        e2e_shards: list[set[str]] = []
        for k in range(1, e2e_n + 1):
            proc = subprocess.run(
                [
                    sys.executable,
                    __file__,
                    "--config",
                    str(e2e_config),
                    "--shard",
                    f"{k}/{e2e_n}",
                ],
                capture_output=True,
                text=True,
            )
            expected_rc = 1 if k == e2e_survivor_shard else 0
            if proc.returncode != expected_rc:
                failures.append(
                    f"the end-to-end shard {k}/{e2e_n} run exited "
                    f"{proc.returncode}, expected {expected_rc}: {proc.stderr}"
                )
                continue
            if (
                k == e2e_survivor_shard
                and f"SURVIVED  {e2e_survivor_name}" not in proc.stdout
            ):
                failures.append(
                    f"the end-to-end shard {k}/{e2e_n} run did not report "
                    f"{e2e_survivor_name!r} as SURVIVED"
                )
            if f"shard {k}/{e2e_n}:" not in proc.stdout:
                failures.append(
                    f"the end-to-end shard {k}/{e2e_n} run's summary line did not "
                    "name its shard"
                )
            e2e_shards.append(
                {
                    line.rsplit(" ", 1)[-1]
                    for line in proc.stdout.splitlines()
                    if line.startswith("check-mutation: killed")
                    or line.startswith("check-mutation: SURVIVED")
                }
            )

        if e2e_shards:
            if set().union(*e2e_shards) != set(e2e_names):
                failures.append(
                    "the union of the shards run end to end through main() does "
                    "not equal the full fixture list"
                )
            seen_e2e: set[str] = set()
            for names in e2e_shards:
                if seen_e2e & names:
                    failures.append(
                        "two shards run end to end through main() selected the "
                        "same mutant"
                    )
                seen_e2e |= names

        for failure in failures:
            print(f"check-mutation: self-test: {failure}", file=sys.stderr)
        if failures:
            return 1

    print(
        "check-mutation: self-test ok — counts zero and one, refuses a broken "
        "baseline, an absent target, a mutant that does not compile, a tree below its "
        "module root, a missing tree and a file escaping its tree, copies a repository's "
        "tracked files but not its untracked ones, and shards partition the full list "
        "while a malformed or empty shard is refused"
    )
    return 0


def _install_cleanup_signals() -> None:
    """Make a termination signal unwind the stack instead of killing the process.

    The cache is removed by its context manager, which only runs if the interpreter
    unwinds. Python already raises KeyboardInterrupt for SIGINT, but a default
    SIGTERM ends the process where it stands and strands the tree -- and SIGTERM is
    exactly how these runs die, since an OOM daemon sends it before resorting to
    SIGKILL. Nothing can be done about SIGKILL itself; _prune_stale_caches is what
    covers that.
    """

    def _unwind(signum: int, _frame: object) -> None:
        raise SystemExit(128 + signum)

    for sig in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, _unwind)


def _cache_root() -> Path:
    """Where the build cache goes: a real filesystem, never TMPDIR.

    /tmp is a tmpfs on this fleet, and a tmpfs page is memory that cannot be paged
    out or reclaimed by killing anything -- a multi-GiB build cache written there
    walks the node into a livelock rather than filling a disk.

    A relative XDG_CACHE_HOME is treated as unset, which is what the XDG base
    directory specification asks for.
    """
    base = os.environ.get("XDG_CACHE_HOME") or ""
    root = (
        (Path(base) if Path(base).is_absolute() else Path.home() / ".cache")
        / "gronin"
        / "mutation"
    )
    try:
        root.mkdir(parents=True, exist_ok=True)
    except OSError as err:
        raise ConfigError(
            f"cannot create the build cache directory {root}: {err}"
        ) from err
    return root


@contextlib.contextmanager
def _claimed(cache: Path):
    """Hold a lock inside the cache for as long as this run uses it.

    The lock is what tells a later run whether the tree is still in use. It is held
    by the kernel against an open descriptor, so it is released by any death of this
    process, SIGKILL included -- which an mtime or a recorded pid cannot manage.
    """
    fd = os.open(cache / ".lock", os.O_CREAT | os.O_RDWR, 0o600)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as err:
            # A filesystem that cannot do advisory locks -- a network-mounted home
            # answering ENOLCK, say. The cache is then unprotected from another run's
            # prune, which costs a rebuild; refusing to run at all costs the whole run.
            print(
                f"check-mutation: cannot lock the build cache ({err}); "
                "a concurrent run may prune it",
                file=sys.stderr,
            )
        yield
    finally:
        os.close(fd)


def _is_orphan(cache: Path, min_age_s: int) -> bool:
    """True when no live run holds this cache and it is old enough to be sure.

    The age is not the test, only a guard against the window between mkdtemp and
    the flock: a tree created seconds ago may not have claimed its lock yet.
    """
    try:
        if time.time() - cache.stat().st_mtime < min_age_s:
            return False
    except OSError:
        return False

    lock = cache / ".lock"
    if not lock.exists():
        return True
    try:
        fd = os.open(lock, os.O_RDWR)
    except OSError:
        return False
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return True
    except OSError:
        return False
    finally:
        os.close(fd)


def _prune_stale_caches(root: Path, min_age_s: int = 900) -> None:
    """Drop caches an earlier run left behind.

    The context manager removes the tree on a normal exit and on a signal that
    unwinds the interpreter, but nothing runs on SIGKILL -- and SIGKILL is how a
    run that exhausts memory ends. Without this the orphans accumulate for as long
    as the filesystem survives.
    """
    for path in root.glob("mutation-cache-*"):
        if path.is_dir() and _is_orphan(path, min_age_s):
            shutil.rmtree(path, ignore_errors=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--config",
        type=Path,
        default=DEFAULT_CONFIG,
        help="the mutations.json to check (default: the repository's own)",
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument(
        "--self-test", action="store_true", help="prove the harness before trusting it"
    )
    mode.add_argument(
        "--shard",
        metavar="K/N",
        help="check only the mutants at position K-1 modulo N (1-based K); shards "
        "for a fixed N are disjoint and their union is the full declared list",
    )
    args = parser.parse_args()

    # Parsed once, here, rather than again wherever the result is needed: a
    # malformed --shard is a configuration mistake, caught before the cache
    # directory or anything else this run touches is set up.
    shard: tuple[int, int] | None = None
    if args.shard is not None:
        try:
            shard = parse_shard(args.shard)
        except ConfigError as err:
            print(f"check-mutation: {err}", file=sys.stderr)
            return 2

    # A build cache of its own, thrown away when the run ends. Every mutant is a fresh
    # copy of the tree at a fresh path, so the compiler treats it as a distinct source
    # tree and writes a distinct set of entries; a day of runs against the developer's
    # own cache took it to 46 GB, and Go trims on five days of disuse rather than on
    # size. Shared across the mutants of one run, so the standard library and the
    # dependencies are compiled once here rather than once per mutant, and cold at the
    # first mutant of every run — that warmth is what this trades away.
    # It is placed under the user cache directory rather than left to TMPDIR: /tmp is a
    # tmpfs on this fleet, so a cache that reached 2.4 GiB there was 2.4 GiB of RAM that
    # no swap could page out and no OOM kill could reclaim, which livelocked the machine.
    _install_cleanup_signals()
    try:
        cache_root = _cache_root()
    except ConfigError as err:
        print(f"check-mutation: {err}", file=sys.stderr)
        return 2
    _prune_stale_caches(cache_root)
    with (
        tempfile.TemporaryDirectory(prefix="mutation-cache-", dir=cache_root) as cache,
        _claimed(Path(cache)),
    ):
        os.environ["GOCACHE"] = cache
        try:
            if args.self_test:
                return self_test()
            declared = load(args.config)
            mutations = apply_shard(declared, shard)
            return 1 if check(mutations, shard=shard, declared=len(declared)) else 0
        except ConfigError as err:
            print(f"check-mutation: {err}", file=sys.stderr)
            return 2


if __name__ == "__main__":
    sys.exit(main())
