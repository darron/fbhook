# frozen_string_literal: true

require_relative "lib/fbhook"

Gem::Specification.new do |spec|
  spec.name = "fbhook"
  spec.version = Fbhook::VERSION
  spec.authors = ["Darron Froese"]
  spec.summary = "Safe Ruby client for fbhook feedback intake"
  spec.description = "Submits feedback to fbhook's HTTP intake API with bounded retries and redacted errors."
  spec.homepage = "https://github.com/darron/fbhook"
  spec.license = "MIT"
  spec.required_ruby_version = ">= 3.3"
  spec.metadata = { "source_code_uri" => "https://github.com/darron/fbhook" }
  spec.files = Dir["lib/**/*.rb", "examples/**/*.rb"] + %w[LICENSE README.md fbhook.gemspec]
  spec.require_paths = ["lib"]
end
