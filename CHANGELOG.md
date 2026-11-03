# Changelog

## Unreleased

### Fixed

- Pass strict Homebrew audits while retaining protection against formula downgrades.

## 0.1.0 — 2026-11-03

### Added

- Shared feedback intake with secret URLs, JSON context and idempotency keys.
- Tigris-backed storage and recoverable background delivery to GitHub and Linear.
- GitHub OAuth admin for destinations, endpoints and delivery recovery.
- Standard-library-only Go client, CLI and embedded Svelte admin.
- CI, release archives/checksums, Homebrew automation and manual Fly deployment.
- MIT license and bundled third-party notices in release archives.
- Public Fly configuration sample with private deployment configuration kept local.

### Fixed

- Authenticate before reading intake bodies; bound concurrent work so stalled
  unauthenticated requests cannot exhaust intake capacity.
- Preserve creation uncertainty after abandonment and history truncation; require
  duplicate-risk acknowledgement before retrying and keep issue linking available.
- Recognize GitHub secondary rate limits without `Retry-After`; preserve candidate
  lookup failures and retry hints during reconciliation.
- Verify captured Linear labels during reconciliation, including paginated labels.
- Keep expanded submission history visible during polling and manual refresh.

### Documentation

- Replace completed Superpowers plans/specs with concise integration and operations docs.
- Add `AGENTS.md` for code boundaries, review, verification and delivery workflow.
- Link the three Tweets that inspired fbhook; keep installation details in a private runbook.
- Apply the `i-have-adhd` writing skill throughout the maintained docs.
