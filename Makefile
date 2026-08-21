.PHONY: build run test vet fmt fmt-check tidy docker docker-check conformance

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

# Gate for CI: asserts what the image promises but a green build alone does not
# prove — nothing runs as uid 0, uhpd and every harness CLI actually execute as
# the runtime user, and the workspace that user was handed is one it can write.
#
# The CLI list mirrors the npm install in the Dockerfile; a CLI added there and
# not here is simply one this gate stops covering.
docker-check: docker
	@docker run --rm --entrypoint sh uhp-go:local -c '\
		[ "$$(id -u)" -ne 0 ] || { echo "image runs as root"; exit 1; }; \
		[ -x /usr/local/bin/uhpd ] || { echo "uhpd missing or not executable"; exit 1; }; \
		for cli in claude opencode; do \
			"$$cli" --version >/dev/null 2>&1 || { echo "harness CLI does not run: $$cli"; exit 1; }; \
		done; \
		touch "$$UHP_WORKSPACE/.probe" || { echo "workspace $$UHP_WORKSPACE not writable"; exit 1; }; \
	'
	@echo "image ok: non-root, uhpd and harness CLIs run, workspace writable"

# Runs the published UHP conformance suite against a locally running uhpd.
# Requires: pip install -e <harnessrouter>/protocol/conformance
conformance:
	uhp-conformance --base-url $${UHP_BASE_URL:-http://localhost:8080} \
		--api-key "$$UHP_API_KEY" --class $${UHP_CLASS:-full} --plain
