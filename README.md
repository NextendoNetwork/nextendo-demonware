# diablo-3

Game server for **Diablo III** on Nintendo Switch, for [Nextendo Network](https://nextendo.network). Source only — no binaries, no certs, no game assets. Not affiliated with Blizzard, Activision, Demonware or Nintendo.

Diablo III does not use NEX: its online layer is **Demonware**. This server speaks it end to end — auth, the encrypted lobby, remote tasks, matchmaking, NAT discovery — and plugs into the Nextendo stack like the other game servers: a route in sni-router, the account gates and presence of nextendo-account, and `/api/stats` for nextendo-dashboard.

## What works

- Login and "connected to the Diablo network"; season and community events served from `pubfiles.json`
- Public games: create, find, join, player counts, NAT introductions
- Co-op between a Switch and an emulator (tested: CFW Switch, Citron 2.7.7)
- Friend lookups inside the game, and presence reported to nextendo-account

## Requirements

This server does not run alone. It sits behind the rest of the Nextendo stack, and a few of those pieces need changes that are shipped as separate pull requests.

| component | needed? | what it must provide |
|---|---|---|
| **sni-router** | required | A route sending `crimson-switch-auth3.*.demonware.net` to `BACKEND_D3` (default `127.0.0.1:8460`), and the PROXY protocol v1 header on that route (`SNI_PROXY_PROTOCOL=1` on the router, `NEXTENDO_PROXY_PROTOCOL=1` here) so the auth sees the player's real address. Neither is in sni-router's `main` yet. The lobby (TCP/UDP 3074) does not go through the router. |
| **nextendo-account** | required for account gates and presence | `POST /internal/online-check` and `POST /internal/presence-batch`, both authenticated with `X-Internal-Key` (`NEXTENDO_INTERNAL_KEY`). Already in `main`. With `NEXTENDO_REQUIRE_ACCOUNT=0` a missing or unreachable account service only logs. |
| **nextendo-dashboard** | optional | A `d3` source polling this server's `/api/stats` on port 8093 (`DASH_D3_URL`, `DASH_D3_TOKEN`). Without it the server works, it just is not on the shared dashboard. |
| **baas-jwks**, RS256 BAAS tokens in nextendo-account | only for real consoles | Letting a real Switch link an account without Nintendo. Emulators (tested: Citron) do not need it. Not specific to Diablo III. |
| **DNS** | required | `crimson-switch-auth3.*.demonware.net`, `crimson-switch-lobby.*.demonware.net` and `stun.{us,eu,jp,au}.demonware.net` must resolve to the stack, and the game must never reach the real Demonware. A console uses Atmosphere hosts entries; an emulator uses the resolver of its host. |
| **TLS certificate** | required | `CERT_FILE` / `KEY_FILE` for the auth port (default `cert.pem` / `key.pem`, never committed). It must be a certificate the client accepts for `*.demonware.net`. |
| **Game files on the client** | required | The client must run the current game update. A client without it finds updated games but can never join them. |

For co-op between a console running d3hack and a stock peer, keep d3hack's cheat sections off and set `MaxParagonLevel = 20000`: gameplay-changing patches on one side desync the session and the game drops the join a few seconds later. Community buffs are served by this server (`pubfiles.json`) instead.

## Install into a Nextendo deployment

These steps follow the generic [deployment guide](https://github.com/NextendoNetwork/nextendo-docs/blob/main/DEPLOYMENT.md); every value is a placeholder you replace with your own.

1. **Prerequisites.** Go 1.23 or newer, and a running `nextendo-account` and `sni-router` (see Requirements above).
2. **Get the code and build it.**

       git clone https://github.com/NextendoNetwork/diablo-3.git
       cd diablo-3
       go build -o server .

3. **Create the environment file.** `cp example.env .env`, then set:
   - `NEXTENDO_HOST` to the IPv4 address players reach this server on;
   - `NEXTENDO_ACCOUNT_URL` to your nextendo-account;
   - `NEXTENDO_INTERNAL_KEY` and `NEXTENDO_SECRET` to the **same values** nextendo-account uses, otherwise the gates and presence calls are refused;
   - `NEXTENDO_PROXY_PROTOCOL=1` when sni-router emits the PROXY header (it should);
   - `NEXTENDO_REQUIRE_ACCOUNT=1` once your accounts are live (0 only logs);
   - `DASH_TOKEN` to a random value (the shared dashboard uses it).
4. **Create a TLS certificate** for the auth hostnames and point `CERT_FILE` / `KEY_FILE` at it. A self-signed one, for testing where the client is set up to trust it:

       openssl req -x509 -newkey rsa:2048 -nodes -days 825 -keyout key.pem -out cert.pem          -subj "/CN=crimson-switch-auth3.prod.demonware.net"          -addext "subjectAltName=DNS:crimson-switch-auth3.prod.demonware.net,DNS:crimson-switch-auth3.cert.demonware.net,DNS:crimson-switch-auth3.dev.demonware.net"

5. **Route it in sni-router.** Set `BACKEND_D3=127.0.0.1:8460` (or wherever the auth port is) and `SNI_PROXY_PROTOCOL=1`, restart the router.
6. **Point the DNS at the stack** as listed in Requirements, and open TCP 3074, UDP 3074 and the auth and dashboard ports in the host firewall.
7. **Add it to the dashboard (optional).** Set `DASH_D3_URL=http://127.0.0.1:8093` and `DASH_D3_TOKEN` (the `DASH_TOKEN` above) on nextendo-dashboard.
8. **Start it and check.**

       ./server

   The log should show `[D3 Auth] listening HTTPS`, `[D3 Lobby] listening TCP` and `[D3 NAT] listening UDP`. Then `curl http://127.0.0.1:8093/healthz` returns 200, and a game logging in produces `[D3 Auth] ... -> 200 code=700`.

## Build

    go build -o server.exe .
    go test ./...

Standard library only.

## Configure and run

Copy `example.env` to `.env` and edit it; every variable is documented there. At minimum set `NEXTENDO_HOST` (the server's IPv4 as players see it), the three secrets (`NEXTENDO_INTERNAL_KEY`, `NEXTENDO_SECRET`, `DASH_TOKEN`) and `NEXTENDO_ACCOUNT_URL`.

    server.exe

| port | what |
|---|---|
| 8460 TCP (TLS) | Demonware auth, behind sni-router (`*.demonware.net` -> `BACKEND_D3`) |
| 3074 TCP | lobby (`LOBBY_PORTS` also opens 3075-3080) |
| 3074 UDP | NAT discovery and introductions |
| 8093 HTTP | `/api/stats?key=DASH_TOKEN`, `/healthz`, `/pubfiles/` |

Open the ports in the host firewall before the first run. A dismissed firewall prompt creates a silent block rule.

The season, community events and the item blacklist are generated from `pubfiles.json`:

    server.exe pubfiles    # regenerate Config.txt / Seasons.txt / Blacklist.txt and exit

The lobby reads the publisher files on every request, so changing the season or an event needs no restart.

Runtime state (`sessions/`, `pubfiles/`, `dumps/`) is created next to the binary and ignored by git. `D3_DUMPS=<dir>` records raw auth bodies and lobby frames; `D3_VERBOSE=1` logs every decrypted lobby message.

## Credits

- **[Nextendo Network](https://nextendo.network)** ([NextendoNetwork](https://github.com/NextendoNetwork))
  — the Switch online stack this server plugs into: NSA/BaaS accounts, dauth,
  the SNI router that carries Demonware traffic, and the game-server pattern
  (gates, presence, dashboard) this server follows.
- **[D3Hack](https://github.com/god-jester/D3StudioFork)** by **jester**, on
  [exlaunch](https://github.com/shadowninja108/exlaunch) by **Shadow** — used
  as the instrumentation platform (hooks and logging inside the game) and as the
  source of the publisher-file formats (`Config.txt`, `Seasons.txt`, `Blacklist.txt`).

Demonware reference implementations consulted (all for other titles). Protocol
facts were read from them and **reimplemented** here in Go; no code was copied.

- **[project-bo4/shield-development](https://github.com/project-bo4/shield-development)** (GPL-3.0)
  — Demonware STUN/NAT-discovery packet format (UDP 3074, types 20/21 and 30/31);
  service ID table.
- **[Laupetin/open-bitdemon-emulator](https://github.com/Laupetin/open-bitdemon-emulator)** (AGPL-3.0)
  — standalone Demonware backend; reference for service result layouts.
- **[Ezz-lol/boiii-free](https://github.com/Ezz-lol/boiii-free)** (GPL-3.0)
  — Demonware lobby/service emulation of the same SDK generation; bdMatchMakingInfo layout.
- **[Protarium-Network/bo2-wiiu-demonware](https://github.com/Protarium-Network/bo2-wiiu-demonware)**
  — NAT traversal packet format (introductions).
- **[skkuull/dwd3](https://github.com/skkuull/dwd3)** (GPL-3.0),
  **[jordam/demonbugger](https://github.com/jordam/demonbugger)**,
  **[hosseinpourziyaie/demonware-companion](https://github.com/hosseinpourziyaie/demonware-companion)**
  — Demonware reverse-engineering tools.

Everything specific to Diablo III (ticket layout, lobby handshake and crypto,
task reply format, the service/task map) was reverse-engineered from the game
binary; function and offsets are documented in the source comments.
