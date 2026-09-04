#!/usr/bin/env python3
"""Check that every FR-, SC- and T- reference in a feature's documents resolves.

Twice in one session an identifier was renumbered or appended and a reference to the old
one survived in a neighbouring file: `plan.md` kept pre-renumbering FR numbers, and after
those were fixed `research.md` still cited FR-030 for a credential requirement that had
become FR-032. Neither failed anything. Both were found by a reviewer.

The pattern is always the same — the edited file is corrected and the adjacent one is not
— so this sweeps every file in the feature directory rather than the ones just touched.
That is the whole point: a check that only looks where you were already looking would have
missed both.

It also reports identifiers nothing references, which is how a requirement ends up with no
task and a success criterion with no test.
"""

from __future__ import annotations

import re
import sys
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SPECS = ROOT / "specs"

DEFINES = {
    "FR": re.compile(r"^- \*\*(FR-\d+)\*\*:", re.M),
    "SC": re.compile(r"^- \*\*(SC-\d+)\*\*:", re.M),
    "T": re.compile(r"^- \[[ x]\] (T\d+)", re.M),
}
REFERENCE = re.compile(r"\b((?:FR|SC)-\d+|T\d{2,4})\b")


def check_feature(feature: Path) -> int:
    docs = sorted(feature.rglob("*.md"))
    if not docs:
        return 0

    defined: dict[str, Path] = {}
    duplicates: list[tuple[str, Path]] = []
    for doc in docs:
        text = doc.read_text()
        for pattern in DEFINES.values():
            for ident in pattern.findall(text):
                if ident in defined:
                    duplicates.append((ident, doc))
                else:
                    defined[ident] = doc

    referenced: dict[str, set[str]] = defaultdict(set)
    for doc in docs:
        text = doc.read_text()
        for line_no, line in enumerate(text.split("\n"), 1):
            for ident in REFERENCE.findall(line):
                referenced[ident].add(f"{doc.relative_to(ROOT)}:{line_no}")

    failures = 0
    name = feature.relative_to(SPECS)

    dangling = {i: s for i, s in referenced.items() if i not in defined}
    for ident in sorted(dangling):
        for where in sorted(dangling[ident]):
            print(f"{where}: references {ident}, which nothing defines")
            failures += 1

    for ident, doc in duplicates:
        print(f"{doc.relative_to(ROOT)}: {ident} is defined more than once")
        failures += 1

    # An identifier only its own definition mentions is not wired to anything.
    orphans = [
        ident
        for ident, doc in defined.items()
        if ident.startswith(("FR-", "SC-"))
        and len({w.rsplit(":", 1)[0] for w in referenced.get(ident, set())}) < 2
    ]
    for ident in sorted(orphans):
        print(
            f"{defined[ident].relative_to(ROOT)}: {ident} is defined but referenced nowhere else"
        )
        failures += 1

    if failures:
        print(f"\n{name}: {failures} problem(s).")
    else:
        print(
            f"check-spec-refs: {name}: {len(defined)} identifiers, all resolved and referenced."
        )
    return failures


def main() -> int:
    if not SPECS.is_dir():
        print("check-spec-refs: no specs/ directory; nothing to check.")
        return 0
    features = [p for p in sorted(SPECS.iterdir()) if p.is_dir()]
    if not features:
        print("check-spec-refs: no feature directory; nothing to check.")
        return 0
    return 1 if sum(check_feature(f) for f in features) else 0


if __name__ == "__main__":
    sys.exit(main())
