.PHONY: build run test vet fmt fmt-check tidy hooks docker docker-check conformance conformance-gate

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

# Point git at the hooks this repository ships. One command per clone; there is
# no hook manager and no dependency to install, because `core.hooksPath` is the
# whole feature and this is a Go repository with no package manager to borrow.
hooks:
	@git config core.hooksPath .githooks
	@echo "git hooks enabled: $$(ls .githooks | tr '\n' ' ')"

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

# The recorded core score (docs/conformance.md), and the report the gate reads.
# Raise the floor when a run raises the score; it is the number the gate
# defends, so leaving it behind lets the score fall back to it unnoticed.
CONFORMANCE_FLOOR ?= 37
CONFORMANCE_REPORT ?= conformance-report.json

# Gate for CI: the same suite, but its result is asserted rather than read.
#
# UHP_HARNESS_ID is required and not defaulted. The suite runs real agent tasks
# and takes "the first harness listed" when it is not told which to use, so a
# gate that left it out would measure whichever harness happens to sort first
# on the machine it ran on — and the answer depends on which CLIs are installed
# there. See docs/conformance.md for which harness this repository's gate picks
# and why.
#
# The suite's exit code is not the whole verdict, which is why the report is
# read afterwards: skips exit zero, and a skip is never a pass.
conformance-gate:
	@test -n "$$UHP_HARNESS_ID" || { echo "UHP_HARNESS_ID is required: the gate must name the harness it measures"; exit 1; }
	# Removed before the run, not after it. The suite writes this file only if
	# it got far enough to have a result, so a run that fails to launch would
	# otherwise leave the previous run's report in place for the check below to
	# read — and yesterday's pass reported as today's is the exact failure this
	# gate exists to catch. CI is safe on a fresh checkout; a developer's
	# working copy is not.
	@rm -f $(CONFORMANCE_REPORT)
	-uhp-conformance --base-url $${UHP_BASE_URL:-http://localhost:8080} \
		--api-key "$$UHP_API_KEY" --class $${UHP_CLASS:-core} \
		--harness-id "$$UHP_HARNESS_ID" \
		--json $(CONFORMANCE_REPORT) --plain
	@python3 scripts/check-conformance.py $(CONFORMANCE_REPORT) $(CONFORMANCE_FLOOR)
