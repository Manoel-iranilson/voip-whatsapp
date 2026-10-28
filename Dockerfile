# syntax=docker/dockerfile:1

# ---------------------------------------------------------------- client
# Node 22 matches what CI builds the client with.
FROM node:22-alpine AS client

WORKDIR /src/client
COPY client/package.json client/package-lock.json ./
RUN npm ci
COPY client/ ./
RUN npm run build

# ---------------------------------------------------------------- server
# Keep this in step with the `go` directive in go.mod.
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# No cgo anywhere in this project — the SQLite driver and the MLow codec are both
# pure Go — so the result is a static binary that needs nothing from the base
# image but certificates.
ENV CGO_ENABLED=0
# .git is not in the build context, so the commit has to come in as a build arg
# (EasyPanel: Build → Build arguments, or `docker build --build-arg
# GIT_COMMIT=$(git rev-parse --short HEAD)`). The build time is always stamped,
# which on its own is enough to tell a fresh deploy from a stale one.
ARG GIT_COMMIT=unknown
RUN go build -trimpath \
      -ldflags "-s -w -X main.commit=${GIT_COMMIT} -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/wacalls-server ./cmd/server

# ---------------------------------------------------------------- runtime
FROM alpine:3.22

# ca-certificates is not optional: whatsmeow opens a WSS connection to WhatsApp
# and webhook delivery is HTTPS. Without it both fail certificate validation.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 -h /app wacalls

WORKDIR /app
COPY --from=build  /out/wacalls-server      /usr/local/bin/wacalls-server
COPY --from=client /src/client/dist         /app/client/dist

# /data holds wacalls.db — the paired WhatsApp credentials — and the recordings.
# Without a volume here every redeploy wipes both and every account has to be
# paired again by QR.
RUN mkdir -p /data && chown -R wacalls:wacalls /data /app
VOLUME ["/data"]

USER wacalls
EXPOSE 8080
EXPOSE 50000-50100/udp

# Everything is also settable as WACALLS_* (WACALLS_TOKEN, WACALLS_API_KEY, …),
# which is how a compose or Portainer stack is meant to configure it.
ENV WACALLS_ADDR=":8080" \
    WACALLS_DB="/data/wacalls.db" \
    WACALLS_STATIC="/app/client/dist" \
    WACALLS_RECORD_DIR="/data/recordings" \
    WACALLS_UDP_PORT_MIN="50000" \
    WACALLS_UDP_PORT_MAX="50100"

# WACALLS_PUBLIC_IP has no default on purpose, because a wrong value is worse
# than none. On a bridge network — which is what a container gets unless you say
# otherwise — leaving it unset makes the server advertise its private container
# address to the browser. The page loads, pairing works, the call rings, and the
# audio never connects. Set it to the host's public IP, and publish the UDP range
# above to match.

# -addr defaults to 127.0.0.1 in the binary, which inside a container listens on
# the container's own loopback and is unreachable from outside with no error to
# show for it. WACALLS_ADDR above is what makes the service reachable; overriding
# it back to a loopback address will look like a broken image.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["wacalls-server"]
