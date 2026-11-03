# Operating fbhook

## Provisioned installation

The initial installation uses Fly app `darron-fbhook` in `yyz` (Toronto),
with private Tigris bucket `darron-fbhook`. Its canonical origin is
`https://darron-fbhook.fly.dev`, and the initial administrator is `darron`.
The GitHub OAuth application is named `fbhook`, with callback
`https://darron-fbhook.fly.dev/auth/callback`.

Storage credentials and encryption/session keys are stored as Fly secrets.
The live Tigris storage contract passed on 2026-11-02. Its disposable verification
objects remain under `fbhook-contract/1793665436737877000/`.

Initial deployment verification on 2026-11-02 confirmed one started Machine
(`17811100df6389`) in `yyz`, a passing Fly readiness check, public health/readiness
responses, rejection of unauthenticated admin requests, successful GitHub sign-in
as `darron`, and completed worker scans without an error. The initial image is
`registry.fly.io/darron-fbhook:bootstrap-20261102`, digest
`sha256:c8a0d019ad410848179fcd61207438daadc66bed0f70a8d1020e59360503fefb`.
It was built from the local development tree; the running binary reports
`fbhook 0.1.0 sha=unknown dirty`.

Live issue delivery was subsequently verified on 2026-11-02 for both destinations:

- GitHub destination `fbhook repository` targets `darron/fbhook`; its integration
  test created [issue #1](https://github.com/darron/fbhook/issues/1).
- Linear destination `fbhook feedback` targets team `fbhook` (`FBH`), UUID
  `59d2dfb9-d9ba-4aba-8774-fc5d7a0d02f8`; its integration test created
  [FBH-1](https://linear.app/froese/issue/FBH-1/fbhook-end-to-end-linear-delivery-test).

Both tests used the Go CLI through separate generated feedback URLs, repeated
the same payload/key and received the same receipt, and confirmed delivery in
one worker attempt. Source data, nested JSON and `9007199254740993` were preserved
in the created issues. Changed payloads with the same key returned 409; malformed,
missing-feedback and oversized requests returned 400, 400 and 413 respectively.
The URLs are credentials and are deliberately excluded from this repository.
The GitHub destination token expires on 2026-12-02; replace it through destination
editing before expiry. Credential rotation does not require changing feedback URLs.

Deploy this installation directly from the repository root:

```sh
fly deploy --ha=false
```

Keep one Machine in one region. Configure the first GitHub or Linear destination
and feedback URL in the private admin interface after signing in.

## Service configuration

Create a private Tigris bucket through your existing Fly.io account. Use a bucket
and access credentials dedicated to fbhook. Nothing in this repository creates a
bucket or application automatically.

| Environment variable | Value |
| --- | --- |
| `AWS_ENDPOINT_URL_S3` | `https://t3.storage.dev` (default) |
| `AWS_ACCESS_KEY_ID` | Tigris access key with GetObject, PutObject and ListBucket |
| `AWS_SECRET_ACCESS_KEY` | Tigris secret |
| `AWS_REGION` | `auto` (default) |
| `BUCKET_NAME` | Bucket name supplied by Fly/Tigris |
| `FBHOOK_STORAGE_PREFIX` | Optional dedicated object prefix (default empty) |
| `FBHOOK_BASE_URL` | Canonical HTTPS origin, e.g. `https://feedback.example.com` |
| `GITHUB_CLIENT_ID` | GitHub OAuth app client ID |
| `GITHUB_CLIENT_SECRET` | GitHub OAuth app secret |
| `FBHOOK_GITHUB_USERS` | Comma-separated initial/configured authorized usernames |
| `FBHOOK_ENCRYPTION_KEY` | Base64 of 32 random bytes from `fbhook keygen` |
| `FBHOOK_SESSION_KEY` | Base64 of 64 random bytes from `fbhook keygen` |
| `FBHOOK_POLL_INTERVAL` | Optional worker scan interval, default `15s`, range `1s`–`1h` |

The service adapter uses static Tigris credentials, not an ambient AWS credential
chain. Confirm variable names against `storage/tigris/tigris.go` when adapting another host.

Register a GitHub OAuth app with homepage `FBHOOK_BASE_URL` and callback
`FBHOOK_BASE_URL/auth/callback`. Login requests `read:user`; it does not grant
issue-writing access. At first login, the configured username is resolved using
the authenticated GitHub API and permanently bound to its numeric GitHub user ID.
A reassigned username cannot inherit the existing binding. Removing a configured
name revokes its bound ID on the next authenticated request. A renamed account
retains access through its existing ID while the original config entry remains.
To grant the newly named account explicitly, add the new username and let it bind
before removing its old entry. Never silently edit an existing binding to another
account; audit and explicitly remove an obsolete object binding during recovery.

Run behind HTTPS. The server origin, Go sender and Tigris adapter reject remote
HTTP URLs; HTTP is accepted only for `localhost`, `127.0.0.1` or `::1` development.
Cookies
are HttpOnly, encrypted/signed, SameSite=Lax, and Secure outside local HTTP. Sessions
expire after eight hours. Mutations require CSRF; private assets and every admin
API route require authorization. There is no public submission read/status API.

`fbhook serve --listen :8080` runs HTTP and one worker. `/healthz` checks the process;
`/readyz` checks storage listing, not successful provider delivery. Concurrent
readiness checks share one probe, with both success and failure cached for five
seconds; a caller disconnect does not cancel the shared probe. Worker scan
status and failures are shown privately. Logs intentionally omit request URLs,
feedback bodies, credentials and upstream response bodies. Configure ingress and
observability to redact the secret path and request bodies too.

Intake allows 120 authenticated requests per endpoint and 600 globally per minute.
Malformed or invalid credentials do not spend these quotas, and per-endpoint
denials do not spend another endpoint's global capacity. Authentication has a
separate cap of 16 concurrent storage lookups and never reads a request body.
Only authenticated, admitted requests read bodies: at most four per endpoint and
16 globally can read bodies or persist submissions concurrently; excess work
receives `429`. A stalled body on one credential cannot fill every intake slot.
No forwarded client-IP headers are trusted. Put abuse controls at a trusted ingress
if you need source-IP filtering or a per-source request budget: guessed credentials
with a valid shape can still cause bounded concurrent authentication reads.

Submission lists and page bootstrap return summaries, with application names
limited to 200 characters. Full feedback, context and delivery history are fetched
only when opening a submission. The stored payload is unchanged.

## Destination setup

Sign in, create a destination, and supply an issue-writing credential. Credentials
are encrypted with AES-256-GCM using `FBHOOK_ENCRYPTION_KEY`; they are never returned
by the admin API. Keep this key backed up separately: losing it prevents decrypting
the stored credentials. Rotating the session key invalidates sessions; changing
the encryption key requires an explicit credential migration/re-entry procedure.

GitHub: fine-grained PAT scoped to the selected repository, Issues read/write and
repository metadata read. Use `owner/repository` and existing label names. Issues
must be enabled. Configured labels require repository push permission; creation
and reconciliation verify they were applied. If GitHub creates an issue but drops
a label, its reference is retained as a failed delivery and retry is blocked.
Correct its labels upstream, then verify and link the existing issue. Token rotation
uses the same destination/credential identity.

Linear: personal API key with permission to read the selected team, labels and
project and to create issues. Use the team's UUID; labels and optional project are
UUIDs, not display names. The adapter validates their team relationship before
saving. Provider credentials are independent of GitHub login credentials.

Create an endpoint and copy the one-time full URL into your application's backend
secret store. Each endpoint routes to one destination. Applications sharing a URL
identify themselves through `source.app`; these fields are caller-supplied context,
not verified identities. Labels belong to the destination configuration. Pausing
an endpoint stops new intake; accepted work keeps its captured routing. Rotation
revokes the old URL immediately, including old-key idempotent retries.

Destination updates create a revision under `destination-revisions/`. Legacy
revisions under `destinations/<id>/revisions/` remain readable as stored history
and are skipped when filling destination list pages; no migration is required.
Accepted work captures its destination,
labels and rendered issue; later routing edits do not move it. Credential rotation
is deliberately read at delivery time, so queued work can use a replacement token.
Disabling a credential blocks delivery and makes affected pending work failed;
re-enable then retry those records in the admin. Missing or unreadable credentials
also fail delivery. Transient credential-storage errors leave work pending and
report an incomplete worker scan, so the next pass can recover automatically.
Configuration writes span separate
objects; a failed save can leave an unused revision or a rotated credential. Inspect
and retry configuration after an error rather than assuming a multi-object transaction.

## Durable delivery and recovery

Successful intake creates the complete submission, captured routing and pending
work in one conditional object write. An idempotency-key digest determines the
record key; concurrent submissions with identical bytes resolve to one receipt.

The worker writes `delivering` before calling the provider. Definite rate-limit
failures retry with bounded exponential backoff and jitter, honoring provider delay
hints. Automatic attempts stop at ten. Timeouts, lost responses, malformed success
responses and ambiguous upstream errors become `delivery_uncertain`. An unfinished
`delivering` record becomes uncertain on the next serial scan, including after
restart. A failed success-state write never triggers another automatic creation.

There is no distributed exactly-once guarantee across object storage and external
issue APIs. An uncertain record is never automatically created again. The worker
can positively find its marker in an existing target issue; an absent search result
never justifies recreation. Reconciliation has a 30-second deadline and a persisted
exponential backoff with jitter (up to 15 minutes for the default local schedule),
honoring longer provider retry hints. A restart retains that schedule. Unknown
Linear GraphQL errors remain uncertain even when the mutation result is null:
that alone does not prove creation had no side effects.
In the private submission panel:

1. Link an existing issue by its provider ID; the server verifies target and marker.
2. Retry after checking upstream state. Uncertain retry requires an explicit
   duplicate-risk acknowledgement and a reason.
3. Abandon delivery with a reason. This retains feedback and history.

Actions record the administrator's stable GitHub ID. The latest 20 events plus a
total event count are retained. Manual retry resets the automatic-attempt budget
and keeps lifetime attempts. There is no automatic deletion/retention policy.
Back up the object prefix and encryption/session keys according to your needs.

Full scans are paginated and repeated, so inserts before a cursor are discovered
on a later pass. The queue has no non-durable index dependency. This trades read
cost/latency at large history sizes for a simple recoverable implementation.

## Tigris contract verification

Conditional create (`If-None-Match: *`), compare-and-swap (`If-Match` with an opaque
ETag), read-after-write and consistent listing are required. The AWS SDK's write
retries are disabled so ambiguous writes are resolved by the application. Use a Single-region or Multi-region bucket for globally strong preconditions, as
described in [Tigris conditional operations](https://www.tigrisdata.com/docs/objects/conditionals/).
Global and Dual-region buckets provide that guarantee only within a region.
Keep the application in one region; S3-compatible syntax alone does not prove
the required storage semantics.

The local test suite exercises the adapter's actual HTTP protocol and shared
storage contract with memory. To verify your real test bucket, inject its credentials
and explicitly run:

```sh
FBHOOK_TEST_TIGRIS=1 go test -run TestLiveContract -count=1 -v ./storage/tigris
```

This writes test objects in a random `fbhook-contract/<timestamp>/` prefix and
prints that prefix. It intentionally does not delete objects; inspect and remove
that test prefix through your normal storage tools. Never use a bucket where
these writes are unwanted. Repeat the storage and provider verification for a
different installation or provider account before relying on its delivery.

## Fly deployment

Use a dedicated Fly app. `fly.toml` configures the Dockerfile, region, graceful
shutdown, readiness check and native rolling deployment. It disables autostop and
autostart so the background worker stays running. Install secrets before the first
deployment and set the canonical HTTPS base URL and OAuth callback. The configured
region defaults to `yyz`; choose the bucket's suitable region.

Deploy directly with the Fly CLI from the repository root:

```sh
fly deploy --app YOUR_APP --ha=false
```

Alternatively, add your application name as the top-level `app` value in
`fly.toml`, then run `fly deploy --ha=false`. The flag avoids creating a spare
machine on the initial deployment. Keep this application scaled to **one machine
in one region**; if using an existing app, check `fly machine list --app YOUR_APP`
and reconcile its scale before deploying. `min_machines_running = 1` is a minimum,
not a replica-count limit.

The rolling strategy replaces the existing machine, with temporary downtime for
this single-worker service, and retains Fly's deployment health checks. Do not
switch this version to canary/bluegreen rollouts or add worker replicas: those
can introduce overlapping workers. See [Fly's deployment strategy configuration](https://docs.fly.io/reference/configuration#picking-a-deployment-strategy).
Inspect deployment problems using `fly status`, `fly checks list` and `fly logs`.
A plain local deployment leaves build identity metadata unknown; the GitHub
workflow supplies the checked source revision and clean build state.

The GitHub **Deploy to Fly** workflow is manual and restricted to `main`, uses the `production`
environment, serializes deployments for each app, checks source identity, and calls
`fly deploy` directly. Configure `FLY_API_TOKEN` as an app-scoped deploy token stored
only in the `production` environment. Restrict that environment's deployment
branches to `main` in GitHub settings; the workflow guard alone does not protect
against a writer who changes the workflow. No push-triggered live deployment,
Fly resource provisioning or custom deployment wrapper is used.

## CI, releases and Homebrew

CI uses immutable action SHAs, Go 1.27.3, a Go 1.26 minimum check, Linux/macOS,
race detection, vet, golangci-lint 2.15.0, govulncheck 1.9.0, bounded parser fuzzing,
frontend type checks/build and Chromium smoke tests. Browser fixtures use signed
test sessions and test-only providers; no production bypass exists.

For a stable `vMAJOR.MINOR.PATCH` release, update the root `Version` constant and
publish a GitHub release for that tag after CI passes. The release workflow resolves
an immutable source SHA, requires a clean matching checkout, tests it, builds
Darwin/Linux amd64/arm64 archives, embeds source/version metadata, generates four
individual SHA-256 sidecars and `SHA256SUMS`, and smoke tests native Linux/macOS.
Publication runs in a fresh job with Contents write permission and executes no
release binaries. Existing assets must be byte-identical; replacements are refused.
The manual backfill workflow accepts only published stable tags and runs from main.

Homebrew automation verifies published hashes and writes `Formula/fbhook.rb` in
`darron/homebrew-tap`. Set `HOMEBREW_TAP_TOKEN` to a fine-grained token with Contents
read/write on that tap only. A missing token fails that optional post-publication
job; release assets are already published. Existing identical formulas are no-ops;
downgrades are refused. The tap workflow can be rerun independently.

The npm dependency audit currently reports two low-severity, development-only
findings via the Vite Svelte plugin's `deepmerge` dependency with no fixed upstream
version. No npm packages execute in the Go runtime. Build only trusted frontend
source/configuration; re-run audits when updating dependencies. No project license
has been selected yet; choose one before publishing for third-party reuse.
