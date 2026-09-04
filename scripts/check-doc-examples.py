#!/usr/bin/env python3
"""Validate every playbook example in the documentation against the published schema.

A published example is the first thing a reader copies, so an example the loader would
refuse teaches something that does not work. Two such examples shipped here before this
check existed: one carried `guard` and `retrieve` blocks the same document declared
refused, and one wrote `timeout: 1800` where the schema requires a suffixed string. Both
were found by a reviewer rather than by anything automatic, which is why this exists.

An example opts in by tagging its fence `yaml playbook`. Nothing else is read, so a YAML
block showing a fragment or a counter-example is not swept up by accident.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

try:
    import jsonschema
    import yaml
except ImportError as exc:  # pragma: no cover - environment problem, not a finding
    print(f"check-doc-examples: missing dependency: {exc.name}", file=sys.stderr)
    print("install with: pip install jsonschema pyyaml", file=sys.stderr)
    sys.exit(2)

ROOT = Path(__file__).resolve().parent.parent
SCHEMA = ROOT / "specs/001-runtime-core/contracts/playbook.schema.json"
FENCE = re.compile(r"^```yaml playbook\s*$(.*?)^```\s*$", re.M | re.S)

# Interpolation must name its source (FR-039). The schema is the shape layer and cannot
# express this, so the check that a *documented* example obeys it lives here.
BARE_REF = re.compile(r"\$\{(?!config\.|trigger\.)[^}]*\}")


def examples(paths: list[Path]) -> list[tuple[Path, int, str]]:
    found = []
    for path in paths:
        text = path.read_text()
        for match in FENCE.finditer(text):
            line = text[: match.start()].count("\n") + 1
            found.append((path, line, match.group(1)))
    return found


def main(argv: list[str]) -> int:
    if not SCHEMA.exists():
        print(f"check-doc-examples: schema not found at {SCHEMA}", file=sys.stderr)
        return 2

    validator = jsonschema.Draft202012Validator(json.loads(SCHEMA.read_text()))

    paths = [Path(a) for a in argv[1:]] or sorted(
        {*ROOT.glob("docs/**/*.md"), *ROOT.glob("specs/**/*.md"), *ROOT.glob("*.md")}
    )
    paths = [p for p in paths if p.suffix == ".md" and p.exists()]

    found = examples(paths)
    failures = 0

    for path, line, body in found:
        where = f"{path.relative_to(ROOT)}:{line}"
        try:
            doc = yaml.safe_load(body)
        except yaml.YAMLError as exc:
            print(f"{where}: not valid YAML: {exc}")
            failures += 1
            continue

        for error in sorted(validator.iter_errors(doc), key=lambda e: list(e.path)):
            field = ".".join(str(p) for p in error.path) or "(root)"
            print(f"{where}: {field}: {error.message}")
            failures += 1

        for ref in BARE_REF.findall(body):
            print(f"{where}: interpolation {ref} omits its namespace (FR-039)")
            failures += 1

    if not found:
        print(
            "check-doc-examples: no example found — is the fence tagged '```yaml playbook'?"
        )
        return 1

    plural = "s" if len(found) != 1 else ""
    if failures:
        print(
            f"\n{failures} problem(s) across {len(found)} documented example{plural}."
        )
        return 1

    print(f"check-doc-examples: {len(found)} documented example{plural}, all accepted.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
