package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aenawi/uhp-go/uhp"
)

type cli struct {
	client *uhp.Client
	json   bool
	out    io.Writer
}

// emit prints a value as JSON when -json is set, and otherwise runs the
// summary. Every command goes through it so that -json is universal rather than
// something each command remembers to support.
func (c *cli) emit(v any, summary func()) error {
	if !c.json {
		summary()
		return nil
	}
	enc := json.NewEncoder(c.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (c *cli) printf(format string, args ...any) {
	fmt.Fprintf(c.out, format, args...)
}

func arg(args []string, n int, name string) (string, error) {
	if len(args) <= n || strings.TrimSpace(args[n]) == "" {
		return "", fmt.Errorf("this command needs a %s", name)
	}
	return args[n], nil
}

func (c *cli) discover(ctx context.Context) error {
	d, err := c.client.Discover(ctx)
	if err != nil {
		return err
	}
	return c.emit(d, func() {
		c.printf("%s %s\n", d.Protocol, d.DefaultVersion)
		c.printf("versions:    %s\n", strings.Join(d.Versions, ", "))
		c.printf("conformance: %s\n", d.ConformanceClass)
		if d.Implementation != nil {
			c.printf("server:      %s %s\n", d.Implementation.Name, d.Implementation.Version)
		}
		c.printf("\ncapabilities\n")
		// Printed as a fixed list including the false ones. A capability
		// reported false and one omitted mean the same thing, and showing only
		// what is true would make a server that predates a field look like one
		// that refused it.
		for _, row := range []struct {
			name string
			on   bool
		}{
			{"streaming", d.Capabilities.Streaming},
			{"sessions", d.Capabilities.Sessions},
			{"cancellation", d.Capabilities.Cancellation},
			{"files_input", d.Capabilities.FilesInput},
			{"files_output", d.Capabilities.FilesOutput},
			{"session_listing", d.Capabilities.SessionListing},
			{"harness_management", d.Capabilities.HarnessManagement},
			{"session_sharing", d.Capabilities.SessionSharing},
			{"idempotency", d.Capabilities.Idempotency},
		} {
			mark := "no"
			if row.on {
				mark = "yes"
			}
			c.printf("  %-20s %s\n", row.name, mark)
		}
	})
}

func (c *cli) harnesses(ctx context.Context) error {
	hs, err := c.client.ListHarnesses(ctx)
	if err != nil {
		return err
	}
	return c.emit(hs, func() {
		if len(hs) == 0 {
			// An empty list is a legitimate answer, not a failure — a server
			// with no harnesses in the caller's scope says so this way.
			c.printf("no harnesses\n")
			return
		}
		for _, h := range hs {
			c.printf("%-28s %-14s %s\n", h.ID, h.Base, h.Name)
		}
	})
}

func (c *cli) harness(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "harness id")
	if err != nil {
		return err
	}
	h, err := c.client.GetHarness(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(h, func() {
		c.printf("id:      %s\nname:    %s\nbase:    %s\n", h.ID, h.Name, h.Base)
		if h.DefaultModel != "" {
			c.printf("default: %s\n", h.DefaultModel)
		}
		if len(h.DisabledTools) > 0 {
			c.printf("blocked: %s\n", strings.Join(h.DisabledTools, ", "))
		}
		for _, s := range h.Skills {
			c.printf("skill:   %s (%d files)\n", s.Name, len(s.Files))
		}
		for _, m := range h.McpServers {
			c.printf("mcp:     %s %s\n", m.Name, m.URL)
		}
	})
}

func (c *cli) models(ctx context.Context, args []string) error {
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		hm, err := c.client.HarnessModels(ctx, args[0])
		if err != nil {
			return err
		}
		return c.emit(hm, func() { c.printModels(hm.Models) })
	}
	catalog, err := c.client.Models(ctx)
	if err != nil {
		return err
	}
	return c.emit(catalog, func() {
		for backend, entry := range catalog.Backends {
			c.printf("%s (default %s)\n", backend, entry.Default)
			c.printModels(entry.Models)
		}
	})
}

// printModels shows availability rather than hiding what is unavailable. A
// model listed as unavailable tells a user it exists and is not configured,
// which is a different problem from it not existing.
func (c *cli) printModels(models []uhp.Model) {
	for _, m := range models {
		mark := " "
		if m.Default {
			mark = "*"
		}
		state := "unavailable"
		if m.Available {
			state = "available"
		}
		c.printf("  %s %-28s %s\n", mark, m.ID, state)
	}
}

func (c *cli) runTask(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	harness := fs.String("harness", "", "harness id (omit to let the server choose)")
	model := fs.String("model", "", "model id (omit for the harness default)")
	stream := fs.Bool("stream", false, "render the answer as it arrives")
	previous := fs.String("previous", "", "continue the session of this response id")
	key := fs.String("key", "", "Idempotency-Key (generated when empty)")
	file := fs.String("file", "", "attach a file as an input_file item")
	instructions := fs.String("instructions", "", "task-specific system guidance")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		return errors.New("run needs a prompt")
	}

	req := uhp.CreateResponseRequest{Model: *model, Instructions: *instructions}
	if *harness != "" {
		req.Metadata = map[string]any{"harness_id": *harness}
	}
	if *previous != "" {
		req.PreviousResponseID = previous
	}

	// The bare-string form unless there is a file, in which case the item array
	// — both of which a server must accept.
	if *file == "" {
		req.Input = prompt
	} else {
		item, err := inputFileItem(*file)
		if err != nil {
			return err
		}
		req.Input = []any{
			map[string]any{"type": "input_text", "text": prompt},
			item,
		}
	}

	// A key is generated when the caller did not supply one, because a task
	// sent without one cannot be retried safely — Errors §4 requires retries of
	// POST /v1/responses to carry one, and a client that omits it has given up
	// the ability to recover from a timeout.
	idempotency := *key
	if idempotency == "" {
		idempotency = generateKey()
	}

	if *stream {
		return c.streamTask(ctx, req, idempotency)
	}

	resp, err := c.client.Create(ctx, req, idempotency)
	if err != nil {
		return err
	}
	return c.emit(resp, func() { c.printResponse(resp) })
}

func (c *cli) streamTask(ctx context.Context, req uhp.CreateResponseRequest, key string) error {
	s, err := c.client.Stream(ctx, req, key)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	var final *uhp.Response
	for {
		ev, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A stream that ends early has not ended the task. Saying so, and
			// how to find out what happened, is the difference between a
			// useful failure and a user assuming their work was lost.
			if final == nil {
				c.printf("\n")
				return fmt.Errorf("%w\nthe task is probably still running; read it back with: uhpc get <id>", err)
			}
			return err
		}

		switch ev.Type {
		case uhp.EventOutputTextDelta:
			c.printf("%s", ev.Delta)
		case uhp.EventReasoningSummaryTextDelta:
			// Reasoning is a summary and is optional; many harnesses produce
			// none. Shown dimly rather than mixed into the answer.
			fmt.Fprintf(os.Stderr, "%s", ev.Delta)
		case uhp.EventError:
			fmt.Fprintf(os.Stderr, "\n[%s] %s\n", ev.Code, ev.Message)
		}
		// Every other type is ignored, which is UHP's second client rule: a
		// server may add event types within a published version and a client
		// must skip the ones it does not know rather than failing.
		if ev.IsTerminal() {
			final = ev.Response
		}
	}

	c.printf("\n")
	if final == nil {
		return errors.New("the stream ended without a terminal event; read the task back with: uhpc get <id>")
	}
	c.printf("\n")
	c.printStatus(final)
	return nil
}

func (c *cli) getResponse(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "response id")
	if err != nil {
		return err
	}
	resp, err := c.client.Get(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(resp, func() { c.printResponse(resp) })
}

func (c *cli) printResponse(resp *uhp.Response) {
	if text := responseText(resp); text != "" {
		c.printf("%s\n\n", text)
	}
	c.printStatus(resp)
	for _, item := range resp.Output {
		for _, part := range item.Content {
			for _, a := range part.Annotations {
				c.printf("file:   %s (%s)\n", a.Filename, a.FileID)
			}
		}
	}
}

func (c *cli) printStatus(resp *uhp.Response) {
	c.printf("id:     %s\n", resp.ID)
	c.printf("status: %s\n", resp.Status)
	c.printf("model:  %s\n", resp.Model)
	if session, ok := resp.Metadata["session_id"].(string); ok && session != "" {
		c.printf("session: %s\n", session)
	}
	if harness, ok := resp.Metadata["harness_id"].(string); ok && harness != "" {
		c.printf("harness: %s\n", harness)
	}
	// Reported when the server ran something other than what was asked for,
	// which it must declare rather than substitute silently.
	if requested, ok := resp.Metadata["requested_model"].(string); ok && requested != "" && requested != resp.Model {
		c.printf("note:   asked for %s, ran %s\n", requested, resp.Model)
	}
	if resp.Usage != nil {
		c.printf("tokens: %d in, %d out\n", resp.Usage.InputTokens, resp.Usage.OutputTokens)
	} else {
		// null usage is an honest absence rather than a free task, and the
		// schema says so; printing zero would be the lie it warns about.
		c.printf("tokens: not reported\n")
	}
	if resp.Error != nil {
		c.printf("error:  %s (%s)\n", resp.Error.Message, resp.Error.Code)
	}
}

func responseText(resp *uhp.Response) string {
	var b strings.Builder
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func (c *cli) inputItems(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "response id")
	if err != nil {
		return err
	}
	items, err := c.client.InputItems(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(items, func() {
		for _, item := range items {
			c.printf("%s\n", item)
		}
	})
}

func (c *cli) cancel(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "response id")
	if err != nil {
		return err
	}
	resp, err := c.client.Cancel(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(resp, func() {
		// Cancellation is asynchronous: a 200 means accepted, not stopped.
		c.printf("cancel accepted; status is now %s\n", resp.Status)
	})
}

func (c *cli) deleteResponse(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "response id")
	if err != nil {
		return err
	}
	if err := c.client.Delete(ctx, id); err != nil {
		return err
	}
	// Stated every time, because this is the surprising half of the endpoint
	// and the specification requires it: deleting history does not stop work.
	c.printf("deleted %s (a run in flight is not stopped; use cancel for that)\n", id)
	return nil
}

func (c *cli) sessions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "page size")
	harness := fs.String("harness", "", "filter by harness id")
	all := fs.Bool("all", false, "follow next_cursor to the end")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var out []uhp.Session
	cursor := ""
	for {
		page, err := c.client.ListSessions(ctx, uhp.SessionFilter{
			Limit: *limit, Cursor: cursor, Harness: *harness,
		})
		if err != nil {
			return err
		}
		out = append(out, page.Sessions...)
		// Paging stops on a null cursor, never on a short page: that heuristic
		// is wrong whenever a page is exactly full, which is why the field is
		// an explicit null.
		if !*all || page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}

	return c.emit(out, func() {
		if len(out) == 0 {
			c.printf("no sessions\n")
			return
		}
		for _, s := range out {
			c.printf("%-28s %-12s %s\n", s.ID, s.Status, s.Title)
		}
	})
}

func (c *cli) session(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "session id")
	if err != nil {
		return err
	}
	s, err := c.client.GetSession(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(s, func() {
		c.printf("id:      %s\nharness: %s\nstatus:  %s\ntitle:   %s\n",
			s.ID, s.HarnessID, s.Status, s.Title)
	})
}

func (c *cli) turns(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "session id")
	if err != nil {
		return err
	}
	turns, err := c.client.SessionTurns(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(turns, func() {
		for _, raw := range turns {
			// Decoded leniently and printed from whatever landed. The OpenAPI
			// types a turn as an untyped object, so a field that is absent is
			// a conformant server rather than a broken one.
			var t uhp.Turn
			if err := json.Unmarshal(raw, &t); err != nil {
				c.printf("%s\n", raw)
				continue
			}
			c.printf("%-28s %-12s %s\n", t.ResponseID, t.Status, firstLine(t.Input))
		}
	})
}

func (c *cli) cancelSession(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "session id")
	if err != nil {
		return err
	}
	if err := c.client.CancelSession(ctx, id); err != nil {
		return err
	}
	c.printf("cancel accepted for %s (the session is not deleted)\n", id)
	return nil
}

func (c *cli) files(ctx context.Context, args []string) error {
	id, err := arg(args, 0, "session id")
	if err != nil {
		return err
	}
	files, err := c.client.SessionFiles(ctx, id)
	if err != nil {
		return err
	}
	return c.emit(files, func() {
		if len(files) == 0 {
			c.printf("no artifacts\n")
			return
		}
		for _, f := range files {
			c.printf("%-24s %-24s %8d  %s\n", f.ContainerID, f.ID, f.Bytes, f.Filename)
		}
	})
}

func (c *cli) download(ctx context.Context, args []string) error {
	container, err := arg(args, 0, "container id")
	if err != nil {
		return err
	}
	file, err := arg(args, 1, "file id")
	if err != nil {
		return err
	}
	body, err := c.client.Download(ctx, container, file)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	_, err = io.Copy(os.Stdout, body)
	return err
}

func (c *cli) upload(ctx context.Context, args []string) error {
	path, err := arg(args, 0, "file path")
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	file, err := c.client.Upload(ctx, filepath.Base(path), f)
	if err != nil {
		return err
	}
	return c.emit(file, func() {
		c.printf("%s  %s  %d bytes\n", file.ID, file.Filename, file.Bytes)
	})
}

func (c *cli) watch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	from := fs.Int("from", 0, "sequence number to start at")
	reconnect := fs.Bool("reconnect", true, "reopen the feed if it drops, resuming where it stopped")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id, err := arg(fs.Args(), 0, "harness id")
	if err != nil {
		return err
	}

	at := *from
	for {
		next, err := c.watchOnce(ctx, id, at)
		at = next
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			// A feed has no terminal event, so a clean end is the server
			// closing it rather than the work finishing.
			return nil
		}
		if !*reconnect {
			return err
		}
		fmt.Fprintf(os.Stderr, "\n[feed dropped at %d: %v — reconnecting]\n", at, err)
		// Backing off at all matters more than the number: a feed that refuses
		// every reconnection would otherwise be retried as fast as the network
		// allows.
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil
		}
	}
}

// watchOnce reads a feed until it ends, returning where a reconnection should
// resume — which is one past the last event actually delivered, never the
// starting point, or a reconnection would replay what was already shown.
func (c *cli) watchOnce(ctx context.Context, harnessID string, from int) (int, error) {
	s, err := c.client.StreamHarness(ctx, harnessID, from)
	if err != nil {
		return from, err
	}
	defer func() { _ = s.Close() }()

	for {
		ev, err := s.Next()
		if errors.Is(err, io.EOF) {
			return s.ResumeFrom(), nil
		}
		if err != nil {
			return s.ResumeFrom(), err
		}
		switch ev.Type {
		case uhp.EventOutputTextDelta:
			c.printf("%s", ev.Delta)
		case uhp.EventResponseCreated:
			c.printf("\n[%d] task started\n", ev.SequenceNumber)
		case uhp.EventError:
			fmt.Fprintf(os.Stderr, "\n[%s] %s\n", ev.Code, ev.Message)
		default:
			if ev.IsTerminal() {
				status := "?"
				if ev.Response != nil {
					status = string(ev.Response.Status)
				}
				c.printf("\n[%d] task %s\n", ev.SequenceNumber, status)
			}
		}
	}
}

// inputFileItem reads a file into the inline `input_file` form.
//
// Inline rather than an upload, because it is one request rather than two and
// this is the form every conformant server accepts. Anything large belongs in
// `uhpc upload` instead: a data URL is re-sent on every retry.
func inputFileItem(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	mediaType := mime.TypeByExtension(filepath.Ext(path))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return map[string]any{
		"type":      "input_file",
		"filename":  filepath.Base(path),
		"file_data": "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data),
	}, nil
}

// generateKey mints an idempotency key.
//
// crypto/rand rather than a timestamp: two uhpc processes started in the same
// millisecond must not collide onto one key, or the second would be answered
// with the first one's task.
func generateKey() string {
	return "uhpc-" + randomHex(16)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
