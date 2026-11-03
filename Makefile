.PHONY: ui build build-example test race vet lint check fuzz vuln browser scripts

BUILD_REVISION ?=
BUILD_STATE ?= unknown
BUILD_FLAGS =
ifneq ($(strip $(BUILD_REVISION)),)
BUILD_FLAGS += -ldflags="-X main.buildRevision=$(BUILD_REVISION) -X main.buildState=$(BUILD_STATE)"
endif

ui:
	npm --prefix web/ui ci
	npm --prefix web/ui run check
	npm --prefix web/ui run build

build: ui
	mkdir -p bin
	go build $(BUILD_FLAGS) -o bin/fbhook ./cmd/fbhook

build-example:
	mkdir -p bin
	go build -o bin/submit-example ./examples/submit

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint fmt --diff
	golangci-lint run ./...

scripts:
	python3 -m unittest discover -s scripts -p 'test_*.py'

FUZZ_TIME ?= 10s
fuzz:
	GOMAXPROCS=2 go test -run '^$$' -fuzz '^FuzzParse$$' -fuzztime=$(FUZZ_TIME) -parallel=2 .

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

# Requires an installed Python Playwright + Chromium; no production auth bypass.
browser:
	FBHOOK_BROWSER=1 go test -run '^TestBrowserSmoke$$' -count=1 -v ./web

check: ui test race vet lint scripts build-example
	go build $(BUILD_FLAGS) -o bin/fbhook ./cmd/fbhook
