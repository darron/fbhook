# frozen_string_literal: true

require "json"
require "net/http"
require "securerandom"
require "timeout"
require "uri"

module Fbhook
  VERSION = "0.1.0"
  MAX_REQUEST_BYTES = 64 * 1024
  MAX_RESPONSE_BYTES = 4096
  ATTEMPTS = 3
  CONTEXT_UNSET = Object.new.freeze

  class Error < StandardError; end
  class EndpointError < Error; end
  class ValidationError < Error; end
  class ReceiptError < Error; end
  class TransportError < Error; end

  class HTTPError < Error
    attr_reader :status

    def initialize(status)
      @status = status
      super("feedback service returned HTTP #{status}")
    end
  end

  class Source
    ATTRIBUTES = %i[app version environment actor].freeze
    attr_accessor(*ATTRIBUTES)

    def initialize(app: nil, version: nil, environment: nil, actor: nil, **_unused)
      @app = app
      @version = version
      @environment = environment
      @actor = actor
    end

    def to_h
      ATTRIBUTES.each_with_object({}) do |name, output|
        value = public_send(name)
        output[name] = value unless value.nil? || value == ""
      end
    end
  end

  class Feedback
    attr_accessor :title, :feedback, :source
    attr_reader :context
    attr_reader :context_provided

    def initialize(title: nil, feedback: nil, source: nil, context: CONTEXT_UNSET, **_unused)
      @title = title
      @feedback = feedback
      @source = Fbhook.source_from(source)
      @context_provided = !context.equal?(CONTEXT_UNSET)
      @context = @context_provided ? context : nil
    end

    def context=(value)
      @context_provided = true
      @context = value
    end

    def to_h
      output = { feedback: feedback }
      output[:title] = title unless title.nil? || title == ""
      source_hash = source.to_h
      output[:source] = source_hash unless source_hash.empty?
      output[:context] = context if context_provided
      output
    end
  end

  Receipt = Struct.new(:id, :status, keyword_init: true)

  def self.source_from(source)
    return Source.new if source.nil?
    return Source.new(**source.to_h) if source.is_a?(Source)
    return Source.new(**symbolize_keys(source)) if source.respond_to?(:to_h)

    raise ValidationError, "source must be an object"
  end

  def self.feedback_from(value)
    return value if value.is_a?(Feedback)
    return Feedback.new(**symbolize_keys(value)) if value.respond_to?(:to_h)

    raise ValidationError, "feedback must be an object"
  end

  def self.prepare(defaults, submission)
    base = feedback_from(defaults)
    overlay = feedback_from(submission)
    source = Source.new(**base.source.to_h)
    Source::ATTRIBUTES.each do |attribute|
      value = overlay.source.public_send(attribute)
      source.public_send("#{attribute}=", value) unless value.nil? || value == ""
    end
    attributes = {
      title: nonempty_or(overlay.title, base.title),
      feedback: nonempty_or(overlay.feedback, base.feedback),
      source: source
    }
    if overlay.context_provided
      attributes[:context] = deep_copy(overlay.context)
    elsif base.context_provided
      attributes[:context] = deep_copy(base.context)
    end
    Feedback.new(**attributes)
  end

  def self.nonempty_or(candidate, fallback)
    candidate.nil? || candidate == "" ? fallback : candidate
  end
  private_class_method :nonempty_or

  def self.deep_copy(value)
    return nil if value.nil?
    JSON.parse(JSON.generate(value))
  rescue JSON::GeneratorError, TypeError, ArgumentError, SystemStackError
    raise ValidationError.new("context is not valid JSON"), cause: nil
  end
  private_class_method :deep_copy

  def self.symbolize_keys(value)
    hash = value.to_h
    hash.each_with_object({}) { |(key, item), output| output[key.to_sym] = item }
  rescue NoMethodError
    raise ValidationError, "feedback must be an object"
  end
  private_class_method :symbolize_keys

  class Client
    def initialize(endpoint, open_timeout: 15, read_timeout: 15, write_timeout: 15, attempt_timeout: 15)
      @uri = validate_endpoint(endpoint)
      @timeouts = { open_timeout: open_timeout, read_timeout: read_timeout, write_timeout: write_timeout }
      @attempt_timeout = attempt_timeout
    end

    def inspect
      "#<Fbhook::Client>"
    end

    # A receipt means the feedback was accepted durably; it does not mean the
    # destination issue has been created or delivered.
    def submit(feedback, key: nil)
      body = serialize(feedback)
      key = idempotency_key(key)
      transport_failed = false

      ATTEMPTS.times do |attempt|
        result = begin
          perform(body, key)
        rescue StandardError
          transport_failed = true
          nil
        end
        if result
          status, response_body, oversized, unreadable = result
          if status == 202 && unreadable
            transport_failed = true
            if attempt == ATTEMPTS - 1
              break
            end
            sleep((attempt + 1) * 0.1)
            next
          end
          if status == 202
            return receipt_from(response_body, oversized)
          end
          retryable = status == 429 || status >= 500
          raise HTTPError.new(status) unless retryable && attempt < ATTEMPTS - 1
        elsif attempt == ATTEMPTS - 1
          break
        end
        sleep((attempt + 1) * 0.1) if attempt < ATTEMPTS - 1
      end
      raise TransportError, "feedback request failed" if transport_failed

      raise Error, "feedback request failed"
    end

    private

    def validate_endpoint(endpoint)
      uri = URI.parse(endpoint.to_s)
      valid_path = uri.path&.start_with?("/api/v1/feedback/")
      host = uri.host.to_s.downcase.sub(/\A\[(.*)\]\z/, "\\1")
      loopback = ["localhost", "127.0.0.1", "::1"].include?(host)
      valid = uri.is_a?(URI::HTTP) && !host.empty? && uri.userinfo.nil? && uri.query.nil? && uri.fragment.nil? && valid_path
      valid &&= uri.scheme == "https" || (uri.scheme == "http" && loopback)
      raise EndpointError, "invalid feedback endpoint" unless valid

      uri
    rescue StandardError
      raise EndpointError.new("invalid feedback endpoint"), cause: nil
    end

    def serialize(value)
      feedback = Fbhook.feedback_from(value)
      validate_feedback(feedback)
      if feedback.context_provided && !json_value?(feedback.context)
        raise ValidationError, "context is not valid JSON"
      end

      body = JSON.generate(feedback.to_h)
      unless body.encoding == Encoding::UTF_8 && body.valid_encoding? && body.bytesize <= MAX_REQUEST_BYTES
        raise ValidationError, "feedback exceeds request limit"
      end
      body
    rescue JSON::GeneratorError, TypeError, ArgumentError, SystemStackError
      raise ValidationError.new("context is not valid JSON"), cause: nil
    end

    def validate_feedback(feedback)
      unless feedback.feedback.is_a?(String) && !feedback.feedback.strip.empty?
        raise ValidationError, "feedback is required"
      end
      title = feedback.title
      if !title.nil? && (!title.is_a?(String) || title.each_codepoint.count > 200 || title.include?("\r") || title.include?("\n"))
        raise ValidationError, "title must be one line of at most 200 characters"
      end
      Source::ATTRIBUTES.each do |attribute|
        value = feedback.source.public_send(attribute)
        raise ValidationError, "source fields must be strings" unless value.nil? || value.is_a?(String)
      end
    end

    def json_value?(value, depth = 0, ancestors = {})
      return false if depth > 64
      case value
      when nil, true, false, String, Integer
        true
      when Float
        value.finite?
      when Array
        return false if ancestors[value.object_id]
        begin
          ancestors[value.object_id] = true
          value.all? { |item| json_value?(item, depth + 1, ancestors) }
        ensure
          ancestors.delete(value.object_id)
        end
      when Hash
        return false if ancestors[value.object_id]
        begin
          ancestors[value.object_id] = true
          value.all? { |key, item| key.is_a?(String) && json_value?(item, depth + 1, ancestors) }
        ensure
          ancestors.delete(value.object_id)
        end
      else
        false
      end
    end

    def idempotency_key(key)
      key = SecureRandom.hex(24) if key.nil? || key == ""
      valid_bytes = key.is_a?(String) && key.bytes.all? { |byte| byte.between?(32, 126) }
      unless valid_bytes && key.bytesize <= 200
        raise ValidationError, "invalid idempotency key"
      end
      key
    end

    def perform(body, key)
      request = Net::HTTP::Post.new(@uri.request_uri)
      request["Content-Type"] = "application/json"
      request["Idempotency-Key"] = key
      request.body = body
      http = Net::HTTP.new(@uri.host, @uri.port)
      http.use_ssl = @uri.scheme == "https"
      @timeouts.each { |name, value| http.public_send("#{name}=", value) if http.respond_to?("#{name}=") }
      http.max_retries = 0 if http.respond_to?(:max_retries=)
      response_body = +""
      response = nil
      response_status = nil
      bytes_read = 0
      Timeout.timeout(@attempt_timeout) do
        response = http.request(request) do |res|
          response_status = res.code.to_i
          res.read_body do |chunk|
            bytes_read += chunk.bytesize
            raise ResponseTooLarge.new(res.code.to_i) if response_body.bytesize + chunk.bytesize > MAX_RESPONSE_BYTES

            response_body << chunk
          end
        end
      end
      expected_length = response["content-length"]
      raise IOError, "incomplete response" if expected_length && bytes_read != expected_length.to_i
      [response_status, response_body, false, false]
    rescue ResponseTooLarge => error
      [error.status, "", true, false]
    rescue StandardError
      raise if response_status.nil?

      [response_status, "", false, true]
    end

    class ResponseTooLarge < StandardError
      attr_reader :status

      def initialize(status)
        @status = status
        super("response too large")
      end
    end

    def receipt_from(body, oversized)
      raise ReceiptError, "invalid acceptance receipt" if oversized
      parsed = JSON.parse(body)
      unless parsed.is_a?(Hash) && parsed["id"].is_a?(String) && !parsed["id"].empty? && parsed["status"] == "accepted"
        raise ReceiptError, "invalid acceptance receipt"
      end
      Receipt.new(id: parsed["id"], status: parsed["status"])
    rescue JSON::ParserError
      raise ReceiptError.new("invalid acceptance receipt"), cause: nil
    end
  end
end
