package http

import (
	"net/http"
	"strings"
)

// withRoutingErrors answers the two responses net/http's ServeMux produces on
// its own — 404 for a path no pattern claims, 405 for a path that matches only
// under another method — with the envelope Errors §1 requires of every non-2xx
// body. Both were plain text, so a client parsing `error.code` on any non-2xx
// (which the specification entitles it to do) got a JSON decode failure and
// could not tell a mistyped path from a server that had fallen over.
//
// Which of the two it is, and which methods a 405 should name, are read off
// net/http's own fallback handler rather than recomputed here. Recomputing
// means probing the routing table with other methods, and a trailing-slash
// route answers that probe yes for a path it only means to redirect — the
// hazard is turning a redirect into a 404, and the way to avoid it is not to
// hold a second opinion about routing at all.
//
// Nothing wraps the ResponseWriter, which is the other half of the design. A
// wrapper that waited for WriteHeader(404) would sit in front of every
// streaming response too, and would have to forward http.Flusher exactly right
// or SSE would stop flushing. Deciding before the route handler runs means the
// real ResponseWriter reaches it untouched.
func withRoutingErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			// A pattern names a route that matched, so `h` is this server's own
			// handler and must not be run twice or run early. Route it through
			// the mux instead: ServeMux.Handler discards the wildcard bindings
			// it matched, so serving `h` directly would leave every
			// r.PathValue empty.
			mux.ServeHTTP(w, r)
			return
		}

		// No pattern narrows `h` to one of three handlers net/http supplies
		// itself — not found, method not allowed, or a redirect to a cleaned
		// path that matched nothing. All three are pure, which is what makes
		// running one against a probe safe; no route handler ever reaches here.
		var probe routingProbe
		h.ServeHTTP(&probe, r)

		switch probe.status {
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, typeInvalidRequest, vendorCodeRouteNotFound,
				"no route on this server matches the request path")

		case http.StatusMethodNotAllowed:
			allow := probe.header.Get("Allow")
			if allow != "" {
				w.Header().Set("Allow", allow)
			}
			// The header is the half a client acts on without parsing
			// anything; `detail` is the same list for a client that only reads
			// the envelope, and it is the actionable part — a refusal that
			// does not say which method to use leaves nothing to do but guess.
			writeErrorDetail(w, http.StatusMethodNotAllowed, typeInvalidRequest,
				vendorCodeMethodNotAllowed,
				"the "+r.Method+" method is not allowed on this path",
				map[string]any{"allowed_methods": splitAllow(allow)})

		default:
			// A redirect, and the reason the status is read rather than the
			// pattern trusted: ServeMux returns an *empty* pattern alongside a
			// redirect whenever the path it cleaned to matches nothing itself
			// — an absolute-form request line with no path is the reachable
			// case. Answering that with a 404 is precisely the flattening this
			// wrapper has to avoid, so it goes back to the mux and is served
			// as the redirect it is.
			mux.ServeHTTP(w, r)
		}
	})
}

// routingProbe is a ResponseWriter that reaches no client.
//
// It exists to ask net/http's own fallback handler what it would have answered
// — and, for a 405, which methods it computed — while discarding the plain
// text body that is the whole reason this wrapper exists.
type routingProbe struct {
	header http.Header
	status int
}

func (p *routingProbe) Header() http.Header {
	if p.header == nil {
		p.header = http.Header{}
	}
	return p.header
}

// WriteHeader keeps the first status, the way a real ResponseWriter does: a
// second call is ignored rather than allowed to overwrite the answer being read.
func (p *routingProbe) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}

// Write records the implicit 200 that writing before WriteHeader means, so a
// handler that only writes a body is not mistaken for one that answered nothing.
func (p *routingProbe) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return len(b), nil
}

// splitAllow turns an Allow header back into the list it was joined from.
//
// Always a list, never a bare string: a client reading `detail.allowed_methods`
// should not have to split anything, and a one-element answer that came back as
// a string would make every client special-case it.
func splitAllow(allow string) []string {
	methods := []string{}
	for _, m := range strings.Split(allow, ",") {
		if m = strings.TrimSpace(m); m != "" {
			methods = append(methods, m)
		}
	}
	return methods
}
