# Operate fbhook

**Run one process and one worker in one region.** Keep it running between requests.

## Set up the service

1. Create a private Tigris or Google Cloud Storage bucket and a dedicated service identity.
2. Register a GitHub OAuth app. Use your HTTPS base URL as its homepage and
   `<base-url>/auth/callback` as its callback.
3. Run `fbhook keygen`; save the encryption and session keys in your secret manager.
4. Set the variables below and start `fbhook serve` behind HTTPS.
5. Sign in, add a destination, then create and save an endpoint URL.

### Storage

Both adapters use the same storage interface for configuration, credentials and
submission state. Choose one backend for each installation:

| Variable | Value |
| --- | --- |
| `FBHOOK_STORAGE_BACKEND` | `tigris` (default) or `gcs` |
| `BUCKET_NAME` | Dedicated private bucket |
| `FBHOOK_STORAGE_PREFIX` | Optional object prefix |

For **Tigris**, also set:

| Variable | Value |
| --- | --- |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | Dedicated Tigris credentials |
| `AWS_ENDPOINT_URL_S3` | Default: `https://t3.storage.dev` |
| `AWS_REGION` | Default: `auto` |

For **GCS**, set `FBHOOK_STORAGE_BACKEND=gcs` and provide
[Application Default Credentials](https://docs.cloud.google.com/docs/authentication/application-default-credentials).
Use an attached service account or Workload Identity Federation in production;
local development can use `gcloud auth application-default login`.
`GOOGLE_APPLICATION_CREDENTIALS` can point to an externally managed credential
configuration file. Keep that file outside Git and the bucket.

Grant the service identity bucket-scoped
[Storage Object User](https://docs.cloud.google.com/storage/docs/access-control/iam-roles)
access, or equivalent object read, list and conditional overwrite permissions.
GCS does not use the AWS variables. Changing the backend does not move existing
objects: migrate the whole prefix and retain the encryption key before switching.

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

## Block unwanted feedback

**Open IP blocklist in the admin to block a submitting machine.** Rules apply to
new feedback intake across all endpoints. Login, admin and health checks remain
accessible.

1. Open a submission to find its **Client IP**. Older records may not have one.
2. Add the address or an IPv4/IPv6 CIDR range in **IP blocklist**; optionally add a note.
3. Save the rules. Matching requests return `403` before their bodies are read or
   submissions are accepted.
4. Remove the rule and save to allow that address again.

The address identifies the machine submitting to fbhook. For a backend client,
that is your application server, not the person using its form. Addresses never
appear in generated issue content. Blocking does not delete issues or cancel
already accepted feedback. Rotate the endpoint URL to revoke a leaked credential
for every address.

### Persist or upload rules

The selected bucket stores `<prefix>/config/ip-blocklist.json`. Admin saves use
conditional writes; if another edit wins, reload the latest rules before saving.
You can also upload this JSON with your storage tools, using a conditional write
to avoid overwriting a newer version:

```json
{
  "schema": 1,
  "rules": [
    {"cidr": "203.0.113.42/32", "note": "Unwanted submissions"},
    {"cidr": "2001:db8:1234::/48", "note": "Blocked network"}
  ]
}
```

The example addresses are documentation ranges; replace them with the actual
source. The saved configuration is limited to 64 KiB and 1,024 rules; notes
can contain at most 200 characters. Upload an empty `rules` array to clear the list. Admin saves take effect immediately; external uploads are read within five
seconds on the next intake request. If the stored policy is malformed or cannot
be read, intake returns `503` until it can load valid rules.

### Identify clients behind a proxy

Direct deployments use the socket peer IP and ignore forwarded headers. Behind
an HTTP proxy, configure both variables explicitly:

| Variable | Value |
| --- | --- |
| `FBHOOK_TRUSTED_PROXY_CIDRS` | Comma-separated networks of the actual connecting proxy peers |
| `FBHOOK_CLIENT_IP_HEADER` | `Fly-Client-IP` or `X-Forwarded-For` |

For Fly, use `Fly-Client-IP` with trusted peer ranges appropriate to your network.
Verify the proxy peer addresses and restrict direct access before enabling trust;
do not trust a header merely because it exists. See
[Fly request headers](https://docs.fly.io/networking/request-headers).
A proxy in front of Fly requires its own verified trust chain.
For `X-Forwarded-For`, fbhook walks from the nearest hop toward the client,
stopping at the first address outside the trusted proxy networks.
Malformed trusted headers fail intake closed. Untrusted peers cannot choose an
address by sending these headers.

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
2. Copy `fly.toml.sample` to `fly.toml`. Set your app name, URL, admin username
   and region, then install app secrets. `fly.toml` is Git-ignored and excluded
   from Docker builds.
3. Check `fly machine list --app YOUR_APP` shows the intended single Machine.
4. Deploy with source identity:

```sh
set -eu
revision=$(git rev-parse HEAD)
test -z "$(git status --porcelain --untracked-files=all)"
fly deploy --app YOUR_APP --primary-region YOUR_REGION --remote-only --ha=false \
  --build-arg BUILD_REVISION="$revision" --build-arg BUILD_STATE=clean
```

5. Verify the running binary, health, admin access and advancing worker scans.

| Check | Expected |
| --- | --- |
| `fbhook version` inside the running Machine | Exact deployed SHA and `clean` |
| `/healthz`, `/readyz` | HTTP 200; readiness includes storage access |
| Anonymous `/api/v1/admin/status` | HTTP 401 |
| Authenticated **Submissions** | Recent advancing scan, no scan error |
| `fly checks list --app YOUR_APP` | Passing |

Readiness alone does not prove issue delivery. For deployment problems, inspect
`fly status`, `fly checks list` and `fly logs`; do not print secrets or feedback URLs.

The manual **Deploy to Fly** workflow runs only from `main`. Configure an app-scoped
`FLY_API_TOKEN` and a `FLY_CONFIG_TOML` secret containing your complete private
`fly.toml` in GitHub's `production` environment. Restrict that environment to `main`.
The workflow writes the ignored config only for deployment, then removes it.
Pushes do not deploy. `--ha=false` avoids an initial spare Machine;
`min_machines_running` is not a replica limit.

## Verify storage

Use a bucket with conditional create/replace, coherent reads and complete paginated
listing. Check the [Tigris consistency requirements](https://www.tigrisdata.com/docs/objects/conditionals/)
for your bucket type and region before choosing a deployment topology.

With test-bucket credentials injected, run:

```sh
FBHOOK_TEST_TIGRIS=1 go test -run TestLiveContract -count=1 -v ./storage/tigris
```

For GCS, inject test-bucket ADC and use:

```sh
FBHOOK_TEST_GCS=1 go test -run TestLiveContract -count=1 -v ./storage/gcs
```

Each command writes and retains objects under a random `fbhook-contract/<timestamp>/`
prefix. The test prints that prefix; remove it later with your storage tools.

## Publish a release

1. Update `Version` in `feedback.go` and move changelog entries into a dated release.
2. Wait for CI, then publish a stable GitHub release tagged `vMAJOR.MINOR.PATCH`.
3. Check the **Release binaries** workflow and its four macOS/Linux amd64/arm64 archives.
4. Verify `SHA256SUMS`, version metadata and native execution before reporting success.
5. Check the Homebrew update separately if using the tap.

Assets are immutable: an existing asset must be byte-identical. After publication,
**Update Homebrew after release** dispatches [the tap's validation workflow](https://github.com/darron/homebrew-tap/blob/main/.github/workflows/update-fbhook.yml).
The tap checks every archive, installs and tests the native binary, then publishes
only the validated formula from a separate runner.

### Enable automatic tap updates

1. Create a fine-grained GitHub token scoped to `darron/homebrew-tap`, with
   **Actions: read and write**.
2. Add it as the `HOMEBREW_TAP_TOKEN` secret in `darron/fbhook`.
3. Publish a stable release. The tap update starts after binary publication.

Manual **Release binaries** runs update the tap only when `update_homebrew` is
selected. To retry a published release directly in the tap with your host login:

```sh
env -u GH_TOKEN -u GITHUB_TOKEN gh workflow run update-fbhook.yml \
  --repo darron/homebrew-tap --ref main -f tag=vMAJOR.MINOR.PATCH
```

The tap refuses downgrades, changed metadata for an existing tag, and manual
formula edits. Check both the source release workflow and the tap workflow before
reporting the update complete.

Release archives include the MIT license and bundled dependency notices. Python
is used by release automation and browser checks; the installed binary and
deployed service do not need it.
