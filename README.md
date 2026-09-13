# diablo-3

Game server for **Diablo III** on Nintendo Switch, for [Nextendo Network](https://nextendo.network). Source only — no binaries, no certs, no game assets. Not affiliated with Blizzard, Activision, Demonware or Nintendo.

Diablo III does not use NEX: its online layer is **Demonware**. This server speaks it end to end — auth, the encrypted lobby, remote tasks, matchmaking, NAT discovery — and plugs into the Nextendo stack like the other game servers: a route in sni-router, the account gates and presence of nextendo-account, and `/api/stats` for nextendo-dashboard.

## What works

- Login and "connected to the Diablo network"; season and community events served from `pubfiles.json`
- Public games: create, find, join, player counts, NAT introductions
- Co-op between a Switch and an emulator (tested: Switch, Citron 2.7.7)
- Friend lookups inside the game, and presence reported to nextendo-account

## Build

    go build -o server .

Standard library only. Builds for Windows and Linux (amd64, arm64).

## Run

See `example.env`.

| port | what |
|---|---|
| 8460 TCP (TLS) | Demonware auth, behind sni-router (`*.demonware.net` → `BACKEND_D3`) |
| 3074 TCP | lobby |
| 3074 UDP | NAT discovery and introductions |
| 8093 HTTP | `/api/stats`, `/healthz`, `/pubfiles/` |

DNS must send these hosts to the stack; the game must never reach the real Demonware:

    crimson-switch-auth3.prod.demonware.net   crimson-switch-lobby.prod.demonware.net
    crimson-switch-auth3.cert.demonware.net   crimson-switch-lobby.cert.demonware.net
    crimson-switch-auth3.dev.demonware.net    crimson-switch-lobby.dev.demonware.net
    stun.us.demonware.net  stun.eu.demonware.net  stun.jp.demonware.net  stun.au.demonware.net

Regenerate the publisher files without restarting (the lobby reads them on every request):

    server pubfiles

## Credits

- **[Nextendo Network](https://nextendo.network)** ([NextendoNetwork](https://github.com/NextendoNetwork))
  — the Switch online stack this server plugs into, and the game-server pattern
  (gates, presence, dashboard) it follows.
- **[D3Hack](https://github.com/god-jester/D3StudioFork)** by **jester** — the publisher-file
  formats (`Config.txt`, `Seasons.txt`, `Blacklist.txt`).

Demonware implementations for other titles consulted; protocol facts were read from them
and **reimplemented** here in Go, no code was copied:

- **[project-bo4/shield-development](https://github.com/project-bo4/shield-development)** — STUN/NAT-discovery packet format.
- **[Laupetin/open-bitdemon-emulator](https://github.com/Laupetin/open-bitdemon-emulator)** — service result layouts.
- **[Ezz-lol/boiii-free](https://github.com/Ezz-lol/boiii-free)** — bdMatchMakingInfo layout.
- **[Protarium-Network/bo2-wiiu-demonware](https://github.com/Protarium-Network/bo2-wiiu-demonware)** — NAT traversal introductions.
