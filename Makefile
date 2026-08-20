.PHONY: build run test vet fmt fmt-check tidy docker conformance

build:
	go build -o bin/uhpd ./cmd/uhpd

run: build
	./bin/uhpd

test:
	go test ./... -race -cover

vet:
	go vet ./...

# Rewrites files in place.
fmt:
	gofmt -w .

# Gate for CI: lists offending files and exits non-zero if there are any.
fmt-check:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needs to run on:"; echo "$$out"; exit 1; \
	fi

tidy:
	go mod tidy

docker:
	docker build -t uhp-go:local .

# Runs the published UHP conformance suite against a locally running uhpd.
# Requires: pip install -e <harnessrouter>/protocol/conformance
conformance:
	uhp-conformance --base-url $${UHP_BASE_URL:-http://localhost:8080} \
		--api-key "$$UHP_API_KEY" --class $${UHP_CLASS:-full} --plain
