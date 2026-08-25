// Command uhpc is a UHP client: it drives any conformant server over HTTP.
//
// It exists to be used rather than demonstrated. Everything in this repository
// other than uhpc talks to the server by calling a handler directly, so until
// this existed nothing here had ever spoken the protocol over a socket — and
// the gaps that shipped in uhp-go were found by reading the specification
// against the routing table rather than by any code failing. A client that
// covers the surface fails to compile against a server missing an endpoint,
// which is cheaper than reading.
//
// It imports only uhp and the standard library. Nothing in it knows what
// uhp-go is, deliberately: a client that special-cases the server next door has
// stopped being evidence about the protocol.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aenawi/uhp-go/uhp"
)

const usage = `uhpc — a client for any UHP server.

Usage:
  uhpc [global flags] <command> [args]

Commands:
  discover                        the capability document (needs no credential)
  harnesses                       list configured harnesses
  harness <id>                    one harness
  models [harness-id]             the model catalogue, or one harness's models
  run <prompt>                    run a task and print the answer
  get <response-id>               read a task back
  input-items <response-id>       the input a task was created with
  cancel <response-id>            stop a running task
  delete <response-id>            forget a task, without stopping it
  sessions                        list sessions
  session <session-id>            one session
  turns <session-id>              a session's ordered history
  cancel-session <session-id>     stop whatever is running in a session
  files <session-id>              a session's artifacts
  download <container-id> <file-id>   an artifact's bytes, to stdout
  upload <path>                   store a file for later use as input
  watch <harness-id>              follow every task on a harness, live

Global flags:
  -url    server base URL (default $UHP_BASE_URL, else http://localhost:8080)
  -key    bearer token   (default $UHP_API_KEY)
  -json   print raw JSON rather than a summary

Run "uhpc <command> -h" for a command's own flags.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		// A UHP error is printed as the protocol describes it rather than as a
		// Go error string: the code is the part a caller acts on, and `param`
		// names the field to fix.
		if e, ok := uhp.AsError(err); ok {
			fmt.Fprintf(os.Stderr, "uhpc: %s (%s)\n", e.Message, e.Code)
			if e.Param != nil && *e.Param != "" {
				fmt.Fprintf(os.Stderr, "  field: %s\n", *e.Param)
			}
			for k, v := range e.Detail {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", k, v)
			}
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "uhpc: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("uhpc", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	url := global.String("url", envOr("UHP_BASE_URL", "http://localhost:8080"), "server base URL")
	key := global.String("key", os.Getenv("UHP_API_KEY"), "bearer token")
	asJSON := global.Bool("json", false, "print raw JSON")
	if err := global.Parse(args); err != nil {
		return err
	}

	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no command given")
	}

	// Interrupt cancels the context rather than killing the process, so a
	// stream being watched is closed and a task in flight is left alone. UHP is
	// explicit that a client giving up does not stop the work — "A client that
	// gives up MUST NOT assume the task stopped" — and printing that on the way
	// out is more honest than exiting silently.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cli := &cli{
		client: &uhp.Client{BaseURL: *url, APIKey: *key, UserAgent: "uhpc/" + uhp.Version},
		json:   *asJSON,
		out:    os.Stdout,
	}
	return cli.dispatch(ctx, rest[0], rest[1:])
}

func (c *cli) dispatch(ctx context.Context, command string, args []string) error {
	switch command {
	case "discover":
		return c.discover(ctx)
	case "harnesses":
		return c.harnesses(ctx)
	case "harness":
		return c.harness(ctx, args)
	case "models":
		return c.models(ctx, args)
	case "run":
		return c.runTask(ctx, args)
	case "get":
		return c.getResponse(ctx, args)
	case "input-items":
		return c.inputItems(ctx, args)
	case "cancel":
		return c.cancel(ctx, args)
	case "delete":
		return c.deleteResponse(ctx, args)
	case "sessions":
		return c.sessions(ctx, args)
	case "session":
		return c.session(ctx, args)
	case "turns":
		return c.turns(ctx, args)
	case "cancel-session":
		return c.cancelSession(ctx, args)
	case "files":
		return c.files(ctx, args)
	case "download":
		return c.download(ctx, args)
	case "upload":
		return c.upload(ctx, args)
	case "watch":
		return c.watch(ctx, args)
	case "help", "-h", "--help":
		fmt.Fprint(os.Stdout, usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
