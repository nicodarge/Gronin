# Feature Specification: Webhook Trigger

**Feature Branch**: `add_webhook_spec`

**Created**: 2026-09-10

**Status**: Draft

**Input**: User description: "The webhook trigger: a playbook started by an HTTP delivery from
outside the deployment. Its own listener, authenticated per source, answering only once the
delivery is durably recorded; an event identity and the deduplication the guard left to it; and a
payload that reaches the playbook without choosing which playbook runs or becoming a
prompt-injection channel."

## Clarifications

### Session 2026-09-10

- Q: The runtime core serves one listener, and it answers `GET /runs/{id}` — prompts, tool calls,
  gathered inputs. Does the webhook share it? → A: **no — two distinct listeners. The owner's
  decision, 2026-09-10.** The webhook ingress listens on its own address and authenticates every
  delivery per source: an HMAC signature over the body, under a per-source secret held in the
  deployment's configuration, compared in constant time, with a replay window anchored on the
  runtime's own clock. The operator API stays on loopback by default and is opened elsewhere only by
  explicit configuration, on the terms the runtime core already imposes. What the sender can reach
  and what the operator can read are separated by the listener, not by a route table inside one.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A signed delivery becomes one run (Priority: P1)

An operator declares a source in the deployment's configuration — a name and a secret — and writes
a playbook whose trigger is a webhook bound to that source. Something outside the deployment sends
a signed JSON delivery to the ingress. The runtime records it, answers that it was accepted, and
hands it to the guard; the playbook runs once. If the sender did not hear the answer and sends the
same delivery again, it is told the delivery was accepted and nothing runs a second time.

**Why this priority**: it is the feature. Everything else here narrows what a delivery may do; this
is what makes a delivery do anything.

**Independent Test**: start the built executable with one source and one playbook bound to it,
driving the stub agent. Sign a body with the source's secret, send it to the ingress on loopback,
and confirm one run is recorded as webhook-triggered and linked to the delivery. Send the same body
again and confirm the answer is an acceptance and no second run exists.

**Acceptance Scenarios**:

1. **Given** a source and a playbook bound to it, **When** a correctly signed delivery arrives,
   **Then** the ingress answers that it was accepted only after the delivery is recorded, and one
   run of that playbook follows.
2. **Given** a delivery already accepted, **When** the same delivery arrives again inside the
   source's replay window, **Then** the answer is an acceptance, no run starts, and the original
   delivery's record counts the repeat.
3. **Given** a delivery accepted once, **When** the same delivery arrives after the replay window
   has passed on the runtime's clock, **Then** it is treated as a new delivery.
4. **Given** a delivery accepted, **When** the runtime restarts and the same delivery arrives
   inside the window, **Then** it is recognised as a repeat.
5. **Given** a record store that does not complete the write in time, **When** a delivery arrives,
   **Then** the ingress answers that the delivery was not accepted, so the sender retries — and if
   the write lands after that answer, exactly one run follows and the retry is recognised as a
   repeat.
6. **Given** a delivery recorded but not yet handed to the guard, **When** the process is killed,
   **Then** nothing runs from it on restart, its record says it was dropped, and a retry of it from
   the sender is handed to the guard rather than treated as a repeat.
7. **Given** several playbooks bound to one source, **When** one delivery arrives, **Then** each of
   them is handed the delivery once, and each one's guard decision is its own.

---

### User Story 2 - The ingress cannot read the record, and the record cannot be reached from the ingress (Priority: P1)

A deployment exposes its ingress to the network so a sender can reach it. Nothing on that address
reveals a run, a prompt or a gathered input, and a request that is not a correctly signed delivery
from a configured source achieves nothing except a count.

**Why this priority**: also P1, and not by preference. It is the owner's decision, and Constitution
Principle I refuses a listener that widens what a stranger can reach: the operator API exposes
everything a run saw, so an ingress that shares it turns every deployment that accepts webhooks into
one that publishes its records.

**Independent Test**: start the runtime with both listeners on loopback ports. Request a run's record
from the ingress address and confirm it is not found; send a delivery to the API address and confirm
it is not found; send unsigned, mis-signed and unknown-source deliveries and confirm no run, no
stored body, and identical answers for the last two.

**Acceptance Scenarios**:

1. **Given** a run in the record, **When** its record is requested on the ingress address, **Then**
   the answer is not-found and carries nothing from the record.
2. **Given** the runtime serving both listeners, **When** a delivery is sent to the operator API's
   address, **Then** the answer is not-found and nothing is recorded as a delivery.
3. **Given** a configuration placing both listeners on the same port, **When** the runtime starts,
   **Then** it refuses and names both.
4. **Given** a delivery whose signature is absent, malformed, of the wrong length, present twice, or
   computed under another source's secret, **When** it arrives, **Then** it is refused, no run
   starts, and its body is not stored.
5. **Given** a delivery to a source name the deployment does not configure, and another to a
   configured source with a wrong signature, **When** both are answered, **Then** the two answers
   are identical.
6. **Given** a flood of unauthenticated requests, **When** they are refused, **Then** the refusal
   record grows with elapsed time and not with the number of requests.

---

### User Story 3 - A payload cannot steer the run (Priority: P1)

An operator binds a playbook to a source whose sender they do not fully control. The delivery can
supply values the playbook declared it wants, each held to a declared shape. It cannot choose which
playbook runs, cannot reach the prompt as instructions, cannot choose where the report goes, and
nothing it carries beyond the declared values reaches the run.

**Why this priority**: P1 for the same reason as User Story 2. A delivery is written by whoever holds
the source's secret, and a secret held by a third-party service is held by that service's bugs too.
The worst a delivery can do has to be stated and bounded before any is accepted.

**Independent Test**: load a corpus of webhook playbooks that reference the payload where they may
not, and confirm each is refused at load with the field named. Then send deliveries whose declared
values violate their shape, and one carrying a sentinel in an undeclared field, and confirm the
first produce no run and the sentinel appears nowhere a run can see.

**Acceptance Scenarios**:

1. **Given** a webhook playbook whose prompt references a payload value, **When** it loads, **Then**
   it is refused, naming the prompt and the reference.
2. **Given** a webhook playbook whose sink configuration references a payload value, **When** it
   loads, **Then** it is refused, naming the sink field.
3. **Given** a playbook bound to a source the deployment does not configure, **When** it loads,
   **Then** it is refused, naming the source and the sources that are configured.
4. **Given** a delivery in which a declared value is absent, not a single value, longer than
   declared, or not wholly matching its declared pattern, **When** it is handed to that playbook,
   **Then** that playbook does not run, the refusal names the playbook and the value, and other
   playbooks bound to the source are unaffected.
5. **Given** a delivery carrying content in fields no playbook declared, **When** its runs execute,
   **Then** that content is absent from every working directory, every gather step's environment,
   every prompt and every sink delivery, and present in the delivery's own record.
6. **Given** a declared value beginning with a dash, **When** it would be bound into a gather step,
   **Then** that playbook does not run and the refusal names the value.
7. **Given** a delivery whose body names a playbook other than the ones bound to its source, **When**
   it is accepted, **Then** only the bound playbooks run.
8. **Given** a webhook playbook, **When** an operator invokes it by hand with values, **Then** the
   values are held to the same declared shapes as a delivery's.

---

### User Story 4 - A hostile or broken sender cannot exhaust the ingress (Priority: P2)

A sender sends a body larger than any legitimate one, opens a connection and never finishes its
headers, trickles a body a byte at a time, or opens many deliveries at once. Each is refused or cut
off within a declared bound, and the deployment keeps answering well-formed deliveries.

**Why this priority**: it bounds what an exposed listener costs. It is below the first three because
none of these reach a run or the record; they reach memory, connections and time.

**Independent Test**: against the ingress on loopback, send each misbehaving request with the
bounds set low, and confirm each is ended within its bound while a correctly signed delivery sent
alongside is still accepted.

**Acceptance Scenarios**:

1. **Given** a delivery declaring a body larger than the bound, **When** it arrives, **Then** it is
   refused before its body is read.
2. **Given** a delivery whose body exceeds the bound without declaring a length, **When** it
   arrives, **Then** it is refused having read no more than the bound.
3. **Given** a sender that stops sending headers or body, **When** the corresponding bound passes,
   **Then** its connection is closed and nothing is recorded as a delivery.
4. **Given** the ingress already holding its bound of deliveries in progress, **When** one more
   arrives, **Then** it is refused at once rather than queued.

---

### Edge Cases

- **A sender that re-serialises its body on retry.** Reordered keys or changed whitespace change the
  body's digest, so the retry is a new delivery under the default identity. A source whose sender
  does this, or stamps a send time into the body, declares where in the body its identity is
  instead (FR-316).
- **A sender that re-sends an unchanged event on purpose.** An alerting system re-notifying a
  continuously firing alert sends the same body again. Inside the replay window it is a repeat and
  does not run; outside it, it runs. The window is therefore a statement of how long a retry is
  expected to take, not of how often an event may recur — the guard's rate limit is the bound on
  recurrence, and a window stretched to do that job would suppress exactly the re-notification an
  operator needs to see.
- **Two hosts behind one ingress address.** Repeat detection reads the host's own record, so a retry
  that lands on the other host runs again. The runtime states that reach rather than implying a wider
  one (FR-319); the guard's claim still keeps the two runs from overlapping.
- **The host clock jumps.** The window is judged on the host's wall clock, as the constitution
  requires. A jump backwards lengthens the memory by the size of the jump; a jump forwards shortens
  it. A monotonic clock would not survive the restart that FR-319 exists for.
- **A source configured with no playbook bound to it.** Its deliveries are authenticated and
  recorded, and hand off to nothing. The record says so rather than the delivery vanishing.
- **A secret rotated at the source before the deployment.** Deliveries signed with the new secret
  are refused as unauthenticated and counted until the deployment is updated; a sender that retries
  delivers them once it is.
- **A body that is valid JSON with no object at its root.** Declared locations that do not exist in
  it are absent values, refused under FR-322, not a parse failure.
- **A delivery accepted while the playbook is already running.** It is a trigger like any other: the
  guard makes it wait or refuses it, and the sender's answer does not change, because the answer
  reports the record and not the run.

## Requirements *(mandatory)*

### Functional Requirements

#### Two listeners

- **FR-301**: The webhook ingress MUST be served on a listener of its own, bound to an address
  configured for it alone, separate from the operator API's listener.
- **FR-302**: The ingress MUST serve deliveries and nothing else. Any other method or path MUST be
  answered not-found, with nothing from the record in the answer. The operator API MUST NOT serve
  the delivery route.
- **FR-303**: The operator API MUST keep the runtime core's bind rule unchanged: loopback by
  default, and any other address only once a credential is configured. No webhook setting may widen
  it.
- **FR-304**: The runtime MUST refuse to start when the ingress and the operator API are configured
  on the same port, whatever their hosts, naming both addresses. Two listeners that could be one
  socket are not two listeners.
- **FR-305**: The runtime MUST NOT listen for deliveries when no ingress address is configured, and
  MUST refuse to start when a loaded playbook declares a webhook trigger and none is.

#### Sources and authentication

- **FR-306**: A source MUST be declared in the deployment's configuration and never in a playbook:
  its name, its secret, and optionally where its deliveries carry their identity (FR-316) and its
  replay window (FR-317). The secret is a secret value, redacted in every record, log line and
  listing, and an operator MUST be able to set it without it appearing on a command line — the
  constitution forbids a secret there, and setting a configuration value today takes it as an
  argument.
- **FR-307**: Every delivery MUST carry an HMAC-SHA256 signature computed over the exact bytes of
  its body under its source's secret, and the runtime MUST verify it before parsing the body,
  before storing any of it, and before handing it anywhere.
- **FR-308**: The signature MUST be compared in constant time over the full MAC. A signature that is
  absent, empty, not decodable, of the wrong length, or carried more than once MUST be refused.
- **FR-309**: A delivery naming a source the deployment does not configure and a delivery failing
  its signature MUST receive identical answers, so the ingress does not tell a stranger which source
  names exist. Reaching the answer MUST take the same work in both: a delivery to an unconfigured
  source is verified under a key the runtime holds for the purpose, rather than answered before any
  MAC is computed, which would separate the two by time where the answers do not.
- **FR-310**: The runtime MUST refuse a playbook whose webhook trigger names a source the deployment
  does not configure, when it loads, on the same terms as it refuses an MCP server the deployment
  does not provide.
- **FR-311**: Operators MUST be able to list the configured sources, with their secrets redacted,
  through the operator's command surface.

#### The answer and what it promises

- **FR-312**: The ingress MUST answer that a delivery was accepted only after the delivery — its
  source, its identity, its body and the time it was received on the runtime's clock — is recorded
  durably enough to survive the process being killed the moment the answer leaves. Accepted means
  recorded, not run: the guard decides whether it runs, and that decision comes later.
- **FR-313**: The durable step MUST be bounded in time, below the listener's own limit on writing
  the answer. Exceeding the bound MUST produce an answer saying the delivery was not accepted, so
  the sender retries — never an acceptance, and never silence while the handler believes it
  answered.
- **FR-314**: A delivery that is durably recorded MUST be handed to each playbook bound to its
  source exactly once by the process that recorded it, whether or not its answer reached the
  sender. The hand-off follows the record, not the answer: a write that lands after its answer said
  "not accepted" still produces its runs, and the sender's retry is then a repeat.
- **FR-315**: A delivery recorded and not yet handed off when its process stops MUST NOT run when
  the runtime starts again, and MUST be marked dropped in its record. A later delivery of the same
  identity inside the window MUST then be handed off — once, under FR-318 — rather than treated as a
  repeat, because nothing ran from the first.

#### Identity and repeats

- **FR-316**: A delivery's identity MUST be its source together with the SHA-256 digest of its exact
  body bytes, unless the source declares a location in the body that carries its identity, in which
  case the value at that location replaces the digest. A delivery whose declared identity location is
  absent or does not hold a single value MUST be refused.
- **FR-317**: A delivery whose identity was accepted within its source's replay window — measured on
  the runtime's clock from the acceptance — MUST be answered as accepted, MUST NOT be handed off
  unless FR-315 marked the accepted one dropped, and MUST be counted on the accepted delivery's
  record instead of adding one of its own. After the window the same identity is a new delivery.
- **FR-318**: Deciding that an identity is new and recording it MUST be one atomic step across every
  process sharing the state directory, so that deliveries of one identity arriving together produce
  exactly one hand-off.
- **FR-319**: The memory of accepted identities MUST be durable and survive a restart. Its reach is
  the host's own record, and the runtime's status output MUST say so, rather than leave an operator
  with two hosts to infer it.
- **FR-320**: Every time the ingress records or compares MUST be the runtime's own wall-clock time.
  No timestamp carried in a header or in the body may enter any comparison, the replay window
  included.

#### What a payload may reach

- **FR-321**: The playbooks a delivery triggers MUST be exactly the loaded playbooks bound to its
  source. Nothing in its body, its headers or its query may select among them.
- **FR-322**: A webhook trigger MUST declare every payload value it uses: where in the body it is
  found, a pattern the whole value must match, and a maximum length in characters rather than bytes,
  so a limit an author reads as characters is the limit enforced. The runtime MUST refuse, at load,
  a declaration missing any of the three or whose pattern does not compile. A delivery whose declared
  value is absent, is not a single value, exceeds its length, or does not wholly match its pattern
  MUST NOT run that playbook, and the refusal MUST name the playbook and the value.
- **FR-323**: Content the playbook did not declare MUST NOT reach any gather step, prompt, sink or
  file in the run's working directory. It is kept in the delivery's own record.
- **FR-324**: The prompt of a playbook with a webhook trigger MUST NOT reference a payload value,
  and the runtime MUST refuse one that does, at load. Declared values reach the agent only as a data
  file written into the run's working directory and recorded as a gathered input.
- **FR-325**: No sink configuration of a playbook with a webhook trigger may reference a payload
  value. The runtime MUST refuse one at load, and the sink MUST refuse it again when it is built. A
  sink field is a destination, a credential or the bucket a cap counts, and a sender choosing any of
  the three chooses where the report goes or how much it may create.
- **FR-326**: A declared value that would be bound into a gather step MUST be refused when it begins
  with a dash. It already arrives as a single word; a leading dash is how a single word becomes an
  option of the command it is handed to.
- **FR-327**: An operator invoking a webhook playbook by hand MUST have the supplied values held to
  the same declarations as a delivery's, so the manual path is not a way around them.
- **FR-328**: Each hand-off MUST be a trigger to the guard stage and subject to everything the guard
  applies to any trigger. The webhook adds no mechanism for concurrency, rate or waiting of its own.
  A guard refusal of a webhook trigger MUST name the delivery it came from.

#### Bounds on the ingress

- **FR-329**: The ingress MUST bound the size of a body and of the headers. A body declaring a length
  over the bound MUST be refused before any of it is read; one exceeding it without declaring a
  length MUST be refused having read no more than the bound.
- **FR-330**: The ingress MUST bound the time to receive a delivery's headers and its body. A sender
  exceeding either MUST have its connection closed, and nothing MUST be recorded as a delivery.
- **FR-331**: The ingress MUST bound the number of deliveries in progress at once, and MUST refuse
  one beyond the bound immediately rather than queue it.

#### What is recorded

- **FR-332**: A refused delivery that passed its signature MUST be recorded individually: its source,
  the reason, the value concerned where there is one, the time of receipt, the peer address, and its
  body. A repeat is not a refusal and is counted under FR-317.
- **FR-333**: A delivery refused before its signature passed MUST NOT have its body or any header
  value stored. It MUST be recorded as a count per reason and per configured source — every name the
  deployment does not configure sharing one count — over a fixed interval, with the last peer address
  seen, so that what it adds to the record follows elapsed time rather than the number of requests.
- **FR-334**: The peer address recorded MUST be the connection's own. A header claiming to forward
  another address MUST NOT replace it.
- **FR-335**: Deliveries and their refusals MUST be readable through the operator's command surface,
  and through no surface the ingress serves.
- **FR-336**: A run started from a delivery MUST record that it was webhook-triggered and which
  delivery it came from. Replaying it MUST reuse the recorded inputs, the data file of FR-324
  included, and MUST NOT fire a trigger.

### Key Entities

- **Source**: a sender the deployment has agreed to hear from. Held in the deployment's
  configuration: a name playbooks bind to, a secret, where its deliveries carry their identity if
  not in the whole body, and its replay window.
- **Delivery**: one authenticated request to the ingress. Its source, its identity, its body, when
  it was received on the runtime's clock, the peer address, how many repeats of it arrived inside
  the window, and whether it was handed off, dropped, or bound to nothing.
- **Webhook trigger**: the part of a playbook naming the source it is bound to and the payload
  values it declares, each with a location, a pattern and a length.
- **Delivery refusal**: what an operator reads to learn why a request did not become a delivery, or
  a delivery did not become a trigger. Individual when the request was authenticated; a count when it
  was not.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-301**: A correctly signed delivery sent to the built executable's ingress produces exactly one
  run, recorded as webhook-triggered and linked to its delivery — FR-301, FR-312, FR-336. Driven
  through the executable rather than the packages, so it fails if the ingress is wired in a package
  test and never in the binary, if the run is not linked, or if it happens twice.
- **SC-302**: While the record store's write is held, no acceptance is answered; when the hold
  exceeds the durable step's bound, the answer says not accepted, within that bound — FR-312,
  FR-313. Measured against a store that holds, because against one that answers promptly an
  implementation answering before it writes passes too.
- **SC-303**: A write that lands after its "not accepted" answer produces exactly one run, and the
  sender's retry is answered as accepted and produces none — FR-314. Fails against an implementation
  that hands off from the answer path, which produces zero runs here.
- **SC-304**: A process killed between recording a delivery and handing it off runs nothing on
  restart, the delivery reads as dropped through the operator's surface, and a retry of it then
  produces one run — FR-315. The process is killed rather than stopped, because a record written on
  the way out passes a graceful stop.
- **SC-305**: The same body sent twice inside the window produces one run and a repeat count of one;
  sent again one second past the window on an injected clock, it produces a second run; a body
  carrying a timestamp that would place it outside the window is still judged on the runtime's clock
  — FR-316, FR-317, FR-320. The timestamp in the body is what makes a payload-anchored
  implementation fail here.
- **SC-306**: With the interleaving forced — a second acceptance of one identity issued while the
  first holds its transaction open, from a second process on the same state directory — exactly one
  hand-off results — FR-318. Forced rather than raced: a check-then-write implementation passes a
  race it happens to win.
- **SC-307**: A delivery accepted before a restart is a repeat after it, and the status output names
  the single-host reach of repeat detection — FR-319.
- **SC-308**: A source whose declared identity location holds a per-attempt value ignores it: two
  deliveries differing only outside the identity location produce one run, and a delivery missing
  the location is refused — FR-316.
- **SC-309**: Requesting a run's record, or any path other than the delivery route, on the ingress
  answers not-found with no record content; the delivery route on the operator API answers not-found
  and records nothing — FR-302. Fails against a single listener serving both route sets, the
  arrangement the owner's decision refuses.
- **SC-310**: The operator API still refuses a non-loopback address with no credential when a source
  and an ingress address are configured, and the runtime refuses to start with both listeners on one
  port under two different hosts — FR-303, FR-304.
- **SC-311**: With no ingress address, nothing listens beyond the operator API; with a webhook
  playbook and no ingress address, startup is refused — FR-305.
- **SC-312**: A corpus of signatures — absent, empty, not hexadecimal, one byte short, carried twice,
  computed over a different body, computed under another source's secret — is refused in full, and a
  correct one is accepted — FR-307, FR-308. Each refusal case fails the suite when its check is
  removed; a corpus probed only on the accepted case is the untested guard Principle I describes.
- **SC-313**: The signature comparison is shown by a test that inspects the verifying code to go
  through the standard library's constant-time comparison, and that fails when it is replaced by an
  ordinary one — FR-308. Timing is not measured: a timing assertion on a shared runner is a flake by
  construction, which Principle VI refuses.
- **SC-314**: The answer to an unknown source and the answer to a wrong signature are byte-for-byte
  identical, and a verifier that records its calls is invoked once for each — FR-309. Fails against an
  implementation that answers an unknown source without verifying, whose answer can still be
  identical. Time is not measured, for SC-313's reason.
- **SC-315**: A corpus of webhook playbooks — prompt referencing a payload value, sink field
  referencing one, unconfigured source, a gather step referencing a value the trigger does not
  declare, a declared value without a pattern, without a length, or with a pattern that does not
  compile — is refused at load with the field named, and the sink refuses a
  payload reference when built directly — FR-310, FR-322, FR-324, FR-325. Each case fails the suite
  when its check is removed.
- **SC-316**: A listing of sources shows every configured name and no secret; the suite's secret
  sentinel, set as a source secret through standard input by the built executable, appears nowhere in
  the suite's output — FR-306, FR-311. Fails if the secret can only be set as an argument, and
  against a listing that prints it.
- **SC-317**: Deliveries whose declared value is absent, an object, one character over its length,
  or matching its pattern only in part produce no run of that playbook and a refusal naming it and
  the value, while a second playbook bound to the same source runs, and a value exactly its length in
  multi-byte characters runs — FR-322. The partial match is the case an unanchored pattern passes;
  the multi-byte value is the case a length counted in bytes refuses.
- **SC-318**: A sentinel carried in an undeclared field of a delivery is found in the delivery's
  record and nowhere in the run's working directory, gather environments, prompt or sink deliveries —
  FR-323, FR-324. Fails against an implementation writing the whole body into the working directory.
- **SC-319**: A declared value beginning with a dash, bound into a gather step, produces no run and a
  refusal naming it — FR-326.
- **SC-320**: A delivery whose body names another loaded playbook runs only the playbooks bound to
  its source — FR-321.
- **SC-321**: A manual invocation of a webhook playbook with a value its pattern refuses is refused
  with the same message a delivery would produce — FR-327.
- **SC-322**: A delivery arriving while its playbook runs waits under the guard rather than starting
  a second run, and a delivery the guard refuses leaves a guard refusal naming it — FR-328.
- **SC-323**: A body declaring a length over the bound is answered before the sender sends any of it;
  a body over the bound sent without a length is answered having read no more than the bound plus
  one byte; headers over their bound are refused — FR-329. Measured by the test's own count of bytes
  consumed, not by the status alone.
- **SC-324**: A sender that stalls in its headers, and one that stalls in its body, each have their
  connection closed within the bound, with no delivery recorded — FR-330. Measured against senders
  built to stall, because a prompt sender satisfies the criterion with no bound enforced at all.
- **SC-325**: With the in-progress bound held full by deliveries that stall, one more is refused
  without waiting, and a correctly signed delivery is accepted once one of them ends — FR-331.
- **SC-326**: A refused authenticated delivery is readable through the operator's surface with its
  source, reason, value, receipt time and peer address; ten thousand unauthenticated requests leave
  the number of refusal records unchanged within one counting interval, and no request body from
  them is found anywhere under the state directory — FR-332, FR-333.
- **SC-327**: A delivery carrying a forwarded-address header naming 192.0.2.7 is recorded with the
  loopback peer address it actually came from — FR-334.
- **SC-328**: Deliveries and refusals are listed by the operator's commands, and none of their
  content is served on the ingress — FR-335.
- **SC-329**: A webhook-triggered run replays to completion without firing a trigger, with the data
  file present in the replay's inputs — FR-336.
- **SC-330**: Each criterion above has at least one test the mutation harness shows failing when the
  behaviour it asserts is removed. The refusals here are all of the kind that look satisfied when
  nothing happens — no run, no stored body, not found — and an assertion of an absence passes
  against an ingress that never started. Each such test also asserts the positive case beside it,
  in the same run, so a dead listener fails it.

## Assumptions

- **The guard is implemented before this feature.** Specified independently, but built after: the
  guard's waiting slot is what keeps a delivery that collides with a run from being lost, and its
  rate limit is what bounds a sender that fires too often. The guard specification already
  anticipates this — its waiting trigger and its rate limit are both written for the webhook.
- **Deduplication is here, and nowhere else.** The guard left it for the first trigger that carries
  an identity. The identity is the delivery, not the event it describes: FR-317 stops a sender's
  retry from running twice, and the guard's rate limit, keyed on the playbook, is what bounds an
  event that recurs.
- **A sender that cannot sign cannot deliver directly.** This follows from the owner's decision and
  is stated so it is not discovered: a sender able only to present a static token needs something in
  front of the ingress that verifies it and signs.
- **Transport security is the deployment's.** The ingress speaks plain HTTP; a deployment that
  exposes it terminates TLS in front of it. The signature proves who wrote a body, not that nobody
  else read it. Behind such a proxy the recorded peer address is the proxy's, by FR-334.
- **Sources and bindings are read at startup**, as playbooks are. Changing either takes a restart.
- **Secret rotation with two secrets live at once is out of scope.** A rotation refuses deliveries
  in the interval between the two sides changing, and FR-333 counts them.
- **Filtering deliveries by content is out of scope.** Every playbook bound to a source is handed
  every delivery; one that should act on some of them decides so itself, in its gather steps or its
  report.
