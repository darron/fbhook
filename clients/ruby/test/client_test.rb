# frozen_string_literal: true

require "json"
require "minitest/autorun"
require "socket"
require "stringio"

class ClientContractTest < Minitest::Test
  def setup
    require "fbhook"
  end

  def test_submits_an_accepted_receipt
    with_server do |server, requests|
      server.on_request { |_request| [202, { "id" => "receipt-1", "status" => "accepted" }.to_json] }
      receipt = Fbhook::Client.new(server.endpoint).submit(
        { feedback: "The export timed out", source: { app: "archive", actor: "agent" }, context: { "page" => "/exports" } },
        key: "event-1"
      )

      assert_equal "receipt-1", receipt.id
      assert_equal "accepted", receipt.status
      assert_equal "event-1", requests.fetch(0).fetch("idempotency-key")
      assert_equal "application/json", requests.fetch(0).fetch("content-type")
    end
  end

  def test_retries_lost_receipt_with_identical_bytes_and_key
    with_server do |server, requests|
      server.on_request do |_request|
        requests.length == 1 ? [202, "{\"id\":", { "content-length" => "40" }] : [202, { "id" => "receipt-2", "status" => "accepted" }.to_json]
      end
      receipt = Fbhook::Client.new(server.endpoint).submit({ feedback: "Retry safely" }, key: "stable-event")

      assert_equal "receipt-2", receipt.id
      assert_equal 2, requests.length
      assert_equal requests[0].fetch("body"), requests[1].fetch("body")
      assert_equal "stable-event", requests[1].fetch("idempotency-key")
    end
  end

  def test_generates_one_key_for_retries
    with_server do |server, requests|
      server.on_request do |_request|
        requests.length == 1 ? [503, "unavailable"] : [202, { "id" => "receipt-3", "status" => "accepted" }.to_json]
      end
      Fbhook::Client.new(server.endpoint).submit({ feedback: "Generated key" })

      assert_equal 2, requests.length
      assert_equal requests[0].fetch("idempotency-key"), requests[1].fetch("idempotency-key")
      refute_empty requests[0].fetch("idempotency-key")
    end
  end

  def test_does_not_retry_conflict_or_malformed_receipt
    with_server do |server, requests|
      server.on_request { |_request| [409, "private response"] }
      error = assert_raises(Fbhook::HTTPError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "No retry" }, key: "one") }
      assert_equal 409, error.status
      assert_equal 1, requests.length
      refute_includes error.message, "private"
    end

    with_server do |server, requests|
      server.on_request { |_request| [202, '{"id":"x","status":"queued"}'] }
      assert_raises(Fbhook::ReceiptError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "Bad receipt" }, key: "two") }
      assert_equal 1, requests.length
    end

    with_server do |server, requests|
      server.on_request { |_request| [202, '{"id":'] }
      assert_raises(Fbhook::ReceiptError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "Bad JSON" }, key: "three") }
      assert_equal 1, requests.length
    end

    with_server do |server, requests|
      server.on_request { |_request| [400, "private" * 1000] }
      error = assert_raises(Fbhook::HTTPError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "No retry for a large client error" }, key: "four") }
      assert_equal 400, error.status
      assert_equal 1, requests.length
    end
  end

  def test_retains_known_status_when_a_non_accepted_body_is_truncated
    with_server do |server, requests|
      server.on_request { |_request| [409, "partial", { "content-length" => "40" }] }
      error = assert_raises(Fbhook::HTTPError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "Known conflict" }, key: "conflict") }
      assert_equal 409, error.status
      assert_equal 1, requests.length
    end

    with_server do |server, requests|
      server.on_request { |_request| [503, "partial", { "content-length" => "40" }] }
      error = assert_raises(Fbhook::HTTPError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "Known transient failure" }, key: "unavailable") }
      assert_equal 503, error.status
      assert_equal 3, requests.length
    end
  end

  def test_never_follows_redirects
    with_server do |server, requests|
      server.on_request { |_request| [302, "redirect", { "location" => server.endpoint }] }
      error = assert_raises(Fbhook::HTTPError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "No redirect" }, key: "three") }
      assert_equal 302, error.status
      assert_equal 1, requests.length
    end
  end

  def test_validates_endpoints_without_leaking_credentials
    %w[https://example.com/api/v1/feedback/id.secret http://localhost:8080/api/v1/feedback/id.secret http://127.0.0.1:8080/api/v1/feedback/id.secret http://[::1]:8080/api/v1/feedback/id.secret].each do |endpoint|
      Fbhook::Client.new(endpoint)
    end
    [
      "http://example.com/api/v1/feedback/id.secret", "http://localhost.example.com/api/v1/feedback/id.secret",
      "https://user:secret@example.com/api/v1/feedback/id.secret", "https://example.com/api/v1/feedback/id.secret?q=x",
      "https://example.com/api/v1/feedback/id.secret#fragment", "https://example.com/not-feedback/id.secret",
      "http://[::ffff:127.0.0.1]/api/v1/feedback/id.secret"
    ].each do |endpoint|
      error = assert_raises(Fbhook::EndpointError) { Fbhook::Client.new(endpoint) }
      refute_includes error.message, endpoint
      refute_includes error.inspect, endpoint
    end
    client = Fbhook::Client.new("https://example.com/api/v1/feedback/inspect.secret")
    refute_includes client.inspect, "inspect.secret"
  end

  def test_bounds_response_reads_and_redacts_transport_errors
    with_server do |server, _requests|
      server.on_request { |_request| [202, "x" * 4097] }
      error = assert_raises(Fbhook::ReceiptError) { Fbhook::Client.new(server.endpoint).submit({ feedback: "Oversized" }, key: "four") }
      refute_includes error.message, "x"
    end

    client = Fbhook::Client.new("https://example.invalid/api/v1/feedback/secret")
    client.define_singleton_method(:perform) { |_body, _key| raise "failed https://example.invalid/api/v1/feedback/secret" }
    error = assert_raises(Fbhook::TransportError) { client.submit({ feedback: "No URL leak" }, key: "five") }
    refute_includes error.message, "example.invalid"
    refute_includes error.inspect, "example.invalid"
    assert_nil error.cause
  end

  def test_enforces_payload_feedback_title_and_key_constraints
    client = Fbhook::Client.new("https://example.com/api/v1/feedback/secret")
    [
      [{ feedback: " \n\t" }, Fbhook::ValidationError],
      [{ feedback: "ok", title: "bad\nline" }, Fbhook::ValidationError],
      [{ feedback: "ok", title: "😀" * 201 }, Fbhook::ValidationError],
      [{ feedback: "x" * (64 * 1024) }, Fbhook::ValidationError]
    ].each do |feedback, error_class|
      assert_raises(error_class) { client.submit(feedback, key: "valid") }
    end
    ["bad\rkey", "bad\x00key", "café"].each do |key|
      assert_raises(Fbhook::ValidationError) { client.submit({ feedback: "ok" }, key: key) }
    end

    with_server do |server, requests|
      server.on_request { |_request| [202, '{"id":"space-key","status":"accepted"}'] }
      Fbhook::Client.new(server.endpoint).submit({ feedback: "ok" }, key: "event 123")
      assert_equal "event 123", requests.fetch(0).fetch("idempotency-key")
    end
  end

  def test_prepare_overlays_nonempty_values_and_copies_context
    defaults = Fbhook::Feedback.new(feedback: "default", source: { app: "app", version: "1", environment: "prod", actor: "system" }, context: { "one" => [1] })
    submitted_context = { "two" => [2] }
    prepared = Fbhook.prepare(defaults, { feedback: "submitted", source: { actor: "person" }, context: submitted_context })
    submitted_context["two"] << 3
    defaults.context["one"] << 9

    assert_equal "submitted", prepared.feedback
    assert_equal({ app: "app", version: "1", environment: "prod", actor: "person" }, prepared.source.to_h)
    assert_equal({ "two" => [2] }, prepared.context)

    source_only = Fbhook.prepare(defaults, { source: { actor: "agent" } })
    assert_equal "default", source_only.feedback
    assert_equal "agent", source_only.source.actor

    explicit_null = Fbhook.prepare(defaults, { feedback: "submitted", context: nil })
    assert explicit_null.context_provided
    assert_nil explicit_null.context
  end

  def test_rejects_cyclic_context_without_exposing_it
    cyclic = []
    cyclic << cyclic
    client = Fbhook::Client.new("https://example.com/api/v1/feedback/secret")
    error = assert_raises(Fbhook::ValidationError) { client.submit({ feedback: "cycle", context: cyclic }, key: "cycle") }
    refute_includes error.message, "cycle"
    assert_nil error.cause
  end

  class Server
    attr_reader :endpoint

    def initialize
      @tcp = TCPServer.new("127.0.0.1", 0)
      @endpoint = "http://127.0.0.1:#{@tcp.addr[1]}/api/v1/feedback/test.secret"
      @requests = []
    end

    def on_request(&handler)
      @handler = handler
      @thread = Thread.new do
        loop do
          socket = @tcp.accept
          headers = {}
          request_line = socket.gets
          while (line = socket.gets) != "\r\n"
            key, value = line.split(":", 2)
            headers[key.downcase] = value.strip
          end
          body = socket.read(headers.fetch("content-length").to_i)
          @requests << headers.merge("body" => body, "request-line" => request_line)
          status, response_body, response_headers = @handler.call(@requests.last)
          socket.write "HTTP/1.1 #{status} Test\r\n"
          socket.write "Content-Length: #{response_body.bytesize}\r\n" unless response_headers&.key?("content-length")
          socket.write "Connection: close\r\n"
          response_headers&.each { |key, value| socket.write "#{key}: #{value}\r\n" }
          socket.write "\r\n#{response_body}"
          socket.close
        end
      rescue IOError, Errno::EBADF
        nil
      end
    end

    def requests
      @requests
    end

    def close
      @tcp.close
      @thread.join
    end
  end

  def with_server
    server = Server.new
    yield server, server.requests
  ensure
    server&.close
  end
end
