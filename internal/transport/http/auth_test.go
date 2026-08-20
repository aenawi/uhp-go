package http

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/service"
	"github.com/aenawi/uhp-go/internal/store"

	"github.com/aenawi/uhp-go/internal/harness"
)

func newAuthServer(keys []string, maxBody int64) *Server {
	reg := harness.NewRegistry()
	reg.Register(echoAdapter{})
	svc := service.NewTaskService(reg, store.NewMemoryStore(), slog.Default())
	return NewServer(svc, slog.Default(), keys, maxBody)
}

// RFC 7235 makes the auth scheme case-insensitive, so a conformant client
// sending "bearer" must be accepted.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	srv := newAuthServer([]string{"k1"}, 0)
	for _, header := range []string{"Bearer k1", "bearer k1", "BEARER k1", "BeArEr k1"} {
		req := httptest.NewRequest("GET", "/v1/harnesses", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("Authorization: %q rejected with %d", header, w.Code)
		}
	}
}

func TestAuthRejections(t *testing.T) {
	srv := newAuthServer([]string{"k1"}, 0)
	cases := []struct {
		name, header, wantCode string
		status                 int
	}{
		{"no header", "", "missing_credential", 401},
		{"wrong scheme", "Basic k1", "missing_credential", 401},
		{"empty token", "Bearer ", "missing_credential", 401},
		{"unknown key", "Bearer nope", "invalid_credential", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/harnesses", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if !strings.Contains(w.Body.String(), tc.wantCode) {
				t.Errorf("body %s does not carry code %q", w.Body.String(), tc.wantCode)
			}
			if !strings.Contains(w.Body.String(), "authentication_error") {
				t.Errorf("error envelope is missing type authentication_error: %s", w.Body.String())
			}
		})
	}
}

// Discovery must be reachable without a credential: a client has to be able to
// tell this is a UHP server before deciding what to present.
func TestDiscoveryNeedsNoCredential(t *testing.T) {
	srv := newAuthServer([]string{"k1"}, 0)
	req := httptest.NewRequest("GET", "/v1/uhp", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET /v1/uhp without a token returned %d", w.Code)
	}
	if got := w.Header().Get("UHP-Version"); got != UHPVersion {
		t.Errorf("UHP-Version = %q, want %q", got, UHPVersion)
	}
}

// A version the server cannot serve must be refused, never silently
// substituted.
func TestUnsupportedVersionIsRefused(t *testing.T) {
	srv := newAuthServer(nil, 0)
	req := httptest.NewRequest("GET", "/v1/uhp", nil)
	req.Header.Set("UHP-Version", "1999-01-01")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "unsupported_protocol_version") {
		t.Errorf("missing code: %s", body)
	}
	if !strings.Contains(body, "supported") || !strings.Contains(body, UHPVersion) {
		t.Errorf("error detail must list the supported versions: %s", body)
	}
}

func TestSupportedVersionIsHonoured(t *testing.T) {
	srv := newAuthServer(nil, 0)
	req := httptest.NewRequest("GET", "/v1/uhp", nil)
	req.Header.Set("UHP-Version", UHPVersion)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("UHP-Version") != UHPVersion {
		t.Fatalf("status=%d version=%q", w.Code, w.Header().Get("UHP-Version"))
	}
}

// An unbounded body is a trivial unauthenticated memory exhaustion, since auth
// is disabled by default.
func TestOversizedBodyIsRefused(t *testing.T) {
	srv := newAuthServer(nil, 1024)
	big := `{"input":"` + strings.Repeat("a", 4096) + `","metadata":{"harness_id":"echo"}}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(big))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != 413 {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "max_bytes") {
		t.Errorf("413 must state the limit in detail.max_bytes: %s", w.Body.String())
	}
}
