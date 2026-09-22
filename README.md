<div align="center">

# 📞 WaCalls (Go)

**Native WhatsApp voice calls in pure Go, straight from the browser.**
Built for native VoIP media, multi-account (multi-session) operation, and a modern browser client.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](https://react.dev)
[![whatsmeow](https://img.shields.io/badge/whatsmeow-VoIP-25D366?logo=whatsapp&logoColor=white)](https://github.com/tulir/whatsmeow)
[![pion](https://img.shields.io/badge/pion-WebRTC-FF6B6B)](https://github.com/pion/webrtc)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](#license)

[Overview](#overview) · [Architecture](#architecture) · [Quick Start](#quick-start) · [API](#api) · [Security](#security)

</div>

---

## Overview

WaCalls pairs one or more WhatsApp accounts via **QR code** and lets you **place and
receive 1:1 voice calls** from any browser on the LAN. The browser microphone is sent
as **raw 16 kHz PCM over a WebRTC data channel** to the Go server, which encodes it with
Meta's **MLow** codec and injects the media into WhatsApp's **SRTP relay** mesh — and the
reverse path brings the peer's audio back to the browser.

The entire VoIP stack runs **natively in pure Go**: the MLow voice codec, **RTP/SRTP**
packetization, **STUN**, the **WebRTC/SCTP relay** transport and the `<call>` signaling,
integrated with [**whatsmeow**](https://github.com/tulir/whatsmeow) and served to a
**React 19** client. There is **no cgo and no native DLL** — the MLow codec is a vendored
pure-Go package, so a plain `go build` produces a self-contained binary with live audio.

Multiple WhatsApp accounts can be paired and operated side by side, each with its own
pairing QR, connection status, and history. A single account can also run **several
concurrent 1:1 calls** at once — one per browser operator — routed independently by call ID.

> **Status:** stable. Outgoing and incoming 1:1 calls reach `ACTIVE` with bidirectional
> audio, and a single account can hold several of them concurrently. Sessions persist in
> `wacalls.db` (pure-Go SQLite).

---

## Architecture

```
┌──────────────────────────────────────────────────────────────────────────┐
│                          BROWSER (React client)                            │
│   mic + speaker  ·  WebRTC data channel (16 kHz PCM)  ·  HTTP + SSE         │
└───────────────────────────────┬──────────────────────────────────────────┘
                                 │  POST /api/sessions/{sid}/calls/{id}/webrtc  (SDP)
                                 │  GET  /api/events                            (SSE)
                                 ▼
┌──────────────────────────── GO SERVER (cmd/server) ────────────────────────┐
│  SessionManager   registry of accounts (client + CallManager + bridge)     │
│  Broker           SSE hub (sessions, auth, call lifecycle fan-out)          │
│  Bridge           pion WebRTC bridge (16 kHz PCM data channel ⇄ call core)  │
│                                                                            │
│  internal/wa      VoipSocket adapter over whatsmeow                        │
│  internal/voip    call · signaling · media · transport · core · wanode     │
└───────────────┬──────────────────────────────────────┬────────────────────┘
                │ <call> signaling (Signal/USync)       │ SRTP media
                ▼                                        ▼
        ┌───────────────┐                    ┌──────────────────────┐
        │  WhatsApp WS  │                    │   WhatsApp relay      │
        │  (whatsmeow)  │                    │  (SRTP over SCTP/DC)  │
        └───────────────┘                    └──────────────────────┘
```

### Layout

| Path | Responsibility |
|---|---|
| `cmd/server` | HTTP/SSE broker, session manager + store, WebRTC bridge, process lifecycle |
| `internal/wa` | `VoipSocket` — sends/receives `<call>` stanzas via whatsmeow |
| `internal/voip/core` | Domain types, constants, the `VoipSocket` interface |
| `internal/voip/wanode` | Shared WhatsApp-node and JID helpers |
| `internal/voip/media` | MLow codec (vendored pure-Go `mlow/`), RTP, SRTP, SSRC, PCM helpers, key derivation |
| `internal/voip/transport` | SCTP relay, STUN, subscription encoding |
| `internal/voip/signaling` | `<call>` stanza build/parse, call-key crypto, relay-ack parsing |
| `internal/voip/call` | `CallManager` — orchestrates a single call end to end |
| `client/` | React 19 + Vite + Tailwind v4 + shadcn/ui (dialer, call cards, sessions, history) |

---

## How a call flows

The core is `internal/voip/call.CallManager`, which drives a call end to end. Outgoing
call sequence:

```
1. POST .../calls            → CallManager.StartCall(peerJid)
                               generates a callID, builds the <call> offer, sends it

2. Browser opens WebRTC      → POST .../calls/{id}/webrtc (SDP offer)
                               the bridge answers with an SDP answer (pion)

3. Peer accepts              → events.CallAccept → HandleCallAccept
                               server receives <relay> + hop-by-hop keys

4. Relay transport           → STUN binding/allocate on WhatsApp relays
                               ICE + DTLS + SCTP DataChannel connect (pion)

5. SRTP media flowing        → state goes ACTIVE
   ├── uplink   (you → peer): browser 16 kHz PCM (data channel) → MLow encode → SRTP → relay
   └── downlink (peer → you): relay → SRTP → MLow decode → 16 kHz PCM (data channel) → browser

6. Teardown                  → DELETE .../calls/{id} or events.CallTerminate
                               CallManager.EndCall + bridge cleanup
```

Each protocol step (hop-by-hop SRTP key derivation, RTP packetization at `PT=120`/16 kHz,
STUN relay registration, relay-ack and `<call>` stanza parsing) is implemented and covered
by tests in `internal/voip` (`go test ./...`).

---

## Requirements

- **Go 1.26+**
- **Node 22+** and **npm** (only to build/run the React client)

No C compiler, cgo, or native libraries are required — the MLow codec is vendored
pure Go (`internal/voip/media/mlow`).

---

## Quick Start

```bash
# clone and enter the project
git clone <repo-url> wacalls-go
cd wacalls-go

# Go dependencies
go mod download

# React client dependencies
cd client && npm install && cd ..
```

### Run

```bash
go run ./cmd/server                      # listens on 127.0.0.1:8080; add -debug for verbose logs
go run ./cmd/server -addr :8080          # expose it on the LAN (see Security)
```

Live audio works out of the box — the MLow codec is pure Go, so a plain build
includes it. No build tags, no `CGO_ENABLED`, no DLLs.

Open `http://localhost:8080`, click **New session**, and scan the QR shown in the browser
(it is also printed in the terminal) with **WhatsApp → Linked devices**. Add more accounts
the same way and switch between them in the sidebar.

### React client in dev mode

```bash
cd client
npm run dev      # Vite on :5173, proxies /api → http://localhost:8080
```

For production, build the static client and serve it from the Go server:

```bash
cd client && npm run build && cd ..
go run ./cmd/server -static client/dist
```

### Server flags

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:8080` | HTTP listen address — use `:8080` to expose it on the LAN |
| `-db` | `wacalls.db` | SQLite session database path |
| `-static` | `client/dist` | Static client directory (optional) |
| `-allow-origin` | *(empty)* | Comma-separated browser origins allowed to call the API. Empty means same-origin only; `*` allows any |
| `-token` | *(empty)* | Shared token the **browser client** presents on `/api`. Empty disables the check |
| `-device-name` | `ORBSDR` | Name WhatsApp lists under *Linked devices*. **New pairings only** — see below |
| `-api-key` | *(empty)* | Credential for another system, as `name:secret`. Repeatable — see [API.md](./API.md) |
| `-webhook-url` | *(empty)* | POST call events here. Empty disables webhooks |
| `-webhook-secret` | *(empty)* | Signs webhook bodies with HMAC-SHA256 |
| `-record-dir` | *(empty)* | Directory for call recordings. Empty means recording is unavailable |
| `-record-all` | `false` | Record every call, including inbound ones (needs `-record-dir`) |
| `-debug` | `false` | Verbose logging (includes whatsmeow's internal log) |
| `-max-calls-per-session` | `8` | Max concurrent calls per session (`0` = unlimited) |

---

## API

All routes are session-scoped. Events stream over a single SSE channel, tagged with the
originating `sessionId`.

| Method | Route | Purpose |
|---|---|---|
| `GET` | `/api/sessions` | List accounts (id, name, jid, status, paired) |
| `POST` | `/api/sessions` | Create an account and begin QR pairing |
| `DELETE` | `/api/sessions/{sid}` | Log out and remove an account |
| `POST` | `/api/sessions/{sid}/logout` | Disconnect an account (keep it for re-pairing) |
| `POST` | `/api/sessions/{sid}/pair` | Re-pair an account (emit a fresh QR) |
| `POST` | `/api/sessions/{sid}/calls` | Start an outgoing call (`{ phone, duration_ms?, record? }`) |
| `POST` | `/api/sessions/{sid}/calls/{id}/webrtc` | Exchange the browser WebRTC SDP |
| `POST` | `/api/sessions/{sid}/calls/{id}/accept` | Accept an incoming call |
| `POST` | `/api/sessions/{sid}/calls/{id}/reject` | Reject an incoming call |
| `DELETE` | `/api/sessions/{sid}/calls/{id}` | End an active call |
| `GET` | `/api/sessions/{sid}/history` | Recent call history (up to 50 records) |
| `GET` | `/api/sessions/{sid}/calls/{id}/recording` | Download a call's stereo recording |
| `GET` | `/api/config` | Server settings the client must agree with (recording mode) |
| `GET` | `/healthz` | Liveness, unauthenticated — for container healthchecks |
| `GET` | `/api/events` | Server-sent events (sessions, auth, call lifecycle) |

---

## The name WhatsApp shows

Paired accounts appear in **WhatsApp → Linked devices** under `-device-name`
(`WACALLS_DEVICE_NAME`), which defaults to `ORBSDR`. Without it whatsmeow
registers as `whatsmeow`, the library's name rather than the product's.

**It applies to new pairings only.** The name travels in the registration
payload, never the login one, so WhatsApp stores it once when the account is
linked. Accounts that are already connected keep the name they registered under
— to change those, unlink them in WhatsApp and pair again.

## Docker

```bash
docker compose up --build
curl http://localhost:8080/healthz      # {"status":"ok"}
```

Then open `http://localhost:8080/?token=change-me`.

Every flag is also an environment variable (`WACALLS_ADDR`, `WACALLS_TOKEN`,
`WACALLS_API_KEY`, …), which is how `compose.yaml` configures it. Two things the
image gets right and a hand-written stack usually does not:

- **`-addr` must not stay at its default.** The binary listens on
  `127.0.0.1:8080`, which inside a container is the container's own loopback —
  unreachable from outside, with no error to show for it. The image sets `:8080`.
- **`/data` has to be a mounted volume.** It holds `wacalls.db`, the paired
  WhatsApp credentials, plus the recordings. Without the volume every redeploy
  wipes both and every account needs pairing again.

### Deploying it anywhere but localhost

Two requirements that fail **silently** — the page loads, pairing works, the call
rings, and only the audio breaks:

- **HTTPS, on a domain.** The client needs `getUserMedia`, which browsers only
  expose in a secure context. Over plain HTTP at an IP address the device list is
  empty and the call never gets a microphone.
- **`WACALLS_PUBLIC_IP` when behind NAT**, which every container on a bridge
  network is. Otherwise the server tells the browser to reach it at the
  container's private address. Publish `WACALLS_UDP_PORT_MIN`–`MAX` to match and
  open that range on the firewall.

[API.md](./API.md) has the step-by-step for EasyPanel and for Portainer, and the
check order that isolates a broken deployment fastest.

## Driving WaCalls from another system

Another system (a CRM, n8n, your own backend) can start and track calls over the
same HTTP API, while a human operator still takes the audio in the browser.
Give each one its own credential and point the server at a webhook:

```bash
wacalls-server -addr :8080 \
  -token "operator-token" \
  -api-key "crm:$(openssl rand -hex 24)" \
  -webhook-url "https://yourapp.example/hooks/wacalls" \
  -webhook-secret "$(openssl rand -hex 32)"
```

A call started with an API key belongs to **nobody** until an operator presses
**Pick up** in the client — an integration never carries audio, so it must not
hold the call. Pass `"operator": "<clientId>"` to reserve one for a specific
person instead.

**[API.md](./API.md)** has the full flow, the endpoints, the webhook payloads and
signature verification.

## Recording

Start the server with `-record-dir` and each recorded call produces **one stereo
16 kHz WAV file** under `<record-dir>/<sessionId>/`:

```
1761050400000-A1B2C3.wav    left channel: you · right channel: the other party
```

It plays back as a single conversation, and because the two voices stay on their
own channels they can still be separated afterwards — useful for transcription
that has to say who said what.

Outgoing calls are recorded when the request asks for it (`{"record": true}`, which
is what the **Record** toggle in the dialer sends). `-record-all` records everything
instead, including incoming calls, which carry no per-call flag. Asking for a
recording while `-record-dir` is unset is rejected with `400` rather than silently
ignored.

The dialer shows what the server actually does, which it learns from
`GET /api/config` (`{"recording": "off" | "optional" | "always"}`): the Record
button disappears when recording is unavailable, and with `-record-all` it is
rendered lit and locked, because the operator needs to know they are recording in
order to tell the other party.

History rows gain a `recording` field with the file name stem, and the file comes
back from `GET /api/sessions/{sid}/calls/{id}/recording`.

The two legs arrive on different goroutines, at different rates, starting at
different moments, so a mixer drains both on a fixed 20 ms tick and pads whichever
side came up short with silence. That shared clock is what keeps the channels on
one timeline. The recording starts at the first audio rather than at call setup, so
a call that rings for a while does not open with a stretch of silence.

There is no retention policy: recordings grow until you delete them.

## Tests

```bash
go test ./...                 # media stack: SRTP, STUN, RTP, relay-ack, codec, state
cd client && npm run build    # client type-check + production build
```

---

## Security

The API has **no per-user authentication**: anyone who can reach it and get past the
checks below can create accounts, place calls, and read history. `wacalls.db` holds
WhatsApp session credentials (secrets): **do not commit it** and keep it protected.

Three things guard it:

- **Loopback by default.** `-addr` defaults to `127.0.0.1:8080`, so a fresh run is not
  reachable from the network. Exposing it on the LAN (`-addr :8080`) is a deliberate choice.
- **Origin check.** Requests carrying a browser `Origin` that is neither same-origin nor
  listed in `-allow-origin` are rejected with `403` **before the handler runs** — CORS
  response headers alone would not help, because a cross-origin `POST` with a simple
  content type needs no preflight and its side effect would still happen. `-allow-origin '*'`
  turns this off; it is for development only.
- **Optional shared token.** With `-token <secret>`, every `/api` request must present it
  as `X-Auth-Token`, `Authorization: Bearer …`, or `?token=` (the last one for `EventSource`,
  which cannot send headers). Open the client once as `http://host:8080/?token=<secret>`;
  it stores the token and strips it from the URL.

Calls are also owned: a call belongs to the browser (`X-Client-Id`) that started or
accepted it, and attaching a media bridge, ending, or rejecting it from another client
returns `409`.

The defaults are safe for a single machine. For a shared LAN, run it with a token:

```bash
go run ./cmd/server -addr :8080 -static client/dist -token "$(openssl rand -hex 16)"
```

---

## Contributors

This project builds on the work of:

<div align="center">

<a href="https://github.com/jotadev66"><img src="https://github.com/jotadev66.png" width="72" height="72" style="border-radius:50%" alt="jotadev66"/></a>
<a href="https://github.com/jobasfernandes"><img src="https://github.com/jobasfernandes.png" width="72" height="72" style="border-radius:50%" alt="jobasfernandes"/></a>
<a href="https://github.com/edgardmessias"><img src="https://github.com/edgardmessias.png" width="72" height="72" style="border-radius:50%" alt="edgardmessias"/></a>
<a href="https://github.com/w3nder"><img src="https://github.com/w3nder.png" width="72" height="72" style="border-radius:50%" alt="w3nder"/></a>

[**@jotadev66**](https://github.com/jotadev66) · [**@jobasfernandes**](https://github.com/jobasfernandes) · [**@edgardmessias**](https://github.com/edgardmessias) · [**@w3nder**](https://github.com/w3nder)

</div>

---

## Acknowledgements

- [**whatsmeow**](https://github.com/tulir/whatsmeow) — Go WhatsApp Web protocol library
- [**pion/webrtc**](https://github.com/pion/webrtc) — pure-Go WebRTC stack (ICE + DTLS + SCTP)
- [**whatsapp-rust**](https://github.com/oxidezap/whatsapp-rust) — reference MLow codec implementation (ported to the vendored pure-Go `internal/voip/media/mlow`)
- [**zapo**](https://github.com/w3nder/zapo) — VoIP media-stack reference

---

## License

[MIT](./LICENSE)
