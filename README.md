# Gronin

A runtime that turns infrastructure signals into bounded agent runs.

> **Status: the runtime core runs, and the guard stage is enforced.** A playbook on a
> schedule gathers its inputs, drives one bounded agent, and delivers a report; unsafe
> playbooks are refused before anything is armed; every run is recorded, replayable and
> resumable. A playbook's rate limit and non-concurrency are held on one host by default,
> and across every host in the deployment when `coordination.json` names an etcd backend —
> see [specs/002-guard/contracts/cli.md](specs/002-guard/contracts/cli.md) — trading the "one
> static binary, nothing beside it" distribution story for that guarantee only where a
> deployment asks for it. See [docs/roadmap.md](docs/roadmap.md) for what is left.

## Running it in a container

The published image carries the runtime and nothing else — not the agent, which is a
separate executable with its own release cadence and its own credentials. Mount it, or
build on top of the image, and name it with `--agent`.

It is built for `linux/amd64`. The release also publishes `linux/arm64` and `darwin/arm64`
binaries; on those, run the binary rather than the image.

The state directory is `/data`, owned by the unprivileged user the image runs as. A named
volume inherits that ownership; a bind mount does not — it takes the host directory's,
which is usually root, and there is no shell in the image to fix it at runtime. Use a
named volume, or `chown 65532:65532` the host directory first.

## What it is

An alert fires, a schedule elapses, a pull request opens. Gronin picks the
signal up, gathers the context that makes it intelligible, retrieves what was
learned the last time something like it happened, hands all of it to an agent
whose reach is declared and enforced, and routes the result somewhere a human
will actually see it.

Six stages, declared in one file:

```text
trigger → guard → gather → retrieve → agent → sink
```

| Stage | What it does |
| ----- | ------------ |
| `trigger` | Webhook, cron or manual invocation |
| `guard` | Claim, rate limit and a waiting slot — before anything is spent |
| `gather` | Commands and HTTP calls that build the input the agent reads |
| `retrieve` | Semantic search over past incidents and documentation |
| `agent` | One agent run, on a declared model, with a declared and enforced tool set |
| `sink` | Chat, knowledge base, issue tracker, metrics |

## Why it exists

Two things already do this, badly, in most homelabs and small platform teams:
a visual workflow tool wiring an LLM call between HTTP nodes, and a pile of
shell scripts behind a CI runner. Both re-implement the same six stages, and
neither can be reviewed, tested or shared.

The stages are not the interesting part. What is interesting is that each one
is a place where unattended agents usually go wrong, and where this runtime
takes a position:

- **`guard` runs before the agent, not after.** Deduplication is anchored on
  the runtime's own clock, never on a timestamp carried in the payload — an alerting
  system re-sends a firing alert with its original start time, so ageing a
  re-fire against it makes every one of them look seconds old and silently
  suppresses the investigation.
- **`agent.tools` is a bound, not a wish.** It is validated when the playbook
  loads, and a playbook that asks for an unrestricted shell is refused at
  startup rather than discovered in production. Handing bash to a model on
  someone else's machine is the one failure here that harms more than its
  author.
- **`sink` is what acts.** The agent reasons and reports; the runtime is what
  opens the issue or posts the message. A run that goes wrong produces a bad
  report, not a bad action.

## Quickstart

Install the binary from the latest release, and make sure the agent it drives (`claude`
by default, or name another with `--agent`) is on the `PATH`. Download it with its checksums
and signature, and verify before installing:

```bash
base=https://github.com/nicodarge/Gronin/releases/latest/download
curl -fsSLO "$base/gronin-linux-amd64"
curl -fsSLO "$base/SHA256SUMS"
curl -fsSLO "$base/SHA256SUMS.bundle"
sha256sum --ignore-missing -c SHA256SUMS
cosign verify-blob \
  --bundle SHA256SUMS.bundle \
  --certificate-identity "https://github.com/nicodarge/Gronin/.github/workflows/release.yaml@refs/heads/production" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
```

`SHA256SUMS` is signed keylessly by the release workflow, so a bundle that verifies with
[cosign](https://docs.sigstore.dev/cosign/) ties the checksums, and through them the binary,
to this repository's release workflow. Each binary has its own `.bundle` in the release too.
The commands here need cosign 2.4.2 or later. Stop if `sha256sum` or `cosign` reports a
failure; otherwise install:

```bash
chmod +x gronin-linux-amd64 && sudo mv gronin-linux-amd64 /usr/local/bin/gronin
gronin version
```

v0.1.0 predates the bundles: its release carries `SHA256SUMS.sig` and `SHA256SUMS.pem`, and
its identity ends `@refs/tags/v0.1.0`. To install it, set `base` to
`https://github.com/nicodarge/Gronin/releases/download/v0.1.0`, build a bundle from those
files and verify it: replace the `curl` line for the bundle and the `cosign` command with

```bash
curl -fsSLO "$base/SHA256SUMS.sig"
curl -fsSLO "$base/SHA256SUMS.pem"
cosign bundle create --artifact SHA256SUMS --signature SHA256SUMS.sig \
  --certificate SHA256SUMS.pem --out SHA256SUMS.bundle
cosign verify-blob \
  --bundle SHA256SUMS.bundle \
  --certificate-identity "https://github.com/nicodarge/Gronin/.github/workflows/release.yaml@refs/tags/v0.1.0" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  SHA256SUMS
```

`gronin version` also prints the agent version it found, and says so if it is missing or
below the floor.

From a clone of this repository, copy the shipped example into the state directory
(`gronin state-dir` prints where that is):

```bash
mkdir -p "$(gronin state-dir)/playbooks"
cp examples/doc-check.{yaml,prompt} "$(gronin state-dir)/playbooks/"
```

The example reads a local checkout and opens issues on a GitHub repository. Both are
configuration values, not playbook fields. `config set` reads the value from standard
input, never from its own command line, so feed it from a variable or a file rather than
typing a secret into the shell:

```bash
printf '%s' "$HOME/some-repository" | gronin config set checkout
printf '%s' 'example-owner/example-repo' | gronin config set repo
```

A report with no findings opens nothing; one with findings fails at the sink until it has a
token. Add `token: ${config.github_token}` under `github:` in the playbook, and store it
as a secret so it is redacted everywhere it would otherwise be printed or recorded:

```bash
printf '%s' "$GITHUB_TOKEN" | gronin config set github_token --secret
```

Check it, run it once by hand, and look at what happened:

```bash
gronin validate              # the load gate; names every refusal and what would be accepted
gronin run doc-check         # now, ignoring the schedule
gronin runs
gronin show <run>            # gathered inputs, tool calls, refusals, cost, what each sink did
```

Iterate on the prompt without paying for delivery twice, or on delivery without paying for
the agent again:

```bash
gronin replay <run>          # re-runs the agent against the recorded inputs
gronin resume <run>          # re-runs only the sinks against the recorded report
```

Then leave it running. `serve` validates every playbook and arms nothing if any is refused:

```bash
gronin serve
```

The longer walkthrough, including what each step refuses, is
[specs/001-runtime-core/quickstart.md](specs/001-runtime-core/quickstart.md).

## Releases

Releases are cut by semantic-release from the commit history, once CI passes on `production`.
The tag is `v<version>`, the release notes are on the GitHub release and in `CHANGELOG.md`, and
`gronin version` prints the tag. Each release carries the binaries, `SHA256SUMS` and their
signature bundles, and publishes `ghcr.io/nicodarge/gronin:<tag>` and `:latest`. How a version
is decided and how the pipeline is protected and repaired: [CLAUDE.md](CLAUDE.md), "Releases".

## Licence

MIT — see [LICENSE](LICENSE).
