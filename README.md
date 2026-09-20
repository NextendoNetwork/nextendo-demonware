# diablo-3

Game server for **Diablo III** on Nintendo Switch, for [Nextendo Network](https://nextendo.network). Source only — no binaries, no certs, no game assets. Not affiliated with Blizzard, Activision, Demonware or Nintendo.

Diablo III does not use NEX: its online layer is **Demonware**. This server speaks it end to end (auth, the encrypted lobby, remote tasks, matchmaking, NAT discovery) and plugs into the Nextendo stack like the other game servers: a route in sni-router, the account gates and presence of nextendo-account, and `/api/stats` for nextendo-dashboard.

## What works

- Login and "connected to the Diablo network".
- Season and community events served from `pubfiles.json` ([PUBFILES.md](PUBFILES.md)), with optional monthly season rotation and weekly Challenge Rifts (see [Optional features](#optional-features)).
- Public games: create, find, join, player counts, NAT introductions.
- Co-op between a Switch and an emulator (tested: CFW Switch, Citron 2.7.7).
- Friend lookups inside the game, and presence reported to nextendo-account.

## Requirements

This server runs behind the rest of the Nextendo stack. Two of the pieces need changes that are shipped as separate pull requests.

| component | needed? | what it must provide |
|---|---|---|
| **sni-router** | required | A route sending `crimson-switch-auth3.*.demonware.net` to `BACKEND_D3` (default `127.0.0.1:8460`), with the PROXY protocol v1 header (`SNI_PROXY_PROTOCOL=1` on the router, `NEXTENDO_PROXY_PROTOCOL=1` here) so the auth sees the player's real address. Neither is in sni-router's `main` yet. The lobby (3074) does not go through the router. |
| **nextendo-account** | for account gates and presence | `POST /internal/online-check` and `POST /internal/presence-batch`, authenticated with `X-Internal-Key`. Already in `main`. With `NEXTENDO_REQUIRE_ACCOUNT=0`, an unreachable account service is only logged. |
| **nextendo-dashboard** | optional | A `d3` source polling `/api/stats` on port 8093 (`DASH_D3_URL`, `DASH_D3_TOKEN`). Without it the server works but is not on the shared dashboard. |
| **DNS** | required | `crimson-switch-auth3.*.demonware.net`, `crimson-switch-lobby.*.demonware.net` and `stun.{us,eu,jp,au}.demonware.net` must resolve to the stack; the game must never reach the real Demonware. A console uses Atmosphere hosts entries, an emulator the resolver of its host: exact lines in [Hosts entries](#hosts-entries). |
| **TLS certificate** | required | A certificate and key for the three `crimson-switch-auth3.*.demonware.net` names, from a CA your clients trust (`CERT_FILE`, `KEY_FILE`). Yours to provide; none is shipped and none is committed. |
| **Game update on the client** | required | The client must run the current game update, or it finds updated games but can never join them. |

## Hosts entries

Diablo III must reach these names on your stack and never on the real Demonware. The auth names go to the machine running sni-router; the lobby and STUN names go straight to the diablo-3 server (the lobby does not pass through the router), which is the address you set as `NEXTENDO_HOST`. They can be the same machine.

```
<ROUTER_IP> crimson-switch-auth3.prod.demonware.net
<ROUTER_IP> crimson-switch-auth3.cert.demonware.net
<ROUTER_IP> crimson-switch-auth3.dev.demonware.net
<D3_IP>     crimson-switch-lobby.prod.demonware.net
<D3_IP>     crimson-switch-lobby.cert.demonware.net
<D3_IP>     crimson-switch-lobby.dev.demonware.net
<D3_IP>     stun.us.demonware.net
<D3_IP>     stun.eu.demonware.net
<D3_IP>     stun.jp.demonware.net
<D3_IP>     stun.au.demonware.net
```

- **Switch (Atmosphere):** add the lines to the end of both `/atmosphere/hosts/emummc.txt` and `/atmosphere/hosts/sysmmc.txt` (the last matching line wins), make sure `enable_dns_mitm = u8!0x1` is set under `[atmosphere]` in `/atmosphere/config/system_settings.ini`, and reboot the console.
- **Emulator:** the same lines in the hosts file of the machine whose resolver the emulator uses, or in the emulator's own redirect if it has one.
- Without the STUN lines the console sends its NAT probes to Activision, and online games stay local only.

## Install

These steps follow the generic [deployment guide](https://github.com/NextendoNetwork/nextendo-docs/blob/main/DEPLOYMENT.md); every value is a placeholder for your own.

1. **Prerequisites.** Go 1.23 or newer, and a running nextendo-account and sni-router.
2. **Build.**

       git clone https://github.com/NextendoNetwork/diablo-3.git
       cd diablo-3
       go build -o server .

   The standard library only. `go test ./...` runs the tests.
3. **Configure.** `cp example.env .env` and edit it; every variable is documented in that file. At minimum:
   - `NEXTENDO_HOST`: the IPv4 address players reach this server on;
   - `NEXTENDO_ACCOUNT_URL`: your nextendo-account;
   - `NEXTENDO_INTERNAL_KEY` and `NEXTENDO_SECRET`: the **same values** nextendo-account uses, or the gates and presence calls are refused;
   - `NEXTENDO_PROXY_PROTOCOL=1`, since sni-router sends the PROXY header;
   - `NEXTENDO_REQUIRE_ACCOUNT=1` once your accounts are live (`0` only logs);
   - `DASH_TOKEN`: a random value.
4. **Certificate.** Point `CERT_FILE` and `KEY_FILE` at the certificate your deployment uses for its game servers. sni-router passes the TLS through untouched, so this is the certificate the client sees. It must cover `crimson-switch-auth3.prod.demonware.net`, `.cert.` and `.dev.`, and come from the CA your clients already trust.
5. **sni-router.** Set `BACKEND_D3=127.0.0.1:8460` (or wherever the auth port is) and `SNI_PROXY_PROTOCOL=1`, then restart it.
6. **DNS and firewall.** Add the [hosts entries](#hosts-entries), and open the ports below on the host firewall before the first run: a dismissed firewall prompt leaves a silent block rule.
7. **Dashboard (optional).** On nextendo-dashboard set `DASH_D3_URL=http://127.0.0.1:8093` and `DASH_D3_TOKEN` to your `DASH_TOKEN`.
8. **Run and check.**

       ./server

   The log shows `[D3 Auth] listening HTTPS`, `[D3 Lobby] listening TCP` and `[D3 NAT] listening UDP`. `curl http://127.0.0.1:8093/healthz` returns 200, and a game logging in logs `[D3 Auth] ... -> 200 code=700`.

| port | what |
|---|---|
| 8460 TCP (TLS) | Demonware auth, behind sni-router |
| 3074 TCP | lobby (`LOBBY_PORTS` also opens 3075 to 3080) |
| 3074 UDP | NAT discovery and introductions |
| 8093 HTTP | `/api/stats?key=DASH_TOKEN`, `/healthz`, `/pubfiles/` |

## Publisher files

The season, the community events with their multipliers, and the item blacklist come from `pubfiles.json`. Every setting is explained in [PUBFILES.md](PUBFILES.md) and the season themes in [SEASONS.md](SEASONS.md).

    server pubfiles    # regenerate Config.txt / Seasons.txt / Blacklist.txt and exit

The lobby reads them on every request, so a change needs no restart; players see it the next time they connect.

## Optional features

Both are off or inert until you set them up. Neither has been tried on a running game yet; the code and settings are covered by tests.

- **Monthly season rotation.** The season advances every month through all the seasons the server knows (14 to 39 built in, more added in `pubfiles.json`) with each season's theme switched on, then starts over. Enable it with `"season_rotation": { "enabled": true }`. See [PUBFILES.md](PUBFILES.md#season-rotation).
- **Weekly Challenge Rifts.** Put `challengerift_config.dat` and the `challengerift_NN.dat` files from d3hack's release zip (`config/d3hack-nx/rift_data/`) in `D3_RIFTDATA` (default `riftdata/`) and the server serves one per week, looping back to the first. The files are captured game data and are not in this repository. See [PUBFILES.md](PUBFILES.md#challenge-rifts).

## Development notes

- Runtime state (`sessions/`, `pubfiles/`, `dumps/`) is created next to the binary and ignored by git, as is `riftdata/`.
- `D3_DUMPS=<dir>` records raw auth bodies and lobby frames; `D3_VERBOSE=1` logs every decrypted lobby message.
- For co-op between a console running d3hack and a stock peer, keep d3hack's cheat sections off and set `MaxParagonLevel = 20000`. Gameplay-changing patches on one side desync the session and the game drops the join a few seconds later. Community buffs come from this server instead.

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

Everything specific to Diablo III (ticket layout, lobby handshake and crypto, task reply format, the service/task map) was reverse-engineered from the game binary; the addresses and layouts are documented in the source comments.
