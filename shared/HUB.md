# Relay hub protocol (runner ⇄ HabitTracker)

Files: runner/internal/uplink (Go side), HabitTracker `habitTracker/relay/` (hub side)

Lets a browser reach a runner through HabitTracker without Tailscale or inbound ports.
The runner dials OUT to the hub; the hub forwards the browser's `/v1/*` calls down that socket;
the runner answers them with its normal handler. The runner HTTP API (API.md) is unchanged.

```
browser ──HTTPS(session + 2FA)──▶ HabitTracker ◀──WSS (device token)── runnerd
         /relay/api/devices/{id}/v1/...           /relay/uplink
```

## QR pairing (recommended, phone first)
1. Machine: `runnerd pair [--hub https://<host>] [--name laptop]` (hub default: `$RELAY_HUB`, else `https://habittrackerdima.me`;
   the Linux and Windows installers run it at the end)
   → `POST /relay/pair/start` `{"name":"laptop"}` (no session, no CSRF)
   → `200 {"claimId":"<20 chars A-Z2-9>","pollSecret":"<43-char base64url>","expiresInSeconds":600}`; `429` too many pending.
2. Runner prints a QR of `https://<host>/relay/claim/<claimId>` (and the link as text).
3. Phone opens the link → login + 2FA if needed → "Add *laptop*?" →
   `GET /relay/api/claims/{id}` → `{"name"}` · Confirm `POST /relay/api/claims/{id}/approve` → `{"deviceId"}` (device created for THIS user) ·
   "Not mine" `POST /relay/api/claims/{id}/deny` → `204`. All three `404` when expired, used or unknown.
4. Runner polls every 2s: `POST /relay/pair/poll` `{"claimId","pollSecret"}` → `202 {"status":"pending"}` |
   `200 {"deviceId","token"}` (once; the claim is then removed) | `404` declined / expired / unknown / wrong secret.
5. Runner saves `hub.json` as below; an already-running runnerd notices the file within 5s and dials the uplink (no restart).
- claimId is a bearer link (100 bits), never typed; pollSecret never leaves the machine. Claims are in hub memory only.
- Confirm is the safety step: scanning someone else's QR would otherwise attach THEIR machine to your account.

## Code pairing (desktop alternative)
1. Browser (logged in + 2FA): `POST /relay/api/devices/pairing-code` → `{"code":"ABCD2345","expiresInSeconds":600}`.
   8 chars, single use, 10 min, bound to the user.
2. Machine: `runnerd pair [--hub https://<host>] --code ABCD2345 [--name laptop]`
   → `POST /relay/pair` `{"code":"ABCD2345","name":"laptop"}` (no session, no CSRF: the code is the credential)
   → `200 {"deviceId":"<id>","token":"<43-char base64url, 32 random bytes>"}`; `400/404` bad or used code.
3. Runner saves `{"hub":"https://<host>","deviceId":..,"token":..}` to `hub.json` (0600) next to `key.txt`.
   Hub stores ONLY `sha256(token)` hex on the Device document `{id, userId, name, tokenHash, createdAt, lastSeenAt}`.

## Uplink
- Runner: `GET wss://<host>/relay/uplink` with header `Authorization: Bearer <token>`.
  Hub looks up `sha256(token)`; unknown → `401` before upgrade. A new connection for the same device replaces the old one.
- Reconnect with backoff 1s → 2s → … → 60s cap, forever. Hub sends WS pings every 30s; runner replies (library default).
- Revoking a device (browser `DELETE /relay/api/devices/{id}`) deletes the document and closes its socket.

## Frames (JSON text messages)
Hub → runner:
```json
{"type":"req","id":"<uuid>","method":"POST","path":"/v1/sessions/abc/message?x=1","body":"{\"text\":\"hi\"}"}
```
Runner → hub:
```json
{"type":"res","id":"<uuid>","status":202,"contentType":"application/json","body":"..."}
```
- `body` is a UTF-8 string ("" when empty). The API is JSON-only; binary is out of scope.
- Runner dispatches to its own `http.Handler` in-process, adding `X-Relay-Key: <its own key>` itself; the hub never knows the runner key.
- Runner handles requests concurrently and only accepts paths starting with `/v1/`; anything else → `status 404`.
- Max body 4 MiB each way; larger → `413` (runner) / `502` (hub).
- Max frame: hub accepts 8 MiB + 64 KiB (a JSON-escaped body can be bigger than the body). A runner response
  frame over that would make the hub close the uplink, so the runner sends `502` instead.
- Oversized browser request → hub answers `413` itself.

## Browser proxy
`ANY /relay/api/devices/{deviceId}/v1/**` (session + CSRF + TOTP gate on `/relay/**`)
→ device.userId != current user → `404` (never reveal other users' devices)
→ device offline → `503 {"error":"device offline"}`
→ forward frame, wait 30s → `504` on timeout → relay `status`, `contentType`, `body`.

`GET /relay/api/devices` → `[{"id","name","online","lastSeenAt"}]` (current user's only).
