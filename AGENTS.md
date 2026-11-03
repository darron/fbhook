# Work on fbhook

**Finish the requested change, verify it, and leave a reviewable PR.**

## Working style

1. Read the current code and relevant docs before proposing changes. Use previous
   sessions for context; verify their claims against the checkout and live state.
2. Work from current `main` on a clean branch/worktree. Preserve unrelated edits,
   ignored local credentials and host-managed worktrees.
3. Make routine choices and keep working within the request. Ask only when missing
   information or a platform gate blocks a consequential action. Existing approval
   carries through the task; do not ask for it again.
4. Give short updates with concrete findings. At handoff, state what changed,
   which checks passed, what remains unverified, and link the PR or artifact.
5. Merge, release and deploy when requested. Before merging, wait for CI and guard
   the exact reviewed head. Verify the resulting remote state.

## Keep these boundaries

- The root Go client/preparation package uses only the standard library. Keep
  storage, OAuth, provider SDKs and UI dependencies outside its import path.
- Object storage is the durable record and work queue. Persist the submission and
  pending state together before returning `202`; use conditional writes and opaque
  storage versions.
- Run one serial worker in one process/region. Stop the old worker before its
  replacement starts. Extra replicas require a coordination design.
- Accepted work captures routing and labels; delivery reads the current credential.
  Uncertain creation requires positive reconciliation or an acknowledged manual
  retry. Preserve uncertainty through abandon, failure and history truncation.
- Authenticate and admit intake before reading caller-controlled bodies. Keep
  feedback/JSON untrusted, admin routes/assets private, and secrets out of output.

Code map: `service/` owns state and delivery; `storage/` owns persistence adapters;
`issues/` owns trackers; `web/` owns Go routes, OAuth and the embedded Svelte UI;
`internal/cli/` wires commands. Applications own forms/tools; issue readers own follow-up.

## Verify the change

Reproduce consequential bugs before fixing them. Add regression coverage for the
failure, then run the relevant checks. Use the existing Make targets:

| Change | Check |
| --- | --- |
| Code or dependency change | `make check` (UI, Go tests/races/vet/lint, script tests, builds) |
| UI or admin workflow | Also `make browser` |
| Parser or security boundary | Also `make fuzz` and `make vuln` as applicable |
| Documentation only | Check links, commands and claims; run `make scripts` if README/release docs change |

Tools: Go 1.26 minimum; CI Go 1.27.3, Node 26.13.0, golangci-lint 2.15.0,
Python 3. Browser checks need Python `playwright==1.64.1` and Chromium.
Run `make ui` before direct Go tests in a fresh checkout; Go embeds `web/ui/dist`.
Never add a production authentication bypass for testing.

## Reviews and GitHub

Use the requested Amp, Claude or OpenCode review skill on the real checkout.
A named review authorizes sharing the relevant code/diff with that reviewer.
Confirm findings against code/tests, fix substantive issues, and rerun affected checks.
A quiet reviewer may need 20 minutes; do not cancel it early unless it fails,
exits, or the user stops it. Review permission alone does not authorize deployment.

Use host-authenticated `gh` with injected tokens unset:

```sh
env -u GH_TOKEN -u GITHUB_TOKEN gh pr view NUMBER
env -u GH_TOKEN -u GITHUB_TOKEN gh pr merge NUMBER --merge --match-head-commit SHA
```

Preserve commit-signing settings. GitHub/network sandbox failures need the host
execution lane, not alternate credentials or a silent connector switch.

## Deploy and test live

Follow [operations](docs/OPERATIONS.md#deploy-to-fly). Embed the exact revision and
clean state. Check the running executable, Fly readiness, authenticated worker scans
and admin rendering. Report acceptance, delivery and provider read-back separately.

For authorized issue-creation tests, reuse saved URLs from the operator's private
installation runbook. Resume, submit with a unique key,
replay the same payload/key, verify issue content, then pause the test endpoints.
Do not rotate or recreate URLs each run. Keep credentials out of logs, commits,
review packets and issue bodies.

## Write docs people can scan

Apply [i-have-adhd](https://github.com/ayghri/i-have-adhd/tree/main/skills/i-have-adhd)
to every new or edited document, including this file and `CHANGELOG.md`.
Read its `SKILL.md` before editing. Lead with the action; number procedures; keep
steps small, paragraphs short and lists to five items where practical.

Keep the README as the entry point, integration details in `docs/INTEGRATION.md`,
and setup/recovery/deployment in `docs/OPERATIONS.md`. Record user-visible changes
in `CHANGELOG.md`. Link to the source instead of copying implementation detail.
Keep plans in the working conversation. Do not recreate Superpowers plans, design
transcripts or session logs in this repository.
