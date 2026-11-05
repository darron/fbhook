#!/usr/bin/env ruby
# frozen_string_literal: true

require "json"
$LOAD_PATH.unshift(File.expand_path("../lib", __dir__))
require "fbhook"

endpoint = ENV.fetch("FBHOOK_ENDPOINT") { abort "FBHOOK_ENDPOINT is required" }
key = ENV.fetch("FBHOOK_IDEMPOTENCY_KEY") { abort "FBHOOK_IDEMPOTENCY_KEY is required; reuse it when retrying this feedback event" }

receipt = Fbhook::Client.new(endpoint).submit(
  {
    feedback: "Example feedback submitted by the Ruby client",
    source: { app: "example", version: "1.0", environment: "development", actor: "operator" },
    context: { "example" => true }
  },
  key: key
)
puts JSON.generate(id: receipt.id, status: receipt.status)
