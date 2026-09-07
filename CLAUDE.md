# CLAUDE.md — Gronin

## Language

Everything in this repository is written in English: code, comments,
documentation, commit messages, pull request titles and bodies, branch names.

## Part of this repository is already public

It is private today, but GitHub Pages has served the playbook schema publicly
since 2026-09-07, and the repository's own switch to fully public can come at
any time, with no date attached. So the rule below is not a grace period
waiting for a deadline — it holds now: anything committed here is committed to
something that can be read by everyone, and rewriting history to remove it is
expensive and unreliable.

So the rule is absolute and has no exception for convenience:

- **Nothing from the Groniko infrastructure enters this repository.** No
  hostnames, no IP addresses, no eyaml ciphertext, no Hiera keys, no tokens,
  no Outline document IDs, no Discord channel or webhook IDs, no NetBox
  identifiers. Not in code, not in a comment, not in an example, not in a test
  fixture, not in a commit message.
- **Examples use example values.** `example.com`, `192.0.2.0/24` (RFC 5737),
  `REPLACE_ME`. A real value that happens to be harmless today is still a real
  value tomorrow.
- **Playbooks that only make sense against Groniko stay in the Puppet
  repository.** What ships here is the runtime and the playbooks that work
  against any fleet.

`gitleaks` runs as a pre-commit hook with the extra rules in `.gitleaks.toml`.
It is the belt, not the fix — it catches shapes it already knows, and an
infrastructure hostname is not one of them.

## Git workflow

- Default branch: `production`. Never commit to it directly — `no-commit-to-branch` refuses it.
- Branch before any modification: `git checkout -b <name> && git push --set-upstream origin <name>`
- Branch naming: `add_<feature>`, `fix_<issue>`, `update_<component>`, `remove_<item>`
- Conventional commits: `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`
- Squash merge only: `gh pr merge --squash`
- Never merge without explicit approval from the repository owner.

## Validation

Run `pre-commit run --files <files>` after every modification and fix
everything it reports before considering the work complete. Run the hook, not
the underlying tool — a hook can load plugins the bare command does not.

## Tests

What the suite has to be is Principle VI of
[.specify/memory/constitution.md](.specify/memory/constitution.md). Read it there; a
paraphrase here would drift from it silently.

`scripts/check-mutation.py` is how a test is shown to be able to fail.

## Documentation

State facts. Do not write a version number or a count into a README or into
this file: both go stale, nothing fails when they do, and the next reader
trusts them. Name the thing, and point at the command that derives the number.

Comments earn their place by telling a reader something the code cannot. A
block narrating what the code below it already says is noise. Rationale goes
in the commit message, where it is dated and out of the way.
