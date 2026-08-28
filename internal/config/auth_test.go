package config

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// stubResolver replaces the machine's resolver for the length of a test, so
// what is measured is what this check does with an answer rather than what
// this machine's /etc/hosts happens to say.
func stubResolver(t *testing.T, answers map[string][]string) {
	t.Helper()
	previous := lookupHost
	t.Cleanup(func() { lookupHost = previous })
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		addrs, ok := answers[strings.ToLower(host)]
		if !ok {
			return nil, errors.New("no such host")
		}
		return addrs, nil
	}
}

// refuseResolver answers nothing, and asserts nothing asked. A literal address
// must be decided without a lookup: that is every address an operator is likely
// to type, and none of them is the resolver's business.
func refuseResolver(t *testing.T) {
	t.Helper()
	previous := lookupHost
	t.Cleanup(func() { lookupHost = previous })
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		t.Errorf("resolved %q, which is a literal address", host)
		return nil, errors.New("should not have been called")
	}
}

// captureConfig builds a Config with only the two fields the posture check
// reads, alongside a logger whose output the test can inspect.
func captureConfig(addr string, keys []string) (Config, *slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return Config{Addr: addr, APIKeys: keys},
		slog.New(slog.NewJSONHandler(&buf, nil)),
		&buf
}

// An unauthenticated server reachable from off the machine is an open relay:
// every endpoint the specification requires a credential for answers anyone who
// can reach the port. That is a deployment mistake, and boot is the only place
// early enough to say so — every per-request answer is already too late.
func TestUnauthenticatedNonLoopbackAddressIsRefused(t *testing.T) {
	for _, addr := range []string{
		":8080",             // every interface, which is what the bare port means
		"0.0.0.0:8080",      // the same thing said out loud
		"[::]:8080",         // and its v6 spelling
		"192.168.1.10:8080", // a LAN address
		"uhp.example.com:8080",
		"", // net/http reads this as ":http", so it is every interface too
	} {
		stubResolver(t, map[string][]string{"uhp.example.com": {"93.184.216.34"}})
		cfg, log, _ := captureConfig(addr, nil)
		err := cfg.CheckAuthPosture(log)
		if err == nil {
			t.Errorf("addr %q with no keys was accepted", addr)
			continue
		}
		// The operator has to be told which variable fixes it; an error that
		// only says "refused" sends them to the source.
		if !strings.Contains(err.Error(), "UHP_API_KEYS") {
			t.Errorf("addr %q: error does not name UHP_API_KEYS: %v", addr, err)
		}
	}
}

// The local tool this server is also meant to be keeps working with no
// configuration at all — and says in the log that it is open, so anyone who
// arrived here by accident finds it there rather than in a scan.
func TestUnauthenticatedLoopbackStartsAndWarns(t *testing.T) {
	refuseResolver(t)
	for _, addr := range []string{
		"127.0.0.1:8080",
		"127.0.0.2:9000", // the whole 127/8 block is loopback
		"[::1]:8080",
	} {
		cfg, log, buf := captureConfig(addr, nil)
		if err := cfg.CheckAuthPosture(log); err != nil {
			t.Errorf("addr %q with no keys was refused: %v", addr, err)
			continue
		}
		out := buf.String()
		if !strings.Contains(out, `"level":"WARN"`) {
			t.Errorf("addr %q logged no warning: %s", addr, out)
		}
		if !strings.Contains(out, "UHP_API_KEYS") {
			t.Errorf("addr %q: warning does not name UHP_API_KEYS: %s", addr, out)
		}
	}
}

// With keys configured the address is nobody's business but the operator's,
// and there is nothing to warn about.
func TestConfiguredKeysAllowAnyAddressAndWarnAboutNothing(t *testing.T) {
	refuseResolver(t)
	for _, addr := range []string{":8080", "0.0.0.0:8080"} {
		cfg, log, buf := captureConfig(addr, []string{"devkey"})
		if err := cfg.CheckAuthPosture(log); err != nil {
			t.Errorf("addr %q with keys was refused: %v", addr, err)
		}
		if out := buf.String(); out != "" {
			t.Errorf("addr %q with keys logged %s", addr, out)
		}
	}
}

// A second key is a second credential, not a second tenant, and the plural
// variable name invites the opposite reading. The log line is the only place
// that catches an operator at the moment they made the assumption — a README
// paragraph only catches the ones who go back and read it.
func TestSeveralKeysSayTheyAreOnePrincipal(t *testing.T) {
	refuseResolver(t)
	cfg, log, buf := captureConfig("0.0.0.0:8080", []string{"alice", "bob", "carol"})
	if err := cfg.CheckAuthPosture(log); err != nil {
		t.Fatalf("three keys were refused: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"level":"INFO"`) {
		t.Fatalf("three keys logged nothing: %s", out)
	}
	// "principal" is the word CONTEXT.md defines and the word the operator has
	// to be able to search for; "tenant" is the word they were thinking of.
	for _, want := range []string{"principal", "tenant"} {
		if !strings.Contains(out, want) {
			t.Errorf("the several-keys line does not say %q: %s", want, out)
		}
	}
	// No key is a credential to be found in a log, however configured it is.
	for _, key := range []string{"alice", "bob", "carol"} {
		if strings.Contains(out, key) {
			t.Errorf("the several-keys line logged the key %q: %s", key, out)
		}
	}
}

// The one silent failure this change could otherwise cause: a keyed server that
// was reachable before the default narrowed, and is now bound where nothing can
// reach it. The keys check passes without ever looking at the address, so the
// address has to speak for itself.
func TestKeyedLoopbackSaysItIsUnreachable(t *testing.T) {
	refuseResolver(t)
	cfg, log, buf := captureConfig("127.0.0.1:8080", []string{"devkey"})
	if err := cfg.CheckAuthPosture(log); err != nil {
		t.Fatalf("keyed loopback was refused: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"level":"INFO"`) || !strings.Contains(out, "UHP_ADDR") {
		t.Errorf("keyed loopback said nothing about being unreachable: %s", out)
	}
}

// `localhost` is conventionally loopback and is ultimately whatever the
// resolver says. An unkeyed server on a `localhost` somebody has pointed
// elsewhere is the open server this check exists to refuse — and SECURITY.md
// puts exactly that bind in scope, so the check has to earn the claim rather
// than assume the name.
func TestHostnamesAreResolvedRatherThanAssumed(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		answers map[string][]string
		want    bool
	}{
		{"localhost as everyone means it", "localhost:8080",
			map[string][]string{"localhost": {"127.0.0.1", "::1"}}, true},
		{"and however it is spelled", "LOCALHOST:8080",
			map[string][]string{"localhost": {"127.0.0.1"}}, true},
		{"localhost pointed somewhere else", "localhost:8080",
			map[string][]string{"localhost": {"10.0.0.5"}}, false},
		{"loopback on one address and reachable on another", "localhost:8080",
			map[string][]string{"localhost": {"127.0.0.1", "10.0.0.5"}}, false},
		{"a name that is genuinely loopback", "dev.internal:8080",
			map[string][]string{"dev.internal": {"127.0.0.1"}}, true},
		{"a name that is not", "uhp.example.com:8080",
			map[string][]string{"uhp.example.com": {"93.184.216.34"}}, false},
		{"a name nothing answers for", "nowhere.invalid:8080", nil, false},
		{"a name that resolves to nothing", "empty.invalid:8080",
			map[string][]string{"empty.invalid": {}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubResolver(t, tc.answers)
			if got := isLoopback(tc.addr); got != tc.want {
				t.Fatalf("isLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// The default configuration has to be the one that runs, or "no configuration
// needed" and "conformant by default" cannot both be kept: the bare binary is
// a local tool, and widening it is the operator's decision to make explicitly.
func TestDefaultAddressIsLoopback(t *testing.T) {
	t.Setenv("UHP_ADDR", "")
	t.Setenv("UHP_API_KEYS", "")
	cfg := Load()
	if !isLoopback(cfg.Addr) {
		t.Fatalf("default UHP_ADDR %q is not loopback", cfg.Addr)
	}
	if err := cfg.CheckAuthPosture(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))); err != nil {
		t.Fatalf("the default configuration refuses to start: %v", err)
	}
}
