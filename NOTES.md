# Diablo III (Switch) — local Demonware server: notes

Everything here was read out of the game binary or observed live on a real
console. Nothing is guessed unless marked **UNCONFIRMED**. Keep this file
updated as you go: it is the map for the next game server too.

- Game: Diablo III Switch, title `01001B300B9BE000`, Demonware title "crimson", id 5745
- Binary: `/atmosphere/contents/01001B300B9BE000/exefs/main` (the d3hack
  downgrade target), extracted to `d3hack/capture/nso/{text,rodata,data}.bin`
- Status (2026-09-13): auth ✔ · encrypted lobby ✔ · season 37 served ✔ ·
  NAT probes ✔ · matchmaking ✔ · console ↔ Citron co-op ✔ · friend lookups ✔ ·
  presence reported ✔ — entirely on local servers.
  Open: "friends online" count on the devices (Nextendo presence must reach
  them), service 29/68/4/10-user-files real storage, stable per-account user IDs.
- Home: `nextendo/diablo-3`, one server laid out like the other Nextendo game
  servers (§1, §7). It started life as three separate binaries (d3-auth, d3-lobby,
  d3-pubfiles); the git history came along.

Join sequence observed: host 21/1 createSession → 21/2 update; joiner 21/5
findSessions (1 result) → direct P2P connect (same LAN, no 0x0A introduction
needed) → host 21/12 updateSessionPlayers(sessionID, u32 players=2, info).
A joiner rejects a found game instantly (and hosts its own) when one session
attribute differs from its filter; seen once with a flag set 1 vs 0, cause
not yet identified (both heroes were seasonal).

**Co-op drops ~8 s after joining = client mods, not the server.** With d3hack on
the console, every join succeeded (21/12 players=2) then dropped (21/12
players=1). Cause: d3hack's `ParagonFieldWidening` widens the paragon attribute
to 31 bits on the wire whenever `MaxParagonLevel > 20000` — gated on that value
alone, even with `[rare_cheats] SectionEnabled = false`. A stock peer (Citron)
misparses the player data and drops the session. Gameplay-changing patches
(`SetBonusTierShift`, `SocketAffixSuppress`, client-side `[events]`, …) break
co-op the same way. Online-safe d3hack config: cheat sections off AND
`MaxParagonLevel = 20000`; community buffs come from the server's Config.txt.
Version mismatch (2.7.6 vs 2.7.7) was NOT the problem: a hack-off 2.7.6 console
played with 2.7.7 Citron.

**Friends: scope.** Nintendo friend lists and online presence are Nextendo's
responsibility (console: BaaS/Penne; emulators: nextendo.network API). D3 reads
them through nn::friends (GetFriendList, FriendPresence::GetStatus,
IsSamePresenceGroupApplication). The D3 server only answers the Demonware side:
12/9 getUserNames resolves friend IDs to connected players — console friends by
Nintendo device-account ID, Citron friends by Nextendo PID. Verified both ways
(console → player2, Citron → player1). Do not rewrite presence in local proxies.

**Presence, the Nextendo way (2026-09-13).** Nextendo game servers report who is
online to nextendo-account: `POST /internal/presence-batch {appId, status:2, pids}`
every 30 s with `X-Internal-Key` (TTL 90 s). nextendo-account hands it to
**nx-account**, which builds the console's BaaS friend objects ("online / playing").
diablo-3 does the same with the players' Nextendo PIDs (presence.go).
Limits: nx-account is **private** (not in the NextendoNetwork org); the console's
friend list and account-link page come from the real Nextendo, whose presence
intake is internal — so a locally hosted D3 server's presence only reaches devices
that read the same nextendo-account. Local accounts also use local PIDs
(1800000101…), not the real Nextendo PIDs.

**Web pieces.** Local nextendo-account already implements every `/api/*` endpoint
of NextendoNetwork/nextendo-site (incl. OAuth authorize/token/userinfo). It serves
the website when `NEXTENDO_STATIC` points at the site files; ours points at
`nextendo/web` (registration page only). Pointing it at a nextendo-site checkout
gives login, account/friends, sessions, status, downloads, verify/forgot/reset
locally (PolyForm Shield license allows self-hosting).

**Local test accounts (assessed 2026-09-13, not built).** The D3 server needs
nothing: it takes the player from whatever Nextendo account the login token
carries (nnex claim), local or real. The blockers are on the devices:
- *Citron* pins its Nextendo API to `nextendo.network` (loopback overrides only).
  Workable without a rebuild: hosts `nextendo.network` → PC, the local CA
  installed on the phone (Citron trusts user CAs), and that host routed to the
  local nextendo-account (tls-front). Accounts are then made on the local site
  and friends/presence are local end to end. While active, the real account is
  unreachable in Citron.
- *Console*: account link, friend list and presence come from the private
  **nx-account** through baas-proxy → real Nextendo. Needs a local stand-in for
  the BaaS endpoints the console calls (all visible in the baas-proxy log),
  backed by the local nextendo-account.

**Confirmed 2026-09-13 04:37:** console (d3hack, online-safe config in
`d3hack-online-safe.toml`) hosted; Citron (stock 2.7.7) quick-matched in via the
NAT introduction relay; host reported 2/4 and the session held. Community buffs
served from Config.txt instead of the client. Friends: Citron's login lookup
(12/9, Nextendo PID 1800000101) resolved player1 as online.

---

## 1. Components

One process (`server.exe`), in `nextendo/diablo-3`:

| piece | port | files |
|---|---|---|
| `nextendo/sni-router` | TCP 443 | routes `*.demonware.net` SNI → auth (`BACKEND_D3`, PROXY header) |
| auth | TCP 8460 (TLS) | `auth.go`, `identity.go`, `gates.go` — `/auth/` login, Nextendo gates, tickets in `sessions/` |
| lobby | TCP 3074 | `lobby.go`, `handshake.go`, `bdcrypto.go`, `pubkey.go`, `services.go`, `matchmaking.go`, `friends.go` |
| NAT | UDP 3074 | `nat.go` — IP/NAT discovery, introductions |
| publisher files | — | `pubfiles.go` + `pubfiles.json` → `pubfiles/` (`server.exe pubfiles` regenerates) |
| presence | — | `presence.go` → nextendo-account `/internal/presence-batch` |
| dashboard | HTTP 8093 | `dashboard.go` — `/api/stats`, `/healthz`, `/pubfiles/` |

Config: `.env` (see `example.env`). Launch: `nextendo_servers.bat`,
`restart_stack.ps1`, `start_nextendo_servers.ps1` (entry `diablo-3`); log in
`nextendo/logs/diablo-3.log`. Raw captures only with `D3_DUMPS=<dir>` (one
folder per run), decrypted-message hex dumps only with `D3_VERBOSE=1`.

DNS: the console uses Atmosphere hosts (`nextendo/switch_hosts_local.txt`,
sections 6–7). Emulators on the PC hotspot resolve through Windows ICS, which
answers from the PC's `C:\Windows\System32\drivers\etc\hosts` (Demonware
entries added there). Never let D3 reach real Demonware/Activision.

Hosts the game uses (from rodata):
`crimson-switch-auth3.{prod,cert,dev}.demonware.net`,
`crimson-switch-lobby.{prod,cert,dev}.demonware.net`,
`stun.{us,eu,jp,au}.demonware.net`.

---

## 2. Auth (HTTPS JSON) — `auth.go`

Request body (all integers are JSON *strings*):
`{auth_task, iv_seed, title_id:"5745", identity:"356c4bc3", extra_data:"{version, token(NSA id_token), username, extended_data}"}`

Response — **field order matters** (sequential parser at 0xBE30C0):
`auth_task` (= request + 1), `code` `"700"`, `iv_seed`, `client_ticket`, `server_ticket`, `extra_data`.

- tickets: 128 bytes, **standard base64** (`+/`, padded)
- `extra_data` is a *string* containing JSON; `{"nso_subscription_status":"1"}`
  is required (vtable[0x50] = 0xBE1440). Without it every response is 735.
- 735 is a catch-all failure. Instrument, don't guess.

Ticket layout (parse_ticket 0xBFCF30):
```
+0   u32  magic 0xEFBDADDE (bytes DE AD BD EF) — if present the client skips ticket decryption
+4   u8
+5   u32  title id | +9 u32 | +13 u32 issued
+17  u64  user id  | +25 u64 expiry
+33  [64] session key
+97  [24] LOBBY KEY  (becomes conn+0x100, signs the lobby handshake)
+121 [3]  | +124 [4]
```
auth.go fills +97 with session_key[0:24] and sends the same bytes as server
ticket. The lobby recovers the key from the ticket echoed back in 0x82.

---

## 3. Lobby transport — `handshake.go`

Connection setup 0xBFC8F0, reader 0xBFA950, receive/dispatch 0xBFAB90.

**Hello (raw, unframed, client → server, 28 B):**
`u32 200 | u32 200 | u32 210 | u32 220 | u32 maxFrame(0x1FFFFF) | nonce[8]`

**Frames (both directions):** `u32 L | u8 flag (0xAB) | body[L-1]`, body[0] = type. `L = 0` is a keepalive.

**Handshake (conn+0x210 state):**
1. server → `0x81`: `u32 version (210..220) | u64 connection id | 8 B (skipped)`
   The u64 is later handed to the game as the "connected" user id.
2. client → `0x82` (0xBFBB80): bdBitBuffer{u32, u32, 128 B server ticket} + 8 B tag
3. server → `0x83`: `u64 check`; `0x84` = error (u32)
4. then everything is `0x85` encrypted.

**Crypto** (LibTomCrypt, SHA-1):
```
transcript = u32 210 | u32 220 | u32 maxFrame | nonce8
           | u32 22 | AB | 81 | 0x81 payload (20 B)
           | the 0x82 frame minus its last 8 bytes
secret     = HMAC-SHA1(key = SHA1(transcript), msg = key24)
             (if conn+0x208 set: key24 = HKDF(key24, RSA pubkey DER @0xF07E90, 24) — not used by D3)
HKDF       = 0xBDB730: T1 = HMAC(k, label|01), Ti = HMAC(k, T(i-1)|label|i)
CLIENTCHAL = HKDF(secret, "CLIENTCHAL", 16): [0:8] = 0x82 tag, [8:16] = 0x83 check
BDDATA     = HKDF(secret, "BDDATA", 72):
             [0:20] c2s HMAC  [20:40] s2c HMAC  [40:56] c2s AES-128  [56:72] s2c AES-128
```
**`0x85`** (0xBFB730 read, 0xBF9DD0 write, same both ways):
`u32 seq (starts at 1) | IV[16] | AES-CBC(u32 N | u8 innerType | payload[N] | zero pad) | tag[8]`
tag = HMAC-SHA1(dir MAC key, whole frame incl. length, minus tag)[:8].

---

## 4. Remote tasks

Lobby service pump 0xBE96E0 dispatches decrypted inner types:
`1` task reply · `2` push · `3` u32 → +0x6F4 · `4` u64 connected user id · `5` ?

**Request** (inner type `0x86`): typed bdByteBuffer, first byte raw:
`u8 service | 03 u8 task | typed args | 00`

**Reply** (inner type `0x01`, 0xBE3FE0 → 0xC00370 → 0xC005B0) — FIFO, the oldest pending task takes it:
```
0A u64 transaction | 08 u32 error (0 ok, 200 pending, else fail)
| 03 u8 | 08 u32 numResults | [08 u32 totalResults | results…]
```
**Typed bdByteBuffer tags** (readers 0xBD90E0, 0xBD9170, 0xBDA100, 0xBDA220, 0xBDA4C0; check 0xBD9D80):
`01 bool, 03 u8, 06 u16, 07 i32, 08 u32, 09 i64, 0A u64, 10 NUL-string, 13 blob (08 u32 len + bytes), 16 compressed, 100+t = array of t (u32 byte count follows)`

**Request builders:** 0xBE6520(buf, service, task, …) and 0xBE3A10(buf, service, task).
Call-site map in `d3hack/capture/nso/taskmap.txt`:

| service | tasks seen in binary | identified | handled |
|---|---|---|---|
| 4 (stats/leaderboards?) | 1, 4, 5, 11, 13 | 11 called 6× at char select | empty ok |
| 6 (messaging?) | 14 | | |
| 10 bdStorage | 10, 12, 13, 21 | 21 = getPublisherFile(ctx, name); 10 = upload user file ("account" save, 2.5 KB protobuf) | 21 ✔, 10 empty ok |
| 12 bdTitleUtilities | 6, 9 | 6 = getServerTime → u32 | 6 ✔, 9 empty ok |
| 21 bdMatchMaking? | 1, 2, 3, 5, 12 | never called yet (online game creation) | |
| 23 bdCounter? | 1 | (u32 counter, i64 delta) | empty ok |
| 27 bdDML? | 2 | getUserData, no args | empty ok |
| 29 ? | 1, 4, 5, 8, 11 | 11 = (ctx str, u32 offset, u32 limit, u16[], bool) paged query | empty ok |
| 67 | 6 | | |
| 68 | 3, 5, 7 | | |

**Publisher files** requested on connect: `Config.txt`, `Seasons.txt`,
`Blacklist.txt`, `update-1.cpk` (absent ok). The Challenge Rift files
(`challengerift_config.dat`, then `challengerift_<number>.dat`) are asked for when
the Challenge Rift menu opens, per d3hack's source (UNCONFIRMED on a live game:
the menu needs a high character level); served from `D3_RIFTDATA`, see `riftdata.go`.
Real copies are cached by d3hack in `sd:/config/d3hack-nx/rift_data/`.
File formats: see `pubfiles.go`. `Seasons.txt` dates must have a 2-digit day.
Results for getPublisherFile: one `bdFileData` = one blob.

---

## 5. NAT probes (UDP 3074) — implemented, awaiting live confirmation

Client sends from :3074 to `stun.*.demonware.net:3074` just before joining the
lobby. Raw bytes, not RFC 5389 (format from project-bo4/shield-development):
```
1e 03 00      type 30 IP discovery  -> 31 | 02 | 00 | ip[4] (network order) | port u16 LE
14 02 00 00   type 20 NAT discovery -> 21 | 02 | 00 | ip[4] | port u16 LE | serverIp[4] | serverPort u16 LE
```
Unanswered → NAT type unknown → **online games become local only** (friends-online
worked while these still reached real Demonware).

**Confirmed live 2026-09-13** (d3hack SendTo/RecvFrom hook with frame walk): IP
discovery sent from 0xC03D6C (serializer 0xC10A08), NAT discovery from 0xC03DA4
(serializer 0xC1145C), replies read at 0xC040A8; console received our 9- and
15-byte replies. A second NAT probe `14 02 00 03` follows. After the replies,
Citron's quick match performed 21/5 → 21/1 → 21/2 (online games unlocked).
Also seen, not yet handled: `0e 02 00 0? … ff 00 ff 00 …` (29 B, type 14) on UDP 3074.

Lesson: two hours of static tracing did not find the sender (it is a virtual call
through the `bdSocket` vtable at 0x1144568, sendTo = +0x38); the packet format
was one search away in an open-source emulator. Check references early.

---

## 6. Reverse-engineering method (reuse for the next server)

1. **Get endpoints from rodata strings**, point DNS at a sink, log everything first.
2. **Never guess reply formats.** Ten guessed lobby replies all failed; reading
   the client's *read path* solved it in one pass. Find the reader, not the writer.
3. Ghidra headless without analysis (fast): `analyzeHeadless <proj> <name>
   -deleteProject -noanalysis -import text.bin -loader BinaryLoader
   -loader-baseAddr 0 -processor AARCH64:LE:64:v8A -preScript AddNsoSegments <dir>
   -postScript DecompAt|DisasmRange <out> <addrs>` (scripts in `d3hack/tools/ghidra_scripts`).
   Decompiling from a mid-function address gives `unaff_x19` garbage — start at the
   real entry (a `bl` target is always one).
4. **Indirect calls:** GOT slot → `R_AARCH64_RELATIVE` addend (RELA at 0xC73048,
   54 747 relocs) gives the vtable; slot N = relocation at vtable+0x10+N. The
   dynamic symbol table only has imports.
5. **Quick scanners** (inline C# via `Add-Type` in PowerShell): BL/B callers of a
   target, ADRP+ADD references to a string, call sites of a builder with the
   `w1/w2` constants loaded before the call.
6. Serialization layers seen so far: framing → handshake state machine → crypto
   → inner message type → service/task → typed buffer. Each layer has one
   dispatcher; find it and read its `switch`.
7. Traps: Windows Firewall "Query User" block rules silently drop SYNs; Atmosphere
   hosts need a reboot; a manual IP on a DHCP adapter disables DHCP.
8. **Instrument inside the game first: an exlaunch module.** D3 went fast because
   d3hack is an exlaunch module (`exefs/subsdk9` + `main.npdm`): one hooked run
   answered what hours of guessing could not (auth `parse ret=735
   expected_task=79`; the SendTo/RecvFrom frame walk that found the NAT prober).
   For the next game, start with a small exlaunch logger: hook the SDK network
   calls (nn::socket Send/Recv/SendTo/RecvFrom, nn::ssl) resolved with
   nn::ro::LookupSymbol — the same symbols in every game — log to SD and pull
   over FTP; then hook the game's own parser once its address is known
   (template: d3hack `authlog.hpp`).
9. **What to pull for decompiling:** `exefs/main` of the version actually
   installed (base + update) holds the game code and its statically linked
   online stack (Demonware, NEX, Pia): that is the Ghidra input. Also keep
   `main.npdm` (title id, SDK version) and `sdk`/`subsdk*` (the nn:: SDK with
   exported symbols, which names the imports `main` calls). Offsets change
   between versions, so decompile the exefs the console runs. romfs only if
   the protocol reads configs or certificates from it.

---

## 7. Nextendo integration (gates, presence, dashboard)

Same contract as the NEX game servers (reference: `nextendo/luigis-mansion-3`).

- **sni-router** owns TCP 443 for every TLS host, so each game with a TLS host
  gets a route there (ACNH's came as a PR the same way). D3's route is
  `demonware.net` → `BACKEND_D3` (127.0.0.1:8460), TLS passthrough with the
  PROXY v1 header like the NEX backends (`SNI_PROXY_PROTOCOL=1` on the router,
  `NEXTENDO_PROXY_PROTOCOL=1` here, `proxyproto.go`), so the auth and the
  online-check see the player's address. The lobby (TCP/UDP 3074) needs no
  router: DNS points straight at the server.
- **Gates at login** (`gates.go`, before a ticket is issued): Nextendo PID from
  the id_token's `nnex` claim (`nx2.<b64 PID.nick.expiry>.<HMAC-SHA256 "nex:"…>`),
  signature checked when the Nextendo secret is configured
  (`NEXTENDO_REQUIRE_SIGNED_TOKEN=1` enforces); `POST /internal/online-check
  {pid, kind, ip}` (fail-open if unreachable). Kind: `ryujinx` when the id_token
  has `di`/`sn` claims (Citron), else `switch`. `NEXTENDO_REQUIRE_ACCOUNT=1`
  enforces refusals; the local `.env` uses 0 because the players' accounts live
  on the real Nextendo, which the local nextendo-account and secret don't hold.
  A refused login gets HTTP 403 (no ticket).
- **Presence** (`presence.go`): every 30 s, `POST /internal/presence-batch`.
- **Dashboard** (`dashboard.go`): `/api/stats?key=DASH_TOKEN` in the NEX servers'
  JSON shape. Players = lobby connections with a known Nextendo player;
  gatherings = bdMatchMaking sessions; "rmc" = remote tasks named
  `Service::task`. nextendo-dashboard polls it as source `d3` (`DASH_D3_URL`).
- **Firewall:** a new `server.exe` path has no Windows Firewall rule. Add allow
  rules (elevated) before the first run, or a dismissed prompt creates a silent
  Block rule on the Public profile (the hotspot) — see §6 item 7.

**Quick match broke after a Citron reinstall (2026-09-14 00:00-00:40), fixed without a server change.**
Symptom: each side found the other's game (21/5 -> 1 result, sometimes a NAT INTRO) and hosted its own within a
second. Checked and ruled out: the server (matchmaking.go and the NAT handler are code-identical to the 2026-09-13
working build; a clean copy behaves the same), the hotspot (devices ping each other), region/strictness
and the one search value that differed (filter slot 9, Switch 1 vs Citron 0: made equal, still failed).
Causes, in order:
1. Reinstalling Citron wiped its **Diablo III update and DLC**. An un-updated game finds updated games and never
   joins them. Install update 0.22.0 (v1441792) + DLC.
2. It also wiped the per-game graphics settings (Turnip driver, GPU ASTC, normal accuracy, async shaders); with the
   defaults the S22 ran out of memory and Android killed Citron (lmkd, ~4.5 GB RSS).
3. A found session can be stale: its host was no longer in a joinable game (lobby connection idle since the game
   ended). Test with the host sitting in its public game, then quick-match from the other device.
Confirmed 00:40:04: Switch quick-matched into Citron's game, INTRO relayed, host 21/12 players=2/4.