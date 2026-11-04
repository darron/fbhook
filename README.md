# fbhook

**Turn application and agent feedback into GitHub or Linear issues.**

Send feedback and JSON to a secret URL. fbhook saves the submission in Tigris,
then a background worker creates the issue. Your application owns the feedback
form or agent tool; issue readers decide what happens next.

```text
Application / agent → secret URL → object storage → worker → GitHub / Linear
```

## Install the CLI

```sh
brew install darron/tap/fbhook
fbhook version
```

Homebrew installs a prebuilt binary for macOS or Linux on Apple Silicon/ARM64
or Intel/AMD64. It needs no Go, Node or Python installation. Downloads require
public release assets.

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

Use a native client from your backend:

| Language | Client | Runnable example |
| --- | --- | --- |
| Go | `github.com/darron/fbhook` | [Submit](examples/submit/main.go) |
| Ruby | [Ruby gem](clients/ruby/README.md) | [Submit](clients/ruby/examples/submit.rb) |
| Rust | [Rust crate](clients/rust/README.md) | [Submit](clients/rust/examples/submit.rs) |
| TypeScript / JavaScript | [npm package](clients/typescript/README.md) | [Submit](clients/typescript/examples/submit.mjs) |

The new client packages can be built and used from this repository. See the
[sending reference](docs/INTEGRATION.md) for the shared contract and retry rules.

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
Run `make ui clients` to check the Ruby, Rust and TypeScript packages, including
local examples against the real intake API.
See [AGENTS.md](AGENTS.md) for code boundaries, tool requirements and the PR workflow.

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

## License

[MIT](LICENSE). Bundled components retain the licenses in
[third-party notices](THIRD_PARTY_NOTICES.txt).
