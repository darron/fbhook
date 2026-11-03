# Shared feedback service

Status: approved design; implementation and verification in this repository.
Date: 2026-11-02

## Summary

Build `fbhook`: one self-hosted service that accepts feedback and application-supplied
JSON through credential-bearing URLs, persists submissions in object storage, and
delivers issues to configured GitHub or Linear destinations in the background.
Applications own their feedback forms, API routes, and agent tool integrations.
Issue readers own triage, implementation, and any subsequent automation.

## Current state

At design time the repository had no application code or commits. The discussion established the
product and storage choices. The user requested the installed
`build-maintainable-go-tools` and `go-vite-svelte-builder` skills as engineering
guidance. Their package-first design and Go-owned routing apply here.

## Goals

- Integrate feedback once centrally instead of implementing tracker integrations
  separately in every application.
- Allow several applications to share one endpoint, with multiple endpoints when
  different routing, defaults, or credential lifecycles are useful.
- Provide reusable Go packages and a practical CLI for preparing and submitting
  feedback from applications and agents.
- Acknowledge accepted feedback only after durable storage; recover delivery work
  after process restarts.
- Keep persistence and issue providers behind separate Go interfaces.
- Provide a private, GitHub-authenticated administration interface.

## Non-goals and anti-goals

- No hosted multitenant accounts, billing, application registration requirement,
  or separate authentication ceremony for submission callers.
- No automatic coding, agent dispatch, prioritization, or issue lifecycle sync.
- No relational database, separate queue service, or required local durable disk.
- No required LLM calls or automatic rewriting of feedback.
- No form builder, hosted public forms, screenshots, or binary uploads in v1.
- Do not discard context silently or report issue creation when only intake has
  succeeded. Do not claim exactly-once external issue creation.

## Locked product decisions

These decisions came from the user:

- Self-hosted for the user's applications; a shared endpoint can receive feedback
  from many applications.
- Applications implement their own feedback mechanisms and send to generated URLs.
- Feedback plus arbitrary application JSON becomes an issue through a provider
  interface, initially targeting GitHub and Linear.
- Possession of a sufficiently random URL is the submission credential.
- Persist in object storage behind an interface. Tigris through Fly.io is the first
  adapter; additional storage adapters can follow.
- Deliver using a background worker.
- Private administration uses GitHub OAuth, with authorized GitHub access configured
  on the service and an initial username supplied in configuration.
- Go packages support both preparing and sending feedback.

## Approaches

SQLite plus a transactional outbox was initially proposed. The user selected
object storage to avoid a database dependency. Object storage is therefore the
system of record for both configuration and submissions.

For delivery, the alternatives were persist-then-create during the request or
persist-and-acknowledge followed by a background worker. The user selected the
worker, isolating intake from tracker latency and availability.

The initial storage adapter uses Tigris's S3 API through the maintained AWS Go SDK,
with Fly-provided endpoint and credentials. Native GCS and other provider adapters
are future work; compatibility is established through behavioral contract tests.

## Proposed implementation decisions

These are builder proposals, not additional requirements attributed to the user.
If the user approves this document, the behaviors in this section become the v1
build contract. Limits explicitly described as configurable are initial defaults;
their values can be tuned without changing the architecture. Changes to deployment
topology, recovery guarantees, credential modes, or uncertainty handling require a
design revision rather than an implementer silently substituting another approach.

### Operation and storage consistency

- Initially run one service process, including its worker, in one configured Fly
  region. Keep it running while deliveries are pending; do not rely on incoming
  HTTP requests to wake the worker. Multi-region active-active operation is outside
  v1. A deploy must drain the old worker before starting the replacement worker.
- Use private bucket objects and application-encrypted tracker credentials. The
  encryption key and OAuth/session secrets live in service configuration, outside
  the bucket. Credential fields are write-only in admin responses.
- Require coherent reads and conditional writes for authoritative objects within
  the selected deployment topology. Pin storage operations to the documented
  consistency boundary where required. Verify Tigris behavior before claiming live
  readiness; its global placement is not assumed to give universal linearizability.
- Use schema-versioned JSON documents. Replacements use version preconditions;
  mismatches are conflicts, never unconditional last-writer-wins updates.
- Paginated listing must eventually enumerate every surviving object in the
  prefix. A listing pass is not assumed to be an atomic snapshot.

### Package boundaries

- Public submission types, validation/preparation helpers, and HTTP client have no
  cloud SDK, storage, OAuth, or UI dependency in their compiled import paths.
- A service package owns endpoint validation, acceptance, routing, and delivery.
- A storage interface exposes read-with-version, create-if-absent,
  replace-if-version, and paginated prefix listing. Provider versions are opaque;
  never interpret an ETag as a content hash. Conditional deletion can be added
  when a concrete lifecycle feature requires it.
- `storage/tigris` implements that interface with the AWS SDK and configured S3
  endpoint. It maps not-found and precondition failures to typed core errors.
- Issue-provider packages accept a prepared issue and return a provider ID and
  URL. They classify definite rejection versus uncertain creation. Reconciliation
  capabilities are explicit rather than inferred from a generic create error.
- CLI, HTTP handlers, admin APIs, and worker use the same service logic.
- Callers own output, process lifetime, contexts, HTTP clients, and shutdown.

The implementer may choose exact package names and exported signatures while
preserving these boundaries.

### Stored objects

```text
config/admin-identities.json
endpoints/<endpoint-id>.json
destinations/<destination-id>.json
submissions/<endpoint-id>/<submission-id>.json
```

- Endpoints contain name, enabled state, secret digest, destination ID, and issue
  defaults. Intake resolves the destination's current revision and captures that
  immutable revision plus effective defaults in the new submission.
- Destination revisions are immutable documents under
  `destinations/<destination-id>/revisions/<revision-id>.json`. A mutable destination
  head selects the current revision for future submissions through its endpoints.
  Revisions contain provider type, repository/team/project, defaults, and an
  encrypted credential reference. Credential rotation can update the referenced
  credential without changing the captured issue target.
- Accepted work reads the currently configured credential at delivery time through
  that stable credential reference; it never captures an obsolete secret version.
  v1 supports rotation and disabling, not physical deletion of referenced
  destination revisions or credentials. Missing, disabled, or unreadable credentials
  stop delivery with a visible configuration failure; they never fall back to a
  different identity or target. After configuration repair, an admin can retry.
- Credential records use `credentials/<credential-id>.json`, with authenticated
  encryption and versioned updates. Avoid copying secrets into submissions.
- Each submission is one authoritative document containing the received feedback,
  context, captured routing/defaults, rendered issue content, and delivery state.
  Initial creation atomically records both accepted content and pending work.
- No queue marker or index object is required for correctness. Optional indexes
  are reconstructible optimizations and cannot be the only recovery path.
- Retain submissions and delivery state in v1; no automatic expiry. Any future
  retention policy must explicitly define the idempotency lifetime it shortens.

### Submission contract

Proposed route: `POST /api/v1/feedback/<endpoint-id>.<secret>`.

```json
{
  "title": "Search loses its filters",
  "feedback": "Returning from a record resets the date range.",
  "source": {
    "app": "archive",
    "version": "v0.4.2",
    "environment": "production",
    "actor": "agent"
  },
  "context": {
    "page": "/search",
    "request_id": "req_example",
    "filters": {"year": 2026}
  }
}
```

- `feedback` is required nonblank text. Title and source fields are optional.
  `context` accepts any valid JSON value. Source and context are caller-supplied
  claims; endpoint metadata and server receipt time are separately authoritative.
- Use a schema version for stored envelopes and a versioned external API. Preserve
  unknown context content. Reject unsupported envelope fields rather than silently
  dropping them. Initial proposed request limit is 64 KiB; provider-specific body
  limits also apply. Reject feedback that cannot be represented in the selected
  tracker without silent truncation.
- An omitted title is derived deterministically from the first nonblank feedback
  line, within provider limits. No model is involved.
- Generate 32 random bytes for each secret. Persist only its digest; show the full
  URL at creation/rotation. Compare digests safely. A successful rotation atomically
  replaces the digest and immediately rejects subsequent use of the old secret.
- Possession permits submissions only. Receipts, histories, and admin APIs require
  admin authentication. The intake response contains only submission ID and
  acceptance state, not private issue contents or destination credentials.
- Return `202` only after confirmed durable creation. On uncertain storage outcome,
  fail the request and let an idempotent retry resolve whether it was stored.
- Support `Idempotency-Key`, scoped to the endpoint. Derive the submission key from
  endpoint identity and the supplied key; create-if-absent stores content and
  delivery state together. An identical retry returns the existing receipt;
  different content for the same key returns `409`. Define equality as identical
  request JSON bytes; Go client/CLI serialize once and reuse those bytes on retries.
  Without a key, every request creates a fresh random submission ID.
- Endpoint revocation applies to future requests, including idempotent retries;
  accepted work remains deliverable. Destination failure must not lose intake.
- Rate-limit per endpoint and globally, bound request bodies and concurrency, and
  redact URL secrets from access logs, tracing, errors, and outbound metadata.
- Applications forwarding feedback keep the URL server-side. Deliberate direct
  browser use exposes the submission credential; CORS is not authentication.

### Worker and crash recovery

- Poll paginated submission prefixes fairly, resume in-memory cursors between
  batches, and periodically restart full scans. Startup scans all prefixes. Never
  stop recovery at a recent date cutoff or depend on an in-memory notification.
- The first implementation intentionally scans retained submissions; expose scan
  progress and pending age so growth is observable. Do not invent a distributed
  indexing subsystem before measured scale requires it.
- Use bounded worker concurrency and at most one local worker attempt per
  submission. Persist `delivering` by version-checked replacement before making
  the external create call. Concurrent admin mutations must use the same state
  transition rules and preconditions.
- States: `pending`, `delivering`, `retry_scheduled`, `delivered`, `failed`,
  `delivery_uncertain`. Store attempt count, attempt ID, timestamps, next retry,
  sanitized error class, and provider reference where known. Bound persisted error
  history to the latest 20 entries plus aggregate counts.
- Definite retryable rejections use bounded exponential backoff with jitter and
  provider retry hints. Proposed defaults: 10 attempts, 2-second initial delay,
  15-minute maximum delay. Configuration can override these limits.
- A timeout after possible transmission, a crash during creation, or failure to
  save a successful create response is uncertain, not a definite failed creation.
  Do not blindly create again. On restart, unfinished `delivering` states become
  uncertain. Search/reconcile using the submission marker where supported;
  otherwise surface for admin resolution. A negative search alone does not prove
  absence. Automatic provider-side deduplication is used only if verified.
- Every issue includes a stable submission marker. Confirmed existing issues can
  be linked as delivered; an admin may explicitly retry an uncertain submission,
  acknowledging potential duplication. No exactly-once claim.
- An admin resolves uncertainty in one of three ways: link a known issue after the
  provider verifies its identity, target, and submission marker; explicitly retry
  with a duplication acknowledgement; or abandon delivery as `failed` with an
  operator reason. There is no force-link bypass in v1. Persist the admin's GitHub
  account ID, action, reason, time, and previous/new state in the same version-checked
  submission update. Keep the latest 20 state transitions and a total count.
- Manual retry is allowed only from `failed` or `delivery_uncertain`, or can make a
  scheduled retry due immediately. It cannot race an active `delivering` attempt.
  Restart a bounded attempt budget for a manual retry while retaining total counts.
- On graceful shutdown stop claiming work, cancel/wait for bounded active attempts,
  and preserve enough state for recovery. Do not reclaim another process's active
  attempt: zero-overlap worker deployment is a v1 operating requirement.

### Issue output and destinations

- One accepted submission creates one issue unless it is a retry of the same
  idempotent submission. No semantic deduplication or append-to-existing workflow.
- A destination configures provider, GitHub repository or Linear team/project,
  credentials, and permitted issue defaults. Submitters cannot choose arbitrary
  destinations, credentials, labels, assignees, or issue states.
- v1 issue defaults support configured label IDs/names only: GitHub labels by name,
  Linear labels by ID. GitHub repository and Linear team/optional project are routing
  fields. Assignees, priority, milestone, and custom issue state are outside v1;
  use the provider's normal creation state. Validate target and labels when saving
  configuration. Later provider rejections become visible failures, without silently
  dropping invalid labels or changing the captured target. Ordinary destination
  edits affect future submissions only. v1 does not edit an accepted submission's
  target or labels: restore the required tracker configuration, or abandon delivery
  and submit fresh feedback through the corrected configuration.
- Render readable feedback first, source information second, JSON context and a
  stable submission marker afterward. Escape format delimiters correctly, retain
  raw accepted JSON in storage, and render admin content safely.
- Both GitHub and Linear creation adapters are in v1 scope. Prove one complete
  path with GitHub first, then exercise Linear through the same interface.
- Configure provider credentials explicitly in admin; GitHub OAuth login is not
  implicitly used as the issue-writing credential. Fine-grained GitHub tokens and
  Linear API keys are proposed initial modes. GitHub App installation support can
  follow without changing the submission client.

### Administration and authentication

- Configuration includes the GitHub OAuth client, callback/base URL, session key,
  application encryption key, and allowed GitHub usernames. Start with one
  configured username; use a list shape without building user management.
- Every authorized account is a full administrator with identical permissions.
  There are no roles or additional operator-only UI permissions in v1. Service
  configuration and encryption/session keys remain deployment-owned.
- Resolve configured usernames to GitHub account IDs with authenticated GitHub
  lookups before permitting login, record the binding conditionally, and thereafter
  compare immutable IDs. Resolve newly configured names; do not silently rebind an
  existing name to a different account. Fail closed on unresolved identity or ID
  conflicts. Explain intentional rebinding through an explicit operator procedure.
- A renamed account remains authorized while its binding is configured; removing
  its configured binding removes access on subsequent authenticated requests.
- Use established OAuth/session libraries where they avoid delicate handwritten
  security code; enforce OAuth state, appropriate PKCE support, secure HTTP-only
  SameSite cookies, bounded session lifetime, logout, and CSRF on cookie-authenticated
  mutations. Signed/encrypted cookie sessions avoid a session database.
- Go owns auth, routes, JSON APIs, HTML shells, typed bootstrap data, and embedded
  Vite assets. Svelte owns authenticated page UI and interactions. Login bootstrap
  stays server-rendered. No SPA router is required.
- Pages: endpoints; destinations; submission history/detail with delivery status;
  retry and uncertain-delivery resolution. Secrets are write-only and never
  included in bootstrap data. Pagination and bounded in-memory filtering suffice
  initially; do not promise arbitrary indexed search over object storage.

### Client and CLI

- Public client accepts a caller context, endpoint URL, injectable HTTP client,
  structured feedback, and arbitrary JSON context. Return typed receipts and errors.
- Preparation helpers merge explicit application defaults with per-submission
  context without mutating caller-owned values. No automatic environment/log dumps.
- CLI supports flags for common text/source fields and JSON on stdin, configurable
  endpoint via environment or protected profile, structured stdout, and useful
  errors on stderr. Avoid putting secret URLs in example shell command arguments.
- Client retries require one stable idempotency key and identical serialized body;
  never retry a POST with a fresh key. Receipt means accepted, not delivered.
- Provide copyable instructions for application-owned agent tools and direct CLI
  submission. A separately hosted MCP server is not required in v1.

## Configuration and deployment

- Consume `AWS_ENDPOINT_URL_S3`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and
  `BUCKET_NAME` supplied by Fly's Tigris integration, with explicit region support
  (documented Tigris default `auto`). Do not hardcode an endpoint that overrides
  the operator's configured value.
- Produce one Go executable with embedded production frontend assets. Provide
  container/Fly configuration and shutdown guidance, without provisioning or
  deploying infrastructure as part of design approval.
- Health/liveness and readiness are separate from private operational status.
  Surface storage readiness, worker progress, backlog age, failures, and uncertain
  deliveries without exposing payloads, tokens, or high-cardinality metric labels.
- Encryption/session keys must survive deploys. Document backup and recovery of
  both bucket data and separately held configuration secrets.

## Implementation phases

1. Core submission types, preparation/client, storage contract, local fixtures,
   Tigris adapter, and adapter contract tests.
2. Object-backed endpoint/destination state, acceptance/idempotency, durable worker,
   GitHub creation and recovery semantics.
3. Linear creation and provider-specific error/reconciliation tests; CLI.
4. GitHub admin auth, Go shells/APIs, Svelte pages, embedded asset build.
5. Operational docs, Fly/container examples, CI, and scoped live verification.

These phases describe sequencing, not authorization to publish or deploy.

## Testing plan

- Native Go tests with real local HTTP/storage fixtures; mock external boundaries.
- Shared storage contract: create conflicts, stale replacement rejection,
  read/version behavior, pagination, cancellation, and ambiguous outcomes.
- Acceptance: no 202 before durable persistence, request limits, no unauthorized
  provider calls, rotation/revocation, stable idempotent retry, differing-body 409.
- Worker: restart recovery across all pages, storage/provider failures, retry
  bounds, conditional update conflicts, success followed by state-write failure,
  unfinished delivering states, and explicit uncertain-resolution behavior.
- Providers: assert actual outbound request/body/target and response/error parsing;
  account for GraphQL errors inside successful HTTP responses from Linear.
- Auth: allowlist enforcement, identity rebinding conflicts, removal, CSRF, OAuth
  state, logout, secret/payload redaction, and safe untrusted content rendering.
- Built CLI and an embedding example exercise the public package; frontend build
  and browser verification cover the authenticated administration path.
- Formatting, lint, vet, relevant race tests and bounded input fuzzing; inspect
  reachable vulnerabilities and verify supported Go versions during implementation.
- Run an opt-in Tigris contract suite against a disposable prefix in an authorized
  existing bucket before claiming provider compatibility. No bucket provisioning
  or external issue creation without corresponding authorization. Record fixture,
  hosted-provider, and release-artifact evidence separately.

## Acceptance criteria

- Two applications can submit distinct source/context data through one endpoint.
- One generated URL is enough for submission; provider credentials stay central.
- Accepted submissions survive process loss and remain discoverable until resolved.
- Repeated idempotent intake does not create multiple submission records.
- GitHub and Linear issues preserve feedback/context and return stored references.
- Uncertain external creation never silently becomes a definite failure or blind
  automatic recreation.
- Authorized GitHub users can configure endpoints/destinations and inspect or
  resolve delivery failures; unauthorized users cannot reach these capabilities.
- The core/client, storage adapter, issue adapters, CLI, and UI have the stated
  dependency and ownership boundaries; no database is required.

## Evidence and remaining verification

- [Fly Tigris integration](https://docs.fly.io/tigris) documents the S3 API,
  Fly-injected configuration, and Go SDK integration.
- [Tigris features](https://www.tigrisdata.com/features/) documents conditional
  writes and distinguishes consistency by placement type. Detailed linked docs
  could not be read through the web tool because of its Markdown content-type
  limitation. No live bucket or account has been inspected.
- [GitHub issue creation](https://docs.github.com/en/rest/issues/issues#create-an-issue)
  and [Linear GraphQL](https://linear.app/developers/graphql) are the provider
  contracts to verify in implementation; no live issues have been created.
- Library choices, UI styling, exact exported APIs, and tuning defaults remain
  implementer decisions. Single-process operation, scan-based recovery, limits,
  credential modes, and uncertainty handling are proposals for design approval.

## Deployment refinement after implementation

Use the Fly CLI directly. `fly.toml` owns native rolling deployment with one
unavailable machine, graceful termination and readiness checks; deploy with
`--ha=false` and keep exactly one application machine. The manual GitHub workflow
calls the CLI directly. This supersedes the earlier custom stop-before-start
deployment tooling choice.
