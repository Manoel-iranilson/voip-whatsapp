# WaCalls API

Complete reference for the HTTP API, and how another system drives it.

- [Authentication](#authentication) — who presents what
- [Driving WaCalls from another system](#driving-wacalls-from-another-system) — the integration flow
- [Endpoints](#endpoints) — all 14 routes
- [Webhooks](#webhooks) — events, signature, verification
- [Deploying](#deploying) — Docker
- [What this API does not do](#what-this-api-does-not-do)

Every route lives under `/api` and answers JSON, except `GET /healthz` (public)
and the recording download (`audio/wav`).

---

## Authentication

Two kinds of caller, and the difference matters:

| | Presents | Is |
|---|---|---|
| **Operator** | `-token` | A human at the browser client. Talks on the call |
| **Integration** | an `-api-key` secret | Another system. Starts and tracks calls, never takes audio |

```bash
wacalls-server \
  -addr :8080 \
  -token "operator-token" \
  -api-key "crm:$(openssl rand -hex 24)" \
  -api-key "n8n:$(openssl rand -hex 24)"
```

The credential goes in any of these, checked in this order:

```
X-Auth-Token: <secret>
X-API-Key: <secret>
Authorization: Bearer <secret>
?token=<secret>          ← for EventSource, which cannot send headers
```

With **neither** `-token` nor `-api-key` set the API is open and every caller
counts as an operator. That is the local-development default; do not run it that
way on a reachable address.

An integration is **not** held to the one-call-at-a-time rule that applies to
operators, and the calls it starts belong to nobody until an operator claims one.

---

## Driving WaCalls from another system

```
  your system                WaCalls                  browser (operator)
       │                        │                             │
       │ POST /calls            │                             │
       ├───────────────────────▶│  call starts, unowned       │
       │   { callId }           │                             │
       │◀───────────────────────┤                             │
       │                        │──── SSE: call-list ────────▶│  "Pick up"
       │                        │                             │
       │                        │◀─── POST .../webrtc ────────┤  claims the call
       │                        │                             │
       │◀── webhook call.active ┤                             │  audio flowing
       │◀── webhook call.ended  ┤                             │
```

The call your system starts belongs to nobody until a browser picks it up. That
is deliberate: an integration never carries audio, so it must not hold the call.

To send one to a specific operator, name them — then only that browser can claim
it:

```json
{ "phone": "5511999999999", "operator": "<the operator's clientId>" }
```

The operator's `clientId` is what their browser sends as `X-Client-Id`; it is
generated on first load and kept in `localStorage`.

---

## Endpoints

### Sessions

A session is one paired WhatsApp account.

#### `GET /api/sessions`

```json
{
  "sessions": [
    {
      "id": "8bd4016abd8bab6604795b79d8c2059d",
      "name": "Support line",
      "jid": "5511888888888:12@s.whatsapp.net",
      "state": "open",
      "paired": true
    }
  ]
}
```

`state` is one of `connecting`, `qr`, `open`, `logged_out`. Only a session with
`"paired": true` can place calls.

#### `POST /api/sessions`

Creates an account and starts QR pairing. The QR arrives over
[`GET /api/events`](#get-apievents) as `session-qr`, and is also printed to the
server's stdout.

```json
{ "name": "Support line" }
```

→ `200` `{ "id": "8bd4016abd8bab66..." }` · `500` if pairing could not start.

An omitted or blank `name` becomes `"Session"`.

#### `DELETE /api/sessions/{sid}`

Logs the account out of WhatsApp and removes it. → `204` · `404`

#### `POST /api/sessions/{sid}/logout`

Disconnects the account but keeps the session so it can be paired again. → `204` · `404`

#### `POST /api/sessions/{sid}/pair`

Starts a fresh QR for a session that is not paired. → `204` · `400` if it already is · `404`

---

### Calls

#### `POST /api/sessions/{sid}/calls`

Starts an outgoing call.

```json
{
  "phone": "5511999999999",
  "record": true,
  "duration_ms": 600000,
  "operator": "op-abc"
}
```

| Field | Required | Meaning |
|---|---|---|
| `phone` | yes | Digits with country code. `+`, spaces and dashes are stripped |
| `record` | no | Record this call. `400` if the server has no `-record-dir` |
| `duration_ms` | no | End the call automatically after this long |
| `operator` | no | Reserve for one operator. Ignored when an operator calls this themselves |

→ `200` `{ "call": { "callId": "A1B2C3D4E5F6" } }`

| | |
|---|---|
| `400` | `phone` missing, or `record` asked for while recording is off |
| `401` | Bad or missing credential |
| `404` | No such session |
| `409` | That **operator** is already on a call (never returned to an integration) |
| `429` | The session hit `-max-calls-per-session` |
| `503` | That account is not paired |

#### `POST /api/sessions/{sid}/calls/{id}/webrtc`

Attaches a browser's audio, and **claims the call**. An unowned call becomes this
caller's; after that nobody else can take it.

This is the browser's route — it needs a microphone and a WebRTC stack. An
integration has no use for it.

```json
{ "sdp_offer": "v=0\r\no=- ..." }
```

→ `200` `{ "sdp_answer": "v=0\r\no=- ..." }` · `400` missing offer · `404` no such
call · `409` owned by another client · `500` the offer could not be answered

The browser must open a data channel labelled `pcm` carrying raw 16 kHz mono
Int16 LE PCM in both directions. There are no audio tracks in the SDP.

#### `POST /api/sessions/{sid}/calls/{id}/accept`

Accepts an incoming call and claims it for the caller. Body is ignored.

→ `200` `{ "call": { "callId": "..." } }` · `404` · `409` another client claimed it
first · `500`

Follow this with `POST .../webrtc` to carry the audio.

#### `POST /api/sessions/{sid}/calls/{id}/reject`

Rejects an incoming call. → `200` `{ "status": "ok" }` · `409` owned by another client

#### `DELETE /api/sessions/{sid}/calls/{id}`

Ends a call in progress. → `204` · `409` owned by another client

---

### History and recordings

#### `GET /api/sessions/{sid}/history`

The 50 most recent finished calls, newest first. Survives a restart.

```json
{
  "rows": [
    {
      "sessionId": "8bd4016abd8bab66...",
      "callId": "A1B2C3D4E5F6",
      "owner": "op-abc",
      "direction": "outbound",
      "peer": "5511999999999@s.whatsapp.net",
      "startedAt": 1761050400000,
      "status": "ended",
      "endedAt": 1761050412000,
      "endReason": "user_ended",
      "recording": "1761050400000-A1B2C3D4E5F6"
    }
  ]
}
```

`recording` is the file name stem, empty when the call was not recorded.

#### `GET /api/sessions/{sid}/calls/{id}/recording`

Downloads the call as one **stereo** WAV — the operator on the left channel, the
remote party on the right.

→ `200` `audio/wav` · `404` when recording is off, the call was not recorded, or
the file is gone

---

### Everything else

#### `GET /api/config`

The settings the client has to agree with.

```json
{ "recording": "optional" }
```

`off` (no `-record-dir`), `optional` (recorded when asked), `always` (`-record-all`).

#### `GET /api/events`

Server-Sent Events: the call lifecycle plus session and pairing state. Use it if
you would rather hold a stream than receive webhooks.

```
GET /api/events?token=<secret>&clientId=<your-id>
```

Each message is one JSON object with a `type`:

| `type` | Carries |
|---|---|
| `session-list` | Every session and its state |
| `session-qr` | A pairing QR for one session |
| `auth-state` | One session's pairing state changed |
| `call-list` | Every live call |
| `call-status` | One call's status changed |
| `call-ended` | A call finished |
| `incoming` | Somebody is calling in |
| `incoming-claimed` | An operator took an incoming call |

A client that falls behind is sent a fresh snapshot rather than left on a stale
view, so a dropped event is not a permanently wrong UI.

#### `GET /healthz`

Liveness, **without a credential** — it is what a container healthcheck calls.

```json
{ "status": "ok" }
```

It carries nothing else on purpose. Being public, it must not report versions,
configuration or session counts.

---

## Webhooks

With `-webhook-url` set, WaCalls POSTs call events to it.

Delivery runs off the call path, so a slow endpoint never holds up a call — and
for the same reason events are dropped if your endpoint stays behind. Treat them
as notifications and use `GET /api/sessions/{sid}/history` when you need the
record of truth.

| Event | When |
|---|---|
| `call.incoming` | Somebody is calling one of your accounts |
| `call.ringing` | An outgoing call is ringing |
| `call.active` | Audio is flowing |
| `call.ended` | The call finished |

```json
{
  "type": "call.ended",
  "delivery": "9f2c4a1b8e7d6c5f4a3b2c1d0e9f8a7b",
  "sentAt": 1761050412345,
  "sessionId": "8bd4016abd8bab66...",
  "callId": "A1B2C3D4E5F6",
  "direction": "outbound",
  "peer": "5511999999999@s.whatsapp.net",
  "status": "ended",
  "owner": "op-abc",
  "startedAt": 1761050400000,
  "endedAt": 1761050412000,
  "endReason": "user_ended",
  "recording": "1761050400000-A1B2C3D4E5F6"
}
```

Each request carries:

| Header | |
|---|---|
| `X-WaCalls-Event` | The event name |
| `X-WaCalls-Delivery` | Unique per event; use it to discard duplicates |
| `X-WaCalls-Signature` | `sha256=<hex>`, only when `-webhook-secret` is set |

A delivery is retried twice with backoff. A `4xx` other than `429` is taken as
"you rejected this payload" and is not retried.

Only real status changes are sent: a call that reports `ringing` several times
internally produces one `call.ringing`.

### Verifying the signature

The signature is an HMAC-SHA256 of the **raw request body**, so compute it before
parsing the JSON. Compare in constant time.

```js
import crypto from "node:crypto";

function verify(rawBody, header, secret) {
  const expected = "sha256=" + crypto.createHmac("sha256", secret).update(rawBody).digest("hex");
  const a = Buffer.from(expected);
  const b = Buffer.from(header ?? "");
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
```

```python
import hashlib, hmac

def verify(raw_body: bytes, header: str, secret: str) -> bool:
    expected = "sha256=" + hmac.new(secret.encode(), raw_body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header or "")
```

---

## Deploying

### Two things that fail silently

Both let the page load, the QR pair and the call ring. Only the audio breaks, so
they look like bugs rather than configuration.

**1. HTTPS is not optional.** The client calls
`navigator.mediaDevices.getUserMedia`, and browsers only expose `mediaDevices` in
a **secure context**. Reached over plain HTTP at an IP address, the device list
comes back empty and the call never gets a microphone. Serve it on a domain with
a certificate; `localhost` is the only exception.

**2. Behind NAT, set `WACALLS_PUBLIC_IP`.** The server tells the browser how to
reach it by advertising the addresses of its own interfaces. In a container on a
bridge network those are private (`172.x`), which no browser on the internet can
reach. Set the public IP and publish a matching UDP range:

```yaml
environment:
  WACALLS_PUBLIC_IP: "203.0.113.42"     # the host's public IP
  WACALLS_UDP_PORT_MIN: "50000"
  WACALLS_UDP_PORT_MAX: "50100"
ports:
  - "50000-50100:50000-50100/udp"
```

The range must also be open on the host's firewall. One UDP port is used per
connected browser.

Host networking is the other way out — the container then sees the host's real
addresses and needs no public IP — but it binds port 8080 on every interface, so
close it at the firewall and reach the service through your proxy.

### Locally

```bash
docker compose up --build
curl http://localhost:8080/healthz
```

Then open `http://localhost:8080/?token=change-me`. `localhost` is a secure
context and the loopback needs no public IP, so neither caveat above applies.

### On a VPS with EasyPanel

EasyPanel builds the image on the server and provisions the certificate, which
covers the HTTPS requirement for you.

1. **Create the service.** Project → **+ Service** → **App**.
2. **Source** → **GitHub** (or **Git**), pointing at this repository and branch.
   Build method **Dockerfile**, path `./Dockerfile`. The image is built on the
   VPS; nothing needs Docker on your own machine.
3. **Domains** → add your domain, proxy port **8080**, HTTPS on. Point the
   domain's DNS at the VPS first so the certificate can be issued. **Never reach
   it by IP** — the microphone will not work.
4. **Advanced → Ports**: publish `50000-50100` UDP, target the same.
5. **Mounts** → volume → mount path `/data`. This holds `wacalls.db`, the paired
   WhatsApp credentials, plus the recordings. Without it every deploy wipes both
   and every account has to be paired again.
6. **Environment**:

   ```
   WACALLS_PUBLIC_IP=<the VPS public IP>
   WACALLS_UDP_PORT_MIN=50000
   WACALLS_UDP_PORT_MAX=50100
   WACALLS_TOKEN=<long random string>
   WACALLS_API_KEY=crm:<another long random string>
   WACALLS_ALLOW_ORIGIN=https://your-domain
   WACALLS_RECORD_DIR=/data/recordings
   WACALLS_MAX_CALLS_PER_SESSION=8
   ```

7. **Deploy**, then open the UDP range on the VPS firewall:

   ```bash
   ufw allow 50000:50100/udp
   ```

Updating later is a `git push` plus **Deploy** — EasyPanel rebuilds from the
Dockerfile.

#### Checking it in the order that isolates a problem fastest

```bash
curl https://your-domain/healthz          # {"status":"ok"} — image up, TLS working
```

Then open `https://your-domain/?token=<your token>` and pair an account by QR;
that proves the container reaches WhatsApp. Finally place a call with
`chrome://webrtc-internals` open: **the remote candidate must be the VPS public
IP.** If it shows `172.x`, `WACALLS_PUBLIC_IP` did not take effect — and that is
the exact point where audio dies.

### On a VPS with Portainer

Deploy `compose.yaml` as a stack, replacing `build: .` with your image, or keep
the build by uploading the repository. Put a reverse proxy with a certificate in
front of port 8080 — the HTTPS requirement is the same.

If you use `network_mode: host` instead, drop `WACALLS_PUBLIC_IP` and the UDP
publishing: the container sees the host's addresses directly.

### Configuration

Every flag is also an environment variable — `WACALLS_` plus the flag name in
upper case with dashes as underscores:

| Variable | Flag | |
|---|---|---|
| `WACALLS_ADDR` | `-addr` | **`:8080` inside a container.** The binary defaults to `127.0.0.1:8080`, which in a container listens on the container's own loopback and is unreachable from outside |
| `WACALLS_TOKEN` | `-token` | What the browser client presents |
| `WACALLS_DEVICE_NAME` | `-device-name` | Name shown in WhatsApp → Linked devices. Takes effect on **new pairings only** |
| `WACALLS_API_KEY` | `-api-key` | `name:secret`, comma-separated for several |
| `WACALLS_DB` | `-db` | Put it on a volume |
| `WACALLS_RECORD_DIR` | `-record-dir` | Put it on a volume |
| `WACALLS_RECORD_ALL` | `-record-all` | |
| `WACALLS_WEBHOOK_URL` | `-webhook-url` | |
| `WACALLS_WEBHOOK_SECRET` | `-webhook-secret` | |
| `WACALLS_ALLOW_ORIGIN` | `-allow-origin` | Your domain, not `*` |
| `WACALLS_MAX_CALLS_PER_SESSION` | `-max-calls-per-session` | |
| `WACALLS_PUBLIC_IP` | `-public-ip` | **Required behind NAT.** See below |
| `WACALLS_UDP_PORT_MIN` / `_MAX` | `-udp-port-min` / `-max` | Browser media ports, published and firewalled |

A flag given on the command line beats the variable. An empty variable counts as
unset, so a blank field in a deployment UI cannot silently clear a setting.

### Persistence

`/data` holds `wacalls.db` — the **paired WhatsApp credentials** — and the
recordings. **Mount it.** Without a volume every redeploy wipes both and every
account has to be paired again by QR.

```yaml
volumes:
  - wacalls_data:/data
```

### Swarm and Portainer

A Swarm stack wants **host networking** so the VoIP relays reach the host's UDP
stack directly instead of going through the bridge:

```yaml
services:
  wacalls:
    image: your-registry/wacalls:latest
    networks: [hostnet]
    environment:
      WACALLS_ADDR: ":8080"
      WACALLS_TOKEN: "..."
      WACALLS_DB: "/data/wacalls.db"
    volumes:
      - wacalls_data:/data
```

On host networking the service binds port 8080 on **every** host interface, so
either close that port at the firewall and reach it through your reverse proxy,
or accept that it is directly reachable over plain HTTP. Set `WACALLS_TOKEN` and
`WACALLS_API_KEY` either way.

---

## What this API does not do

- **It never sends audio.** There is no way to play a file or a TTS prompt into a
  call; a human has to take it in the browser.
- **Credentials come from flags or the environment.** Rotating a key or changing
  the webhook URL means restarting.
- **One webhook URL for the whole server**, not one per WhatsApp account.
- **No retention policy for recordings.** They grow until you delete them, and
  they are voice recordings of real people — decide how long you keep them.
