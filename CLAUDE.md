# CLAUDE.md — Gronin

## Language

Everything in this repository is written in English: code, comments,
documentation, commit messages, pull request titles and bodies, branch names.

## This repository is public

It has been public since 2026-09-13, and GitHub Pages has served the playbook
schema since 2026-09-07. Anything committed here, history included, can be read
by everyone, and rewriting history to remove it is expensive and unreliable.

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
- Merge once every required check is green — standing approval, given 2026-09-08, for this
  repository. `gate` is the required check and it needs all the others; no checks reported is
  not green. This is approval to merge, not to skip the review that precedes it.

## Releases

Never create a tag and never write a version by hand. A push to `production` runs CI, and
when its `gate` passes `.github/workflows/release.yaml` runs semantic-release (`.releaserc`)
on that commit. The squash commit's whole message decides the version, as the real
`@semantic-release/commit-analyzer` measures it with this `.releaserc`:

- The subject's type: `fix:` and `perf:` give a patch, `feat:` a minor, `!` after the type a
  major; `ci:`, `docs:`, `chore:`, `test:`, `refactor:`, `build:` and `style:` release
  nothing.
- A body line that STARTS with `BREAKING CHANGE` (any case) followed on the same line by `:`
  or whitespace and more text gives a major. Behind a star bullet or indentation it still
  counts, as in a pull request description pasted into a squash body; behind a dash or a plus
  bullet it does not, and neither do `BREAKING-CHANGE` and `BREAKING CHANGES`.
- A squash titled `Revert "..."` whose body says `This reverts commit ...` gives a patch,
  whatever the type it reverts.

When a release is cut, the same run builds the binaries from the tag, signs them, attaches
them to the GitHub release and pushes the image (`gronin:<tag>` and `gronin:latest`). The
version `gronin version` prints is the tag, stamped in by `-X main.version`. The only commit
semantic-release makes is `chore(release): <version> [skip ci]`, which writes `CHANGELOG.md`
as `github-actions[bot]` (set in the release step's `env:`); that file is excluded from
markdownlint because it is written by semantic-release.

- A tag pushed by hand starts nothing: nothing triggers on a tag.
- The `chore(release)` commit is pushed with the write deploy key in the secret
  `RELEASE_SSH_KEY`, because `production`'s ruleset requires `gate` and that commit has no
  checks, so the job token's push is refused. The ruleset's bypass actor is the `DeployKey`
  category, not one key: any other write deploy key on this repository can push to
  `production` past `gate`, so add none.
- `RELEASE_SSH_KEY` is an environment secret of the GitHub Environment `release`, whose
  deployment branches are limited to `production`, and only the `release` job names that
  environment. A repository secret on a public repository is readable by a workflow on any
  branch; an environment secret is released only to a run on a permitted branch.
- The key is loaded into an ssh-agent by the step `Load the release deploy key`, which runs
  no npm. The step that runs semantic-release and its npm tree never had the key in its
  environment or in `/proc/<pid>/environ`: it is given the agent socket, so it can push while
  the job runs but an unprivileged process cannot read the key (a hosted runner grants
  passwordless sudo, so the confinement does not hold against root). `actions/checkout`
  never gets the key and it never touches the disk. The key's lifetime is set next to the
  job's timeout in `release.yaml` and equals it. The agent's identities are removed through
  its socket and the agent is stopped right after semantic-release. The workflow fails
  before releasing anything when the secret is empty, and a dry-run push right before
  semantic-release makes a deleted or read-only key fail with the SSH error rather than
  semantic-release falling back to an HTTPS URL built from the job token; a missing ruleset
  bypass is not caught there and shows when the changelog commit is pushed. Losing the key
  stops releases loudly.
- `[skip ci]` in the release commit's message is what keeps that push from starting CI and,
  through it, another release run.
- The workflow reacts to the workflow named `CI`. Renaming it in `ci.yaml` stops releases
  without failing anything.
- The toolchain is pinned with its whole dependency tree in `.github/release/`
  (`package.json`, `package-lock.json`) and installed by `.github/release/install.sh` with
  `npm ci` outside the workspace, because this is a Go repository with no root
  `package.json`. `@semantic-release/npm`, and the npm it bundles, carry advisories and
  nothing here loads them, so `.github/release/stubs/semantic-release-npm` replaces them; it
  throws if `.releaserc` ever names the plugin. Bump a version in `package.json`, then run
  `npm install --package-lock-only --ignore-scripts` in that directory and commit both files.
  Both workflows that install it ask `actions/setup-node` for a range that satisfies the
  plugins' `engines` and run `npm ci --engine-strict`: keep the two ranges equal.
- The workflow `release-toolchain.yaml` checks the toolchain on every pull request, on a
  hosted runner, with no secret: it installs it as the release job does, loads
  semantic-release and every plugin `.releaserc` names, and runs `npm audit`. It has no
  `paths` filter so it can be made a required check without blocking unrelated pull requests.
- `scripts/release-guard.sh` holds the release job's decisions about commits and tags (the
  tip, the repair of a tag, whether a tag is the newest release). A release commit is
  recognised by its subject and by touching nothing but `CHANGELOG.md`, never by its author.
  `scripts/release-guard.sh --self-test` runs in the CI job `release-guard-refuses`, which
  `gate` needs, and asserts that the message in `.releaserc` matches the subject pattern:
  change them together.
- Each artefact is signed keylessly into its own `<artefact>.bundle`, and the `publish` job
  verifies every bundle against the identity the README documents before attaching it.
  `v0.1.0`, built before that, carries `.sig` and `.pem` files instead.
- The GitHub release exists before its assets: `binaries` and `publish` run after it. If
  either fails the release stays without assets until they are re-run.
- A failure after the tag exists (the binaries, signing or the image) is repaired with "Re-run
  failed jobs" on that run. A failure in the `release` job after it pushed the tag is not:
  semantic-release then sees the tag as the last release. A changelog pushed with no tag is
  repaired by "Re-run all jobs", which releases the same version again and leaves a duplicated
  `CHANGELOG.md` entry.
- The workflow also runs by hand (Actions, Release, "Run workflow") and only on `production`,
  in two modes. With the `tag` input empty it repairs a lost `workflow_run` event: it takes
  the newest commit on `production` that is not a release commit, requires a successful `gate`
  check run from GitHub Actions on it, and then proceeds as a CI-triggered run. With `tag` set
  it accepts only a tag that is an ancestor of `production` and points at the release commit
  semantic-release made for that version. A tag made by hand is refused, so `v0.1.0`, tagged
  by hand before this workflow existed, cannot be repaired this way. For an accepted tag it
  skips semantic-release, creates the GitHub release when there is none (marked latest only
  when the tag is the newest release tag on `production`), refuses a release that is a draft
  with a message saying to publish or delete it, and builds, signs and publishes the
  binaries and the image for it again.
- `:latest` is decided in the `image` job, right before the push, from the live tags: it moves
  only when the tag being built is the newest release tag merged into `production`, a release
  tag being one that points at a release commit, so a stray hand-made tag does not count.
  Re-running an older run's image job therefore does not take it back. The `image` job runs
  `release-guard.sh` as it is on `production`, because the tag it checks out may predate it.
- A concurrency group keeps one running and one pending job, and a newer pending job replaces
  an older one: a queued CI-triggered release can be dropped by a later run or dispatch, and
  likewise a queued `image` job. GitHub's `queue: max` would keep every pending one, but the
  pinned actionlint rejects the key (`unexpected key "queue" for "concurrency" section`), so
  it is not used. Repair a dropped release with a dispatch without `tag`, a dropped image with
  a dispatch with `tag`.

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
