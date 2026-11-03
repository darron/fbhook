# Feedback Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the approved self-hosted feedback intake, Tigris persistence, reliable issue delivery, Go client/CLI, and private administration.

**Architecture:** A small public Go client depends on the standard library. A shared service owns object-backed state and worker transitions; independent storage and issue adapters implement its boundaries. Go serves authenticated shells and APIs with embedded Vite/Svelte assets.

**Tech Stack:** Go (minimum 1.26, local patched 1.27.3), AWS SDK v2 for Tigris, OAuth2 and secure cookie libraries, Svelte/Vite, GitHub Actions, native Go tests and browser verification.

**Spec:** `docs/superpowers/specs/2026-11-02-feedback-service-design.md` (approved 2026-11-02).

## Global Constraints

- No database, external queue, required LLM, or hosted multitenant accounts.
- Persist feedback and pending delivery together before returning 202.
- One process/region, zero-overlap worker deployments; conditional object updates.
- 64 KiB intake limit; arbitrary JSON context; exact-byte idempotency per endpoint.
- GitHub and Linear adapters; configured labels only; no caller-selected destination.
- Submission URL secrets are write-only, 32 random bytes, digest stored.
- Encrypted provider credentials, GitHub allowlist by stable ID, CSRF and OAuth state.
- 10 attempts, initial backoff 2 seconds, maximum 15 minutes; bounded histories of 20.
- External creation uncertainty must never trigger blind automatic recreation.
- No provisioning, live issue creation, publishing, or production deployment is authorized by this plan.

## Review Focus

- A stored acceptance whose HTTP response is lost must resolve to the same receipt on retry (Tasks 1, 3).
- Successful provider creation followed by storage failure must become uncertain rather than duplicate (Task 3).
- Credential rotation must affect pending deliveries without rerouting them (Tasks 3, 4).
- OAuth usernames may be renamed/reassigned; persisted ID bindings must not silently change (Task 5).
- Untrusted feedback/JSON must remain literal text in issue formatting and admin rendering (Tasks 1, 5).

## Task 1: Submission package and client

**Files:** `go.mod`, `feedback.go`, `client.go`, `feedback_test.go`, `client_test.go`, `example_test.go`.
**Interfaces:** `Feedback`, `Source`, `Receipt`; `Parse([]byte) (Feedback,error)`, `Prepare(Feedback,Feedback) Feedback`, `NewClient(string,*http.Client) (*Client,error)`, `(*Client).Submit(context.Context,Feedback,string) (Receipt,error)`.

- [ ] Write tests for strict envelope parsing, raw JSON context, derived titles, immutable preparation, idempotent request bytes, typed/redacted HTTP errors and cancellation.
- [ ] Run `go test .` and observe missing behavior; implement standard-library-only package and safe issue formatting.
- [ ] Run `go test .` and public embedding example; record results.

## Task 2: Storage contract and Tigris

**Files:** `storage/store.go`, `storage/memory/memory.go`, `storage/contract/contract.go`, `storage/tigris/tigris.go`, accompanying tests.
**Interfaces:** `Store.Get(ctx,key) (Object,error)`, `Create(ctx,key,body) (Version,error)`, `Replace(ctx,key,body,Version) (Version,error)`, `List(ctx,prefix,cursor,limit) (Page,error)`; opaque versions and typed not-found/conflict errors.

- [ ] Write shared contract tests covering conflicting concurrent creates, stale replacement, canceled calls, exact bytes and multipage enumeration; run before implementation.
- [ ] Implement concurrency-safe fixture and AWS SDK-backed adapter with Fly environment configuration and bounded HTTP clients.
- [ ] Test actual HTTP S3 wire behavior (precondition headers, errors, pagination) and opt-in disposable-prefix live contract suite.
- [ ] Run `go test ./storage/...`.

## Task 3: Object-backed service and worker

**Files:** `service/{service,config,accept,worker,admin,crypto}.go`, corresponding tests.
**Interfaces:** `New(storage.Store,Options) (*Service,error)`; endpoint/destination CRUD; `Accept(ctx,credential,body,key) (Receipt,error)`; `Run(ctx) error`; `Scan(ctx,recover bool) error`; provider interface `Validate`, `Create`, `Find` with explicit typed outcomes.

- [ ] Write service tests for durable acceptance, same-key conflicts, secret rotation, two apps/one endpoint, destination snapshots and current credentials.
- [ ] Run tests RED; implement encrypted config revisions and authoritative one-object submissions.
- [ ] Write worker tests for stored pending scan, CAS races, backoff, manual retry/abandon/link, delivering recovery, uncertain outcomes, and failed success-state persistence; run RED.
- [ ] Implement serial worker attempts, full paginated recovery scans, bounded audit/error histories, cancellation and safe provider error classification.
- [ ] Run `go test -race ./service/...`.

## Task 4: Issue adapters

**Files:** `issues/{github,linear}/*.go` and tests.
**Interfaces:** Consume service provider contracts; configured injectable base URLs and HTTP client; literal submission marker and target-aware reconciliation.

- [ ] Write real HTTP fixture tests for GitHub/Linear validation, create payloads, positive marker lookup, bad credentials, limits, GraphQL partial errors and uncertain timeout/5xx behavior; run RED.
- [ ] Implement provider APIs, safe Markdown formatting, label/target validation, and known-rejection retry hints.
- [ ] Run `go test ./issues/... ./service/...`.

## Task 5: Admin authentication, HTTP API, and frontend

**Files:** `web/{web,auth,api,assets}.go`, templates, `web/ui/{package.json,vite.config.js,src/*}`, tests.
**Interfaces:** `web.New(service,Options) (http.Handler,error)`; signed cookie auth, GitHub OAuth, typed JSON bootstrap, admin service calls and CSRF header; explicit `/api/v1/admin` routes.

- [ ] Write HTTP auth tests for anonymous methods/paths, identity-binding conflicts, removal, OAuth state, logout, CSRF and safe feedback rendering; run RED.
- [ ] Implement allowlist/account binding, established OAuth/cookie libraries, private Go routes and APIs, request limits and sanitized diagnostics.
- [ ] Build Svelte administration with endpoint creation/rotation/disable, destination validation/configuration/credential rotation, paginated submission history, safe details, retry and uncertainty resolution.
- [ ] Run Go HTTP tests, frontend type/build checks, and browser smoke using test-only authenticated fixtures (no production auth bypass).

## Task 6: CLI, server, and operator flow

**Files:** `cmd/fbhook/*.go`, `internal/cli/*.go`, `examples/submit/main.go`, tests.
**Interfaces:** `cli.Run(ctx,args,in,out,errOut) error`; commands `serve`, `send`, `version`, `keygen`; environment configuration for secrets and endpoint.

- [ ] Write CLI tests for stdin JSON, idempotent delivery, clean machine stdout, invalid configuration and version metadata; run RED.
- [ ] Implement CLI over shared client/service, bounded HTTP server and graceful worker shutdown; required key validation and no secret logging.
- [ ] Build and execute CLI and embedding example against a local fixture.

## Task 7: Delivery tooling, documentation, final review

**Files:** `Makefile`, `.golangci.yml`, `.github/workflows/{ci,release,homebrew,deploy}.yml`, `Dockerfile`, `fly.toml`, `scripts/*.py`, `README.md`, `CHANGELOG.md`, operator/testing/security docs.

- [ ] Adapt go-opentunnel's pinned-action CI, supported-Go matrix, race/vet/lint/vulnerability checks, bounded fuzzing, immutable release archives/checksums, release-triggered Homebrew updates, and artifact tests.
- [ ] Add manual Fly deployment workflow with serialized stop-before-start operation and always-running worker; do not deploy it.
- [ ] Document setup, credential/key management, SDK/CLI use, agent integration, recovery, configuration and unverified live boundaries.
- [ ] Run full checks, build container configuration where supported, inspect UI, smoke built artifacts, and request one independent whole-change review.
- [ ] Fix consequential review findings with regression tests; run final affected checks, record evidence and leave work reviewable.

## Execution record

Execution is authorized by the user's explicit instruction to scaffold and build.
Work stays on `feat/initial-service` in the otherwise empty checkout: there is no
baseline commit from which to create a worktree and no existing implementation to
isolate. Commit signing previously stalled; preserve configured signing and do not
silently disable it. Implementation progress lives in `.superpowers/progress.md`.

## Verification outcome

All seven implementation areas are built. Local verification passed: full Go
unit/HTTP suite and race detector, vet, golangci-lint (zero issues), frontend
type check (zero warnings)/build, 10 release/delivery-script tests, bounded parser
fuzzing (214,233 executions), govulncheck (no findings), Go 1.26.10 minimum
compiler suite, Chromium desktop/mobile workflows, and native CLI/example
HTTP-fixture smoke. Docker image builds and native container CLI smoke pass.
Workflow syntax passes actionlint.

One independent whole-change review found six consequential issues; each was
reproduced by failing regression tests and corrected, followed by a green suite.
The exact-byte raw envelope retention and refresh/pagination refinements remain
minor deferred work. Hosted CI/release/tap execution and live issue-provider writes
remain unverified. See docs/OPERATIONS.md for those gates and the provisioned Fly
installation. The live Tigris storage contract passed against the dedicated
`darron-fbhook` bucket on 2026-11-02.

The explicitly authorized Fly provisioning and initial deployment are now complete:
private bucket, nine service/storage/OAuth secrets, public addresses, and one
Machine in `yyz`. Public health/readiness checks and unauthorized admin rejection
passed. Browser sign-in reached the private admin as `darron`, and the submission
panel showed a successful worker scan. The running initial development binary
reports `fbhook 0.1.0 sha=unknown dirty`; hosted CI and real provider delivery remain
separate unverified gates.

The original checkboxes above preserve the proposed step checklist; this outcome
records completed areas and actual evidence rather than claiming every proposed
test permutation was implemented.

Deployment refinement: the manual workflow now calls `fly deploy` directly; native
rolling deployment is defined in `fly.toml`, and operations documentation describes
single-machine scale and `--ha=false`. The obsolete Python wrapper and its deployment-specific tests were removed
after explicit deletion authorization. Homebrew/release script tests remain.
