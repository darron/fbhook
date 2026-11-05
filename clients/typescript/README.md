# Send feedback from TypeScript or JavaScript

**Use this package on your backend.** Keep `FBHOOK_ENDPOINT` out of browser code;
the URL is a credential. Node.js 22+ supplies the HTTP transport through `fetch`.
The package is ESM, includes TypeScript declarations and has no runtime dependencies.

## Run the example

1. Build the package from the repository root:

   ```sh
   npm --prefix clients/typescript ci
   npm --prefix clients/typescript run build
   ```

2. Set your private endpoint and event key:

   ```sh
   export FBHOOK_ENDPOINT='https://YOUR_HOST/api/v1/feedback/ENDPOINT_ID.SECRET'
   export FBHOOK_IDEMPOTENCY_KEY='typescript-example-event-123'
   ```

3. Send the sample feedback:

   ```sh
   node clients/typescript/examples/submit.mjs
   ```

The example prints a JSON acceptance receipt. **Accepted means saved, not delivered.**
Reusing the key and unchanged sample payload returns the same receipt. Choose a
new key for a new event.

## Use the package locally

1. Create the npm archive after building:

   ```sh
   cd clients/typescript
   npm pack
   ```

2. Install that archive from your backend project:

   ```sh
   npm install /path/to/fbhook/clients/typescript/darron-fbhook-0.1.0.tgz
   ```

```typescript
import { Client } from "@darron/fbhook";

const client = new Client(process.env.FBHOOK_ENDPOINT!);
const receipt = await client.submit({
  feedback: "Keep search filters",
  source: { app: "archive", actor: "agent" },
  context: { page: "/search" },
}, {
  idempotencyKey: "archive-feedback-event-123",
  signal: AbortSignal.timeout(30_000),
});
console.log(receipt.id);
```

Use `prepare(defaults, submission)` to overlay source defaults and copy context.
Catch `HTTPError` to inspect `statusCode`. Client errors omit the endpoint URL
and response body. An optional `AbortSignal` cancels requests and retry waits.
Context uses JSON values; represent IDs outside JavaScript's safe integer range
as strings. `bigint`, cycles and non-finite numbers are rejected.

See the [shared limits and retry rules](../../docs/INTEGRATION.md#retry-safely).
Run `make clients-typescript` from the repository root to build and test.
This package is prepared here; these instructions do not require an npm release.
