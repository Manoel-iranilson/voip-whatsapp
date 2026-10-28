package main

import (
	"net/http"
	"time"
)

// Stamped at link time with -ldflags "-X main.version=... -X main.commit=...
// -X main.buildTime=..."; the Dockerfile and the release workflow set them. A
// plain `go build` leaves the defaults, which is how a local build shows up.
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = ""
)

var startedAt = time.Now().UTC()

// handleVersion tells the operator which build is live, so a deploy can be
// confirmed without shell access. It sits behind the /api guard: /healthz is
// deliberately public and must not carry it.
func (s *server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":   version,
		"commit":    commit,
		"buildTime": buildTime,
		"startedAt": startedAt.Format(time.RFC3339),
	})
}
