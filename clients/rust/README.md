# Send feedback from Rust

**Keep `FBHOOK_ENDPOINT` in your backend environment.** The URL is a credential.
Rust 1.85+ is required. The async client uses `reqwest` with rustls, Serde and Tokio.

## Run the example

1. From the repository root, set your private endpoint and event key:

   ```sh
   export FBHOOK_ENDPOINT='https://YOUR_HOST/api/v1/feedback/ENDPOINT_ID.SECRET'
   export FBHOOK_IDEMPOTENCY_KEY='rust-example-event-123'
   ```

2. Send the sample feedback:

   ```sh
   cargo run --manifest-path clients/rust/Cargo.toml --example submit
   ```

The example prints a JSON acceptance receipt. **Accepted means saved, not delivered.**
Reusing the key and unchanged sample payload returns the same receipt. Choose a
new key for a new event.

## Use the crate locally

Add a path dependency to your application's `Cargo.toml`:

```toml
[dependencies]
fbhook = { path = "/path/to/fbhook/clients/rust" }
```

Create `fbhook::Client::new(endpoint)` and call
`client.submit(&feedback, Some(event_key)).await` inside your Tokio runtime.
Use `fbhook::prepare(&defaults, &submission)` to overlay source defaults and
copy context. The [complete example](examples/submit.rs) shows the native types.

Errors expose an optional HTTP status through `error.status()`. Their display
and debug output omit the endpoint URL and response body. Dropping the submit
future cancels its request or retry wait.

See the [shared limits and retry rules](../../docs/INTEGRATION.md#retry-safely).
Run `make clients-rust` from the repository root to check formatting, lint and tests.
The crate is prepared here; these instructions do not require a crates.io release.
