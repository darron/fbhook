# fbhook

**Turn application and agent feedback into GitHub or Linear issues.**

Send feedback and JSON to a secret URL. fbhook saves the submission in Tigris,
then a background worker creates the issue. Your application owns the feedback
form or agent tool; issue readers decide what happens next.

```text
Application / agent → secret URL → object storage → worker → GitHub / Linear
```

## Send feedback

1. Sign in to fbhook with GitHub.
2. Add a destination: a GitHub repository or Linear team.
3. Create an endpoint and save its one-time URL in your backend's secret store.
4. Set `FBHOOK_ENDPOINT` in your backend environment and send:

```sh
fbhook send --app archive --actor agent \
  --feedback 'Keep search filters when returning to results' \
  --context '{"page":"/search"}' \
  --key archive-feedback-event-123
```

**An `accepted` receipt means saved, not yet delivered.** Check **Submissions**
in the admin for the delivery result. Reuse the same key and payload for retries.

Several apps can share an endpoint. Keep its URL out of browser code and logs.

For Go, import `github.com/darron/fbhook` and use `NewClient`, `Prepare` and
`Submit`. See the [runnable example](examples/submit/main.go) or
[sending reference](docs/INTEGRATION.md).

## Build and run

Use Go 1.26+, Node 26.13.0 and npm. CI pins Go 1.27.3.

```sh
make build
bin/fbhook version
```

Follow [service setup](docs/OPERATIONS.md#set-up-the-service) to configure storage,
OAuth and keys, then run:

```sh
bin/fbhook serve
```

For development, install `golangci-lint` 2.15.0 and Python 3, then run `make check`.
See [AGENTS.md](AGENTS.md) for code boundaries, checks and the PR workflow.

## Find the right doc

| Task | Read |
| --- | --- |
| Send from Go, a CLI or an agent | [Integration](docs/INTEGRATION.md) |
| Configure, deploy or recover delivery | [Operations](docs/OPERATIONS.md) |
| Change the code | [AGENTS.md](AGENTS.md) |
| See what changed | [Changelog](CHANGELOG.md) |

## Inspiration

These Tweets started the project:
[@patrickc](https://x.com/patrickc/status/2116636967517798672),
[@zeeg](https://x.com/zeeg/status/2116657400312971463), and
[@tanayj](https://x.com/tanayj/status/2116313952888263141).
