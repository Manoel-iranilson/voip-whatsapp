package main

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
)

// originPolicy decides which browser origins may reach the API.
//
// The default (empty spec) allows same-origin only: the bundled client is served
// from this server, and `npm run dev` reaches the API through Vite's proxy, so
// neither needs a cross-origin grant. A wildcard has to be asked for explicitly,
// because the API has no per-user authentication — with `*`, any page the
// operator happens to visit can drive this server.
type originPolicy struct {
	wildcard bool
	allowed  map[string]bool
}

func newOriginPolicy(spec string) originPolicy {
	p := originPolicy{allowed: map[string]bool{}}
	for _, o := range strings.Split(spec, ",") {
		switch o = normalizeOrigin(o); o {
		case "":
		case "*":
			p.wildcard = true
		default:
			p.allowed[o] = true
		}
	}
	return p
}

func normalizeOrigin(o string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(o), "/"))
}

// sameOrigin reports whether the Origin header points back at the host this
// request was sent to. Scheme is not comparable here (the request does not carry
// one behind a proxy), so host equality is the test.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// permits is the actual access decision, and it runs before the handler.
// CORS response headers alone are not enough: a cross-origin POST with a simple
// content type needs no preflight, so the browser would run it and only hide the
// response — the side effect still happens. Requests with no Origin at all come
// from non-browser clients (curl, scripts), which the header cannot protect
// against anyway; use -token for those.
func (p originPolicy) permits(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if p.wildcard {
		return true
	}
	if p.allowed[normalizeOrigin(origin)] {
		return true
	}
	return sameOrigin(origin, r.Host)
}

// corsHeader returns the value for Access-Control-Allow-Origin, or "" when the
// origin gets no cross-origin grant (same-origin requests need no header).
func (p originPolicy) corsHeader(origin string) string {
	if origin == "" {
		return ""
	}
	if p.wildcard {
		return "*"
	}
	if p.allowed[normalizeOrigin(origin)] {
		return origin
	}
	return ""
}

func withCORS(p originPolicy, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allow := p.corsHeader(origin); allow != "" {
			w.Header().Set("Access-Control-Allow-Origin", allow)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Client-Id, X-Auth-Token, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			if allow != "*" {
				w.Header().Add("Vary", "Origin")
			}
		} else if origin != "" {
			w.Header().Add("Vary", "Origin")
		}

		if !p.permits(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// caller is who made a request. An empty integration means a human operator —
// someone sitting at the browser client, who talks on the call. A named
// integration is another system driving WaCalls over the API: it starts and
// tracks calls but never takes the audio, so the rules written for operators
// (one live call at a time, ownership of what you start) must not apply to it.
type caller struct {
	integration string
}

func (c caller) isIntegration() bool { return c.integration != "" }

type callerCtxKey struct{}

func callerFrom(r *http.Request) caller {
	c, _ := r.Context().Value(callerCtxKey{}).(caller)
	return c
}

// apiKeys maps an integration name to its secret, parsed from repeated
// -api-key "name:secret" flags.
type apiKeys map[string]string

func (k apiKeys) match(presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	for name, secret := range k {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(secret)) == 1 {
			return name, true
		}
	}
	return "", false
}

// withAuth gates the API and records who is calling.
//
// With neither -token nor -api-key configured the server stays open and every
// request counts as an operator, which is the long-standing behaviour.
func withAuth(operatorToken string, keys apiKeys, h http.Handler) http.Handler {
	if operatorToken == "" && len(keys) == 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			h.ServeHTTP(w, r)
			return
		}
		presented := presentedCredential(r)

		var who caller
		switch name, ok := keys.match(presented); {
		case ok:
			who = caller{integration: name}
		case operatorToken != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(operatorToken)) == 1:
			// operator: zero value
		default:
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing credential"})
			return
		}

		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerCtxKey{}, who)))
	})
}

// presentedCredential pulls the token or API key out of a request. EventSource
// cannot send headers, so the query parameter has to be accepted too.
func presentedCredential(r *http.Request) string {
	if v := r.Header.Get("X-Auth-Token"); v != "" {
		return v
	}
	if v := r.Header.Get("X-API-Key"); v != "" {
		return v
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.URL.Query().Get("token")
}
