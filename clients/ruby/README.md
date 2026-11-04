# Send feedback from Ruby

**Keep `FBHOOK_ENDPOINT` in your backend environment.** The URL is a credential.
This package uses Ruby 3.3+ and the standard library.

## Run the example

1. From the repository root, set your private endpoint and event key:

   ```sh
   export FBHOOK_ENDPOINT='https://YOUR_HOST/api/v1/feedback/ENDPOINT_ID.SECRET'
   export FBHOOK_IDEMPOTENCY_KEY='ruby-example-event-123'
   ```

2. Send the sample feedback:

   ```sh
   ruby -I clients/ruby/lib clients/ruby/examples/submit.rb
   ```

The example prints a JSON acceptance receipt. **Accepted means saved, not delivered.**
Reusing the key and unchanged sample payload returns the same receipt. Choose a
new key for a new event.

## Use the gem locally

```sh
cd clients/ruby
ruby -S gem build fbhook.gemspec
ruby -S gem install --local ./fbhook-0.1.0.gem
```

```ruby
require "fbhook"

client = Fbhook::Client.new(ENV.fetch("FBHOOK_ENDPOINT"))
receipt = client.submit(
  { feedback: "Keep search filters", source: { app: "archive", actor: "agent" },
    context: { "page" => "/search" } },
  key: "archive-feedback-event-123"
)
puts receipt.id
```

Use `Fbhook.prepare(defaults, submission)` to overlay source defaults and copy
context. Catch `Fbhook::HTTPError` for a response status; its `status` excludes
the response body. Client errors omit the endpoint URL.

See the [shared limits and retry rules](../../docs/INTEGRATION.md#retry-safely).
Run `make clients-ruby` from the repository root to test this package. Tests need
Minitest: `ruby -S gem install minitest --version 5.27.0 --no-document`.
The gem is prepared here; these instructions do not require a RubyGems release.
