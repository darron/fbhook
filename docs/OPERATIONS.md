# Operate fbhook

**Run one process and one worker in one region.** Keep it running between requests.

## Set up the service

1. Create a private Tigris bucket and dedicated read/write/list credentials.
2. Register a GitHub OAuth app. Use your HTTPS base URL as its homepage and
   `<base-url>/auth/callback` as its callback.
3. Run `fbhook keygen`; save the encryption and session keys in your secret manager.
4. Set the variables below and start `fbhook serve` behind HTTPS.
5. Sign in, add a destination, then create and save an endpoint URL.

### Storage

| Variable | Value |
| --- | --- |
| `BUCKET_NAME` | Dedicated bucket |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | Dedicated Tigris credentials |
| `AWS_ENDPOINT_URL_S3` | Default: `https://t3.storage.dev` |
| `AWS_REGION` | Default: `auto` |
| `FBHOOK_STORAGE_PREFIX` | Optional object prefix |

### Service and sign-in

| Variable | Value |
| --- | --- |
| `FBHOOK_BASE_URL` | Canonical HTTPS origin |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | OAuth app credentials |
| `FBHOOK_GITHUB_USERS` | Comma-separated admin usernames |
| `FBHOOK_ENCRYPTION_KEY` | Base64 of 32 random bytes |
| `FBHOOK_SESSION_KEY` | Base64 of 64 random bytes |
| `FBHOOK_POLL_INTERVAL` | Default `15s`; range `1s`–`1h` |

Back up the object prefix and encryption key together. Losing the encryption key
makes stored tracker credentials unreadable. Changing the session key signs users out.

Admin names bind to stable GitHub account IDs at first sign-in. Removing a name
revokes that binding's access. For a renamed account, add and bind its new username
before removing the old entry. Never silently reassign an existing binding.
Sessions expire after eight hours; admin mutations require CSRF.

## Connect a destination

| Provider | Target | Credential |
| --- | --- | --- |
| GitHub | `owner/repository` | Fine-grained PAT: Issues read/write and metadata read; push permission for configured labels |
| Linear | Team UUID; optional project UUID | API key able to read team/project/labels and create issues |

Use existing GitHub label names or Linear label UUIDs. Labels belong to the
destination. GitHub sign-in does not grant issue-writing permission.

Save replacement tracker credentials by editing the destination. Queued work uses
the current credential but keeps its captured target, labels and rendered issue.
Pausing an endpoint stops intake; accepted submissions still deliver.

## Recover a delivery

**Open Submissions and inspect the record before retrying.**

| State | Action |
| --- | --- |
| `pending`, `delivering`, `retry_scheduled` | Check worker status; scheduled retries honor provider delays |
| `failed` | Fix credentials/configuration, then retry with a reason |
| `delivery_uncertain` | Find the issue upstream; link its provider ID if it exists |
| `delivered` | Open the recorded issue link |

The worker makes at most ten automatic creation attempts. Timeouts, ambiguous
responses and interrupted creation become uncertain; they never trigger blind
automatic recreation. An absent search result does not prove creation failed.

For an uncertain record, choose one action:

1. **Link:** supply the issue's provider ID. fbhook verifies target, marker and labels.
2. **Retry:** check upstream first; give a reason and acknowledge possible duplication.
3. **Abandon:** give a reason to stop delivery while retaining the record.

Abandoning preserves uncertainty: later linking remains possible, and a later retry
still needs acknowledgement. Records from before the durable uncertainty flag can
recover it only from retained history. Already discarded legacy history is unrecoverable.
If an issue reference is already recorded, correct that issue's defaults and link
it; retrying creation is blocked.

History retains the latest 20 events plus a lifetime count. Manual retry resets
the attempt budget. Submissions have no automatic expiry. Scans read all retained
records, so storage reads and scan time grow with history.

## Deploy to Fly

**Keep exactly one Machine.** Rolling replacement briefly interrupts service;
canary/bluegreen or extra replicas can overlap workers.

1. Use a clean `main` checkout with passing CI, unless testing a branch was requested.
2. Install app secrets and confirm `fly.toml` targets the intended app and region.
3. Check `fly machine list --app darron-fbhook` shows the intended single Machine.
4. Deploy with source identity:

```sh
set -eu
revision=$(git rev-parse HEAD)
test -z "$(git status --porcelain --untracked-files=all)"
fly deploy --app darron-fbhook --primary-region yyz --remote-only --ha=false \
  --build-arg BUILD_REVISION="$revision" --build-arg BUILD_STATE=clean
```

5. Verify the running binary, health, admin access and advancing worker scans.

| Check | Expected |
| --- | --- |
| `fbhook version` inside the running Machine | Exact deployed SHA and `clean` |
| `/healthz`, `/readyz` | HTTP 200; readiness includes storage access |
| Anonymous `/api/v1/admin/status` | HTTP 401 |
| Authenticated **Submissions** | Recent advancing scan, no scan error |
| `fly checks list --app darron-fbhook` | Passing |

Readiness alone does not prove issue delivery. For deployment problems, inspect
`fly status`, `fly checks list` and `fly logs`; do not print secrets or feedback URLs.

The manual **Deploy to Fly** workflow runs only from `main`. Configure an app-scoped
`FLY_API_TOKEN` in GitHub's `production` environment and restrict that environment
to `main`. Pushes do not deploy. `--ha=false` avoids an initial spare Machine;
`min_machines_running` is not a replica limit.

### This installation

App and bucket: `darron-fbhook`. Region: `yyz`. Admin: `darron`.
URL: [darron-fbhook.fly.dev](https://darron-fbhook.fly.dev).
GitHub target: `darron/fbhook`. Linear team: `fbhook` (`FBH`).
The configured GitHub PAT expires **2026-12-02**; replace it before expiry.

Reusable test URLs are in `/Users/darron/src/fbhook/.integration-endpoints.local`
(JSON; owner-only permissions; Git-ignored and excluded from Docker builds).
**Resume those endpoints for an authorized test; pause them afterward.** Reuse the
URLs rather than generating new endpoints. Never print or commit their contents.

Live creation passed on 2026-11-03 in one attempt per provider:
[GitHub #5](https://github.com/darron/fbhook/issues/5) and
[Linear FBH-2](https://linear.app/froese/issue/FBH-2/fbhook-pr-4-linear-delivery-verification-pr4-adddaa3-2026-11-03t20-23).
Identical intake replays returned the same receipt; changed content returned `409`.

## Verify storage

Use a bucket with conditional create/replace, coherent reads and complete paginated
listing. Check the [Tigris consistency requirements](https://www.tigrisdata.com/docs/objects/conditionals/)
for your bucket type and region before choosing a deployment topology.

With test-bucket credentials injected, run:

```sh
FBHOOK_TEST_TIGRIS=1 go test -run TestLiveContract -count=1 -v ./storage/tigris
```

This writes and retains objects under a random `fbhook-contract/<timestamp>/`
prefix. The test prints that prefix; remove it later with your storage tools.

## Publish a release

1. Update `Version` in `feedback.go` and move changelog entries into a dated release.
2. Wait for CI, then publish a stable GitHub release tagged `vMAJOR.MINOR.PATCH`.
3. Check the **Release binaries** workflow and its four macOS/Linux amd64/arm64 archives.
4. Verify `SHA256SUMS`, version metadata and native execution before reporting success.
5. Check the Homebrew update separately if using the tap.

Assets are immutable: an existing asset must be byte-identical. **Update Homebrew**
verifies published hashes and requires `HOMEBREW_TAP_TOKEN` with Contents read/write
on `darron/homebrew-tap`. That job can be rerun independently after publication.
Choose a project license before publishing for third-party reuse.
