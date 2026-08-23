package harness

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// maxLineBytes bounds one line of harness output. Agent CLIs emit whole JSON
// events per line, and a large tool result can be megabytes, so the default
// 64 KiB scanner limit is far too small; exceeding this is reported as a
// failure rather than silently truncating the run.
const maxLineBytes = 8 * 1024 * 1024

// process runs one harness CLI as a subprocess and streams parsed output.
// It is shared by every CLIHarness: the concerns that must never be forgotten
// when a sixth harness is added — process-group isolation, prompt delivery
// that cannot be re-parsed as options, scanner error checking, guarded sends —
// live here once rather than in each adapter.
type process struct {
	binary    string
	prompt    PromptMode
	buildArgs func(RunRequest) ([]string, error)
	parseLine func(string) []RunUpdate

	mu     sync.Mutex
	cancel map[string]context.CancelFunc
}

func newProcess(binary string, prompt PromptMode, buildArgs func(RunRequest) ([]string, error), parseLine func(string) []RunUpdate) *process {
	return &process{
		binary:    binary,
		prompt:    prompt,
		buildArgs: buildArgs,
		parseLine: parseLine,
		cancel:    make(map[string]context.CancelFunc),
	}
}

// healthCheck verifies the CLI binary is on PATH and responds to --version.
func (p *process) healthCheck(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.binary, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("harness: %s health check failed: %w", p.binary, err)
	}
	return nil
}

// maxCaptureBytes bounds the output of a short-lived query such as "list your
// models". It is generous because it has to be — codex's catalogue is around
// 325 KB, since every entry embeds that model's own system prompt — and it
// exists only so that a CLI which streams without end cannot take the router's
// memory with it.
const maxCaptureBytes = 16 * 1024 * 1024

// capture runs the CLI to completion and returns its stdout.
//
// Unlike run, this is for a question with an answer rather than a task with a
// stream: nothing is parsed incrementally and stderr is not collected, because
// a query that failed has no output worth reporting — the caller falls back.
// The process group and WaitDelay are here for the same reason they are in
// run: these CLIs are wrappers that spawn node or bun, and a grandchild
// holding the stdout pipe open would otherwise outlive the timeout.
func (p *process) capture(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, p.binary, args...)
	isolateProcessGroup(cmd)
	cmd.WaitDelay = 5 * time.Second

	var out cappedBuffer
	out.limit = maxCaptureBytes
	cmd.Stdout = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("harness: %s %v: %w", p.binary, args, err)
	}
	return out.buf.String(), nil
}

// cappedBuffer accumulates up to limit bytes and silently drops the rest,
// reporting every write as accepted so the child is never handed a write error
// and killed over output nobody needed.
type cappedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.buf.Write(p)
	}
	return n, nil
}

func (p *process) run(ctx context.Context, req RunRequest) (<-chan RunUpdate, error) {
	args, err := p.buildArgs(req)
	if err != nil {
		return nil, fmt.Errorf("harness: build args for %s: %w", p.binary, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancel[req.TaskID] = cancel
	p.mu.Unlock()

	cmd := exec.CommandContext(runCtx, p.binary, args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	isolateProcessGroup(cmd)
	// Backstop: if anything still holds the pipes open after the process is
	// signalled, Wait closes them rather than blocking forever.
	cmd.WaitDelay = 5 * time.Second

	// Prompt delivery. Under PromptStdin the prompt is written to the child's
	// stdin and never appears in argv, so it cannot be re-parsed as an option
	// no matter what it contains. Under PromptArgs the CLI cannot read a
	// prompt from stdin and BuildArgs has placed it in argv itself; stdin is
	// still closed so a CLI that would otherwise wait on it does not hang.
	var stdin io.WriteCloser
	if p.prompt == PromptStdin {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("harness: stdin pipe: %w", err)
		}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("harness: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("harness: stderr pipe: %w", err)
	}

	out := make(chan RunUpdate, 16)

	if err := cmd.Start(); err != nil {
		cancel()
		close(out)
		return nil, fmt.Errorf("harness: start %s: %w", p.binary, err)
	}

	if stdin != nil {
		go func() {
			defer func() { _ = stdin.Close() }()
			_, _ = io.WriteString(stdin, req.Input)
		}()
	}

	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.cancel, req.TaskID)
			p.mu.Unlock()
			cancel()
		}()
		defer close(out)

		var stderrBuf []byte
		stderrDone := make(chan struct{})
		go func() {
			defer close(stderrDone)
			sc := bufio.NewScanner(stderr)
			// Same limit as stdout: a CLI that writes one very long diagnostic
			// line would otherwise trip the 64 KiB default and lose the very
			// message explaining a failure.
			sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
			for sc.Scan() {
				stderrBuf = append(stderrBuf, sc.Bytes()...)
				stderrBuf = append(stderrBuf, '\n')
			}
			// A scan error here cannot fail the run — stderr is not the answer,
			// and a diagnostic too long to read is no reason to discard a
			// successful one. But it must not pass for the whole of what the CLI
			// said: stderrBuf is quoted verbatim into the failure below, so an
			// unmarked truncation is a partial diagnostic presented as complete,
			// which is the same lie the stdout path refuses to tell — and it
			// would be told at the exact moment someone is reading the message
			// to find out why a run failed. Say so in the buffer instead.
			if err := sc.Err(); err != nil {
				stderrBuf = append(stderrBuf,
					fmt.Sprintf("\n[harness: stderr truncated: %v]\n", err)...)
				// And keep reading. The scanner has given up but the child has
				// not: it is still writing into a pipe with no reader, and once
				// that pipe fills it blocks forever. A blocked child never
				// exits, so it never writes stdout and never closes it — which
				// hangs the stdout scan below and cmd.Wait after it, with no
				// timeout to break the deadlock and the process leaked until
				// the caller's own context expires. Draining costs nothing and
				// is the only thing that guarantees the child can finish.
				_, _ = io.Copy(io.Discard, stderr)
			}
		}()

		// send delivers one update, giving up if the consumer has gone away.
		// The guard is the caller's ctx, not runCtx: an explicit Cancel stops
		// runCtx but leaves a live consumer that still needs the terminal
		// "cancelled" update. Without any guard a vanished reader blocks this
		// goroutine forever once the 16-slot buffer fills, leaking the
		// goroutine, both pipe FDs and the child process along with it.
		send := func(upd RunUpdate) bool {
			select {
			case out <- upd:
				return true
			case <-ctx.Done():
				return false
			}
		}

		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			for _, upd := range p.parseLine(line) {
				if !send(upd) {
					// Consumer gone. runCtx is a child of ctx, so the child
					// process is already being killed; drain and reap it so
					// nothing is left behind.
					<-stderrDone
					_ = cmd.Wait()
					return
				}
			}
		}
		// A scan error means the output was truncated. Reporting the run as
		// completed here would hand the client a partial answer labelled as a
		// whole one, which is worse than reporting the failure.
		scanErr := sc.Err()
		if scanErr != nil {
			// Nothing is reading stdout any more. The child would block
			// writing into a full pipe and cmd.Wait would never return, so
			// tear the process group down before waiting on it.
			cancel()
		}

		<-stderrDone
		waitErr := cmd.Wait()

		// Order matters: a scan error cancels runCtx above, so testing
		// runCtx.Err() first would report a truncated run as "cancelled" and
		// hide the real reason.
		switch {
		case scanErr != nil:
			send(RunUpdate{Type: UpdateFailed, Err: fmt.Errorf("harness: %s output truncated: %w", p.binary, scanErr)})
		case runCtx.Err() != nil:
			send(RunUpdate{Type: UpdateCancelled})
		case waitErr != nil:
			send(RunUpdate{Type: UpdateFailed, Err: fmt.Errorf("harness: %s exited: %w: %s", p.binary, waitErr, string(stderrBuf))})
		default:
			send(RunUpdate{Type: UpdateCompleted})
		}
	}()

	return out, nil
}

// cancelTask terminates the subprocess associated with taskID, if still running.
func (p *process) cancelTask(_ context.Context, taskID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel, ok := p.cancel[taskID]; ok {
		cancel()
		delete(p.cancel, taskID)
		return nil
	}
	return fmt.Errorf("harness: no running task %s", taskID)
}
