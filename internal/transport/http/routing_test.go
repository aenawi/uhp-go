package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// Errors §1 is unconditional — every non-2xx carries the envelope — and the two
// answers net/http's ServeMux produces on its own used to be the exceptions.
// They are reachable on every route, so a client that parses `error.code` on a
// non-2xx got a JSON decode failure for nothing worse than a typo in a path.
func TestRouterDefaultsCarryTheErrorEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantCode    string
		wantAllow   string
		wantMethods []string // detail.allowed_methods, nil for "detail is null"
	}{
		{
			name:       "no pattern matches the path",
			method:     "GET",
			path:       "/v1/nope",
			wantStatus: http.StatusNotFound,
			wantCode:   vendorCodeRouteNotFound,
		},
		{
			// The Allow header is the only part of a 405 a client acts on
			// without parsing, and `detail` repeats it for one that only reads
			// the envelope. Both come from net/http's own method match, so
			// neither can drift from what the router would actually accept.
			name:        "the path matches only under another method",
			method:      "PATCH",
			path:        "/v1/responses/resp_x",
			wantStatus:  http.StatusMethodNotAllowed,
			wantCode:    vendorCodeMethodNotAllowed,
			wantAllow:   "GET, HEAD",
			wantMethods: []string{"GET", "HEAD"},
		},
		{
			name:       "a trailing slash matches nothing",
			method:     "GET",
			path:       "/v1/harnesses/",
			wantStatus: http.StatusNotFound,
			wantCode:   vendorCodeRouteNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := do(t, newTestServer(), tc.method, tc.path, nil)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := w.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}

			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %s", w.Body.String())
			}
			if code := errorCode(t, body); code != tc.wantCode {
				t.Errorf("code = %s, want %s", code, tc.wantCode)
			}
			envelope, _ := body["error"].(map[string]any)
			if typ, _ := envelope["type"].(string); typ != typeInvalidRequest {
				t.Errorf("type = %v, want %s", envelope["type"], typeInvalidRequest)
			}
			if msg, _ := envelope["message"].(string); msg == "" {
				t.Errorf("message is empty: %v", envelope)
			}
			// Present as explicit nulls rather than omitted, so a client can
			// tell "no value" from "this server is older than the field".
			for _, field := range []string{"param", "detail"} {
				if _, ok := envelope[field]; !ok {
					t.Errorf("%s is absent, want it present: %v", field, envelope)
				}
			}
			if tc.wantMethods == nil {
				return
			}
			detail, ok := envelope["detail"].(map[string]any)
			if !ok {
				t.Fatalf("refusal carries no detail: %v", body)
			}
			var got []string
			for _, m := range detail["allowed_methods"].([]any) {
				got = append(got, m.(string))
			}
			if !reflect.DeepEqual(got, tc.wantMethods) {
				t.Errorf("allowed_methods = %v, want %v", got, tc.wantMethods)
			}
		})
	}
}

// A route that matched is routed by ServeMux itself, not by the handler
// ServeMux.Handler hands back: only ServeHTTP binds the wildcards, so serving
// the probed handler directly would leave every r.PathValue empty.
func TestRoutingWildcardsSurviveTheEnvelopeWrapper(t *testing.T) {
	mux := http.NewServeMux()
	var got string
	mux.HandleFunc("GET /thing/{id}", func(_ http.ResponseWriter, r *http.Request) {
		got = r.PathValue("id")
	})

	req := httptest.NewRequest("GET", "/thing/abc", nil)
	withRoutingErrors(mux).ServeHTTP(httptest.NewRecorder(), req)

	if got != "abc" {
		t.Fatalf("PathValue(%q) = %q, want %q", "id", got, "abc")
	}
}

// Flattening a redirect into a 404 is the hazard this wrapper has to avoid, and
// the pattern ServeMux returns is not enough on its own to avoid it: a redirect
// to a *cleaned* path comes back with an empty pattern — the same signal a
// genuine 404 gives — whenever the cleaned path matches nothing either. Reading
// the status the fallback handler produced is what tells the two apart, so both
// shapes are pinned here.
func TestRedirectsSurviveTheEnvelopeWrapper(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register string
		target   string
		wantLoc  string
	}{
		{
			// The cleaned path matches a route, so ServeMux names it and the
			// pattern alone would have been enough.
			name:     "cleaned path matches a route",
			register: "GET /thing",
			target:   "/thing/../thing",
			wantLoc:  "/thing",
		},
		{
			// The cleaned path matches nothing, so ServeMux returns its
			// redirect with an empty pattern. Reachable on the real server as
			// an absolute-form request line carrying no path at all.
			name:     "cleaned path matches nothing",
			register: "GET /thing",
			target:   "/nope/../other",
			wantLoc:  "/other",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(tc.register, func(http.ResponseWriter, *http.Request) {})

			w := httptest.NewRecorder()
			withRoutingErrors(mux).ServeHTTP(w, httptest.NewRequest("GET", tc.target, nil))

			if w.Code < 300 || w.Code > 399 {
				t.Fatalf("status = %d, want a redirect: %s", w.Code, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != tc.wantLoc {
				t.Errorf("Location = %q, want %q", loc, tc.wantLoc)
			}
		})
	}
}

// The one redirect that survives refuseDotSegments and reaches the wrapper on
// the real server: an absolute-form request line with no path. It is the live
// vector for the empty-pattern redirect above, so it is asserted end to end
// rather than only against a bare mux.
func TestEmptyPathStillRedirects(t *testing.T) {
	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.URL.Path = ""

	w := httptest.NewRecorder()
	newTestServer().Handler().ServeHTTP(w, req)

	if w.Code < 300 || w.Code > 399 {
		t.Fatalf("status = %d, want a redirect: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
}
