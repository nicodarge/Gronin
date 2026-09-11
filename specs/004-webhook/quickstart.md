# Phase 1 — Quickstart

How an operator checks, on a real host and against a real sender, that the ingress does what the
suite says it does. The suite signs its own deliveries; what it cannot show is that a sender written
by someone else produces a signature this ingress accepts. This is a validation rather than a gate,
and nothing in it has been run: it is written before the code it describes.

## Prerequisites

- A host set up as in the runtime core's [quickstart](../001-runtime-core/quickstart.md) §1:
  `gronin` installed, the agent authenticated. Below it is reachable at `192.0.2.10`.
- A sender that signs the exact body with HMAC-SHA256 in a header of its own — a Forgejo or Gitea
  repository webhook, or a Grafana contact point with its timestamp header left unset (research.md
  §7). Below, a Forgejo instance at `https://forge.example.com`.
- A secret for the source, in a file only the operator can read, below `/path/to/forge-hook-secret`.

The source, in `sources.json` in the state directory, as [contracts/cli.md](./contracts/cli.md)
describes it:

```json
{
  "forge": {
    "secret": "${config.forge_hook_secret}",
    "signature_header": "X-Forgejo-Signature",
    "identity": "/delivery_uuid"
  }
}
```

```bash
gronin config set --secret forge_hook_secret < /path/to/forge-hook-secret
```

And a playbook bound to it, differing from `examples/doc-check.yaml` in its trigger and one gather
step — the repository name, declared, held to a pattern, and bound into a command as one word:

```yaml
trigger:
  type: webhook
  source: forge
  values:
    repository:
      at: /repository/full_name
      pattern: '[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+'
      max_length: 200

gather:
  - run: printf '%s\n' ${trigger.repository}
    as: repository.txt
```

A local signer for the steps below that do not use the real sender. It reads the key from a file, so
the secret is never on a command line:

```bash
sign() {  # sign <key-file> < body
  python3 -c 'import hmac, hashlib, sys
key = open(sys.argv[1], "rb").read().rstrip(b"\n")
print(hmac.new(key, sys.stdin.buffer.read(), hashlib.sha256).hexdigest())' "$1"
}
```

## 1. Nothing listens until asked (FR-305)

Start `gronin serve` with no `--ingress-address`. It refuses: the playbook has a webhook trigger.
Start it with `--ingress-address 0.0.0.0:8443`. It names the ingress and says that repeat detection
reaches this host only (FR-319).

## 2. A real sender's delivery becomes one run (SC-301)

In Forgejo, add a webhook to a repository with the target `http://192.0.2.10:8443/hooks/forge` and
the same secret, and send its test delivery. Forgejo's delivery log shows `202`. `gronin deliveries`
shows one delivery, `handed_off`, and the run it became; `gronin show` on that run names the
delivery, and its gathered inputs include `trigger.json` holding the repository name and nothing
else.

Redeliver the same delivery from Forgejo's log. The answer is `202`, the delivery's repeat count is
one, and no second run exists — Forgejo resends the same `delivery_uuid`, which is the identity this
source declared.

## 3. A payload cannot steer the run (SC-317, SC-318)

Rename a test repository to a name the pattern refuses, and push. The delivery is `202`, no run
follows, and `gronin deliveries refused` names the playbook, the value and the reason. The whole body
is readable through `gronin deliveries show`, and nowhere else: the run that did not happen has no
working directory, and the runs from step 2 hold only `trigger.json`'s declared value.

## 4. A stranger gets nothing (SC-309, SC-312, SC-326)

From another machine:

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://192.0.2.10:8443/runs              # 404
curl -s -o /dev/null -w '%{http_code}\n' -X POST --data '{}' \
  http://192.0.2.10:8443/hooks/forge                                               # 403
curl -s -o /dev/null -w '%{http_code}\n' -X POST --data '{}' \
  http://192.0.2.10:8443/hooks/nosuchsource                                        # 403
```

The two `403` bodies are identical. `gronin deliveries refused` shows counts for this minute, the
unknown name counted under `(unconfigured)`, and no body from any of them.

## 5. A kill between the record and the run (SC-304)

Send a signed delivery with `sign` and `curl`, and kill `serve` with `SIGKILL` as the `202` arrives.
Whether the kill landed before or after the hand-off is not something this step can choose — the
suite forces it — so read what happened: `gronin deliveries` shows the delivery `handed_off` with its
run, or `dropped` with nothing run. If it is dropped, start `serve` again and resend the same body: it
is handed off once, and runs.

## 6. Behind a TLS proxy (FR-334)

Put a TLS-terminating reverse proxy in front of the ingress and point the sender at it. Deliveries
still verify, since the signature covers the body and not the transport, and the recorded peer is
the proxy's address, whatever forwarding header the proxy adds.
