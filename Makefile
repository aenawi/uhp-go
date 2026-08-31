.PHONY: build run test vet fmt fmt-check tidy hooks docker docker-check conformance conformance-gate capture-claude probe-claude-delivery probe-pi probe-codex probe-grok probe-steps probe-pi-steps probe-grok-max-turns probes

# Both binaries, because a server nobody can call is half a delivery: uhpc is
# how the surface gets exercised over a socket rather than against a handler.
build:
	go build -o bin/uhpd ./cmd/uhpd
	go build -o bin/uhpc ./cmd/uhpc

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
#
# The floor and UHP_CLASS move together, and this is the one place that says so.
# The gate below defaults to `core`, which is 40 checks since harnessrouter#46
# added T-08/T-09/T-10. Full is 62: 40 core, 8 extended, 14 full, the last of
# those grown by the seven R checks of harnessrouter#45. Point UHP_CLASS at a
# higher class without raising this and the gate keeps defending 40 while
# reporting on 62, which is a gate that cannot fail.
#
#   UHP_CLASS=full  →  CONFORMANCE_FLOOR=62
#
# Both denominators moved under a floor that did not, which is the failure this
# comment now exists to prevent. The suite grew by ten checks upstream and the
# floors here kept the old counts, so `37` went on defending a class of 37 that
# no longer existed. A floor is a number about the suite, not only about this
# server: re-read it when the suite moves, not only when the score does.
#
# Do not set a floor of 45 for `extended`. X-06 and X-07 only test something if
# the agent writes a file, and whether it does is the model's choice on the day:
# the 2026-08-23 extended run skipped X-07 and the full run passed it, same
# server, ninety seconds apart. check-conformance.py refuses skips outright,
# which is the right answer — but it means `extended` is not a class to gate on.
#
# This gate was red at 36/37 and is now green: 2026-08-24 scored 37/37 with no
# skips, T-03 reading `claude-opus-5[1m]` off a task that named no model (#43).
# It went green because the bug was fixed, not because the floor moved — the
# proposal at the time was to lower this to 36, which would have retired the
# finding instead of the defect. The number is a floor, not a description.
#
# Keep this gate the one place the suite runs without `--model`. That is the
# configuration that found #43 while three pinned runs walked straight past it,
# and it stops being worth anything the moment someone adds `--model` here to
# make a run reproducible.
CONFORMANCE_FLOOR ?= 40
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
#
# UHP_CLASS=full needs the *server* started with UHP_SESSION_SHARING=1, and with
# UHP_WORKSPACE and a harness store. The capability is off by default, so a
# full-class gate against a server without it is measuring one that reports
# `session_sharing: false` — the same shape of mistake as #43, where the
# configuration that exercises the feature was the one nothing ran. Nothing here
# can check that: this target points at a server it did not start.
#
# What it is no longer invisible in is the document. Since #65 the server
# computes `conformance_class` from those same capabilities, so a server
# misconfigured for a full-class run says so in `GET /v1/uhp` — it answers
# `core` or `extended`, not `full` — instead of claiming a class it cannot
# defend. Read the class off discovery before trusting a UHP_CLASS=full score.
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

# Probe for a maintainer's machine: run the Claude Code invocation uhpd ships
# against a logged-in CLI and check the stream against what parseClaudeLine
# assumes. `go test` cannot do this — it has no logged-in CLI — and that gap is
# how the delta shape came to be read out of the binary instead of off the wire
# (#32). Run it after every Claude Code upgrade.
#
# Needs a logged-in `claude` and nothing else — nested inside a Claude Code
# session is fine. An earlier note here said otherwise; the "Not logged in" that
# claim was built on came from `--bare` in the invocation discarding OAuth, not
# from the nesting. See the comment in the probe.
CLAUDE_CAPTURE ?= claude-capture.jsonl

capture-claude:
	@python3 scripts/capture-claude-stream.py --out $(CLAUDE_CAPTURE)

# The second Claude Code probe, and the other half of what `go test` cannot
# reach: `capture-claude` checks what the CLI says back, this checks what it
# does with the configuration uhpd hands it (#19). Five runs against a logged-in
# CLI — a tool block with its control, an MCP server that must be reached, one
# that must not be, and the control proving the isolation is this invocation's
# doing. It starts its own MCP endpoints on loopback and needs no network.
#
# Run it after every Claude Code upgrade, alongside `capture-claude`.
probe-claude-delivery:
	@python3 scripts/probe-claude-delivery.py

# The pi probe (#33), and the one that costs nothing to run: pi's models.json
# can declare a provider outright, base URL included, so this answers from a
# loopback server of its own and needs no credentials, no network and no
# logged-in CLI — only `pi` on PATH.
#
# It checks the two things pi.go used to declare rather than observe — that the
# answer arrives as streamed `message_update`/`text_delta` events, and that
# `--session-id` really resumes — plus the exit-0-on-failure claim
# harnessFailure rests on. Run it after every pi upgrade.
#
# Nothing it does touches the machine's own pi: PI_CODING_AGENT_DIR points at a
# temporary directory for the run.
probe-pi:
	@python3 scripts/probe-pi-session.py

# The codex and grok probes (#34), and the two that cost real money: neither
# CLI takes a per-run base URL, so unlike `probe-pi` they cannot answer from a
# loopback provider and must be run against the maintainer's logged-in account.
# The prompts are one sentence each for that reason.
#
# They are the renewal for claims that never expired on paper. Nothing in
# codex.go or grok.go was ever marked UNVERIFIED — every claim said "verified by
# execution" and none said against what — and #13 is why that is not the same
# thing: two of opencode's execution-verified claims were true when written and
# false one minor version later, with nothing in the tests to notice.
#
# `probe-codex` runs seven checks against codex-cli: the four the issue asks for
# (stdin delivery, argv injectability, what `--` does, session discovery and
# resume) plus the two that turned out to matter more — two agent messages per
# run with no separator, and a failure whose reason is on stdout — and the
# `--skip-git-repo-check` claim the router cannot do without.
#
# `probe-grok` runs six against grok: the same four, plus the streaming format
# and the failure envelope, neither of which the adapter had before 1.0.5. Its
# resume evidence is `grok export <id>` rather than the model's answer, because
# a control run in the same directory as the probe's own captures will find the
# answer by reading them off disk.
#
# Run both after every codex or grok upgrade.
probe-codex:
	@python3 scripts/probe-codex-session.py

probe-grok:
	@python3 scripts/probe-grok-session.py

# The step probe (#72), and the one that answers a question rather than renewing
# an answer. `max_step` is a budget on agent steps, and ADR-0007's rule is that a
# bound holds on every base or is not claimed at all — so what this establishes,
# per base, is whether every tool call a run takes is one the CLI narrates. A
# counter fed by a partial narration is a ceiling that never fires.
#
# Ground truth is on disk, never in the stream: each base is asked for five
# files, one tool call each, and its narration has to match what it produced.
# Under-counting fails the run; over-counting warns, because stopping early is
# survivable and stopping never is not.
#
# Re-run it whenever a base is upgraded, and whenever one is added — a base
# nobody has probed cannot be counted, and a base that cannot be counted takes
# `max_step` down with it.
probe-steps:
	@python3 scripts/probe-steps.py

# pi's half of the step question, and the one that needed a probe of its own
# (#91). probe-steps asks a real model for five files; it could not ask pi,
# because pi routes through whichever provider the machine is logged in to and
# the only one reachable capped at 8,000 tokens per minute against a 71,166-token
# request. That is a fact about an API key, and it blocked #72 for a week.
#
# This asks the same question with the key removed: pi's models.json can declare
# a provider outright, so the run answers from a loopback OpenAI-compatible
# server that returns five tool calls and then an answer. The same trick
# probe-pi-session.py uses, for the same reason — what is measured is pi's own
# layer, above whatever generated the tokens — and ground truth is on disk
# either way: pi runs its own `write` tool five times and five files appear.
#
# What it does not show is stated in the probe: a model did not choose to call
# those tools, the provider did. What a counter reads is what pi narrates about
# the calls it executes, and that is exactly what this measures.
#
# Costs nothing and needs no login, so unlike the four below it can be re-run
# freely — do, after every pi upgrade.
probe-pi-steps:
	@python3 scripts/probe-pi-steps.py

# The other half of the step-budget question, and the half only grok owes (#90).
# grok is the one base uhpd would not count, because it bounds its own steps with
# `--max-turns` — and an exemption on that basis holds only if a run the flag
# stopped is distinguishable from one that finished. If it is not, a truncated
# run reaches the client as `completed` and the router cannot repair it, having
# neither done the stopping nor anything to relabel.
#
# Ground truth is on disk here too: the task is chained so it cannot be collapsed
# into one turn, and fewer files than it asked for is what makes the run a
# truncation rather than an early finish. It spends real tokens — grok takes no
# per-run base URL — which is why the ceiling is one turn.
#
# Re-run it after every grok upgrade. A release that collapsed the stop into the
# success subtype would take grok's exemption from #72 down with it.
probe-grok-max-turns:
	@python3 scripts/probe-grok-max-turns.py

# Every probe that needs a logged-in CLI, in one command. Not part of `test`:
# `go test` has no logged-in CLI, which is the whole gap these fill.
probes: capture-claude probe-claude-delivery probe-pi probe-pi-steps probe-codex probe-grok probe-steps probe-grok-max-turns
