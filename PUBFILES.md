# Publisher files

When an online session starts, Diablo III asks the lobby for three small text files: the season, the community events with their multipliers, and an item blacklist. The server generates them from `pubfiles.json`.

## How it works

| the game asks for | generated file (`D3_PUBFILES`, default `pubfiles/`) | content |
|---|---|---|
| `Config.txt` | `config.txt` | `Key "value"` lines: events, multipliers, flags |
| `Seasons.txt` | `seasons_config.txt` | one `[Season N]` block with a start and end date |
| `Blacklist.txt` | `blacklist_config.txt` | `[GBID]` and `[SNO]` sections, empty for now |

Edit `pubfiles.json` (`D3_PUBFILES_CONFIG`). The season and event files are generated from it on every request, so a change needs no restart and no regeneration: players see it the next time they connect, because the game fetches the files when a session starts. The shipped file lists every setting with its default and explains each one in comments; comments (`//` and `/* */`) are allowed in it. A setting you leave out keeps its default, so your own file can be short, and a missing `pubfiles.json` is created from the shipped one.

`server pubfiles` writes copies of the generated files to `pubfiles/` (`D3_PUBFILES`) for you to inspect; the game does not read them.

Check what is served at `http://<server>:8093/pubfiles/<name>`, for example `config.txt`, `seasons_config.txt` or `challengerift_config.dat`. This shows the current generated content, including rotation and rifts. The listing at `/pubfiles/` only shows the files on disk.

## Settings

| field | default | effect |
|---|---|---|
| `season` | `39` | Season number: the latest known one. Ignored while rotation is on. |
| `season_start`, `season_end` | 2020-01-01, 2050-01-01 | Season window, wide enough that it never ends ([dates](#dates)). |
| `buff_start`, `buff_end` | 2023-09-16, 2027-12-01 | Window in which the community events apply. |
| `season_theme` | `true` | Switch on the served season's theme events automatically ([season themes](#season-themes)). |
| `events` | all off | Extra community events on top of the theme, by name ([events](#events)). |
| `legendary_find`, `gold_find`, `xp` | `"1.0"` | Multipliers, as text. `"1.0"` is normal. |
| `hero_publish_frequency_minutes` | `"30"` | `HeroPublishFrequencyMinutes`. |
| `cross_platform_save_migration` | `true` | `EnableCrossPlatformSaveMigration`. |
| `seasonal_global_leaderboards` | `true` | `SeasonalGlobalLeaderboardsEnabled`. |
| `diablo4_advertisement` | `false` | `EnableDiablo4Advertisement`. |
| `update_version` | `"1"` | `UpdateVersion`. |
| `season_rotation` | off | Advance the season every month ([below](#season-rotation)). |
| `season_themes` | 14 to 39 | The events of each season: the one place they are listed ([below](#season-themes)). |
| `challenge_rifts` | `weekly` | Weekly Challenge Rifts ([below](#challenge-rifts)). |

Example, a file that keeps season 39 and its theme, adds doubled goblins and 2x experience:

```json
{ "events": { "DoubleGoblins": true }, "xp": "2.0" }
```

### Events

Each event becomes `CommunityBuff<Name> "1"` or `"0"` in `config.txt`. The game knows exactly these 21 names, and the server always sends all of them, `"0"` for any that is off (a missing line is not the same as an off one). Unknown names are ignored. What an event does is defined by the game.

`DoubleGoblins` `DoubleBountyBags` `RoyalGrandeur` `LegacyOfNightmares` `TriunesWill` `Pandemonium` `KanaiPowers` `TrialsOfTempests` `SeasonOnly` `ShadowClones` `FourthKanaisCubeSlot` `EtherealItems` `SoulShards` `SwarmRifts` `SanctifiedItems` `DarkAlchemy` `ParagonCap` `NestingPortals` `EasterEggWorld` `DoubleRiftKeystones` `DoubleBloodShards`

What is on is the season's theme (next section) **plus** whatever you set to `true` in `events`. So `events` only holds extras; leave them all `false` to get exactly the season's theme. The shipped file lists all 21, each with the seasons that use it.

Only `DarkAlchemy`, `KanaiPowers`, `NestingPortals` and `SwarmRifts` have been run in live co-op. Leave `SeasonOnly` and `ParagonCap` off: d3hack notes that any `SeasonOnly` value other than `1` crashes on item drop, yet its own generated file writes `0` and so does this server. That conflict is unresolved, and `ParagonCap` is untested.

### Season themes

A season is a number and window plus a theme, which the game receives as the events above. **`season_themes` in `pubfiles.json` is the one place each season's events are listed.** With `season_theme` on (the default), the served season's events are switched on automatically, for a fixed `season` and for a rotating one alike. Turn `season_theme` off to get no theme and control every event yourself in `events`.

The shipped file lists seasons 14 to 39, the ones that had a theme ([SEASONS.md](SEASONS.md) names them; seasons 1 to 13 had none). To add a season, add a line; to change one, edit its line. Give the events as a list of names, or as an object with `0` or `1` for each. An empty list is a season with no theme.

```json
"season_themes": {
  "40":  ["SanctifiedItems"],
  "41":  { "SanctifiedItems": 1, "SoulShards": 1, "Pandemonium": 0 },
  "420": []
}
```

The `"template"` entry in the shipped file has all 21 events at `0`. It is not a season number, so it is ignored: copy it to a number and change the `0`s you want to `1`.

### Season rotation

With rotation on, the season advances every month through **the seasons listed in `season_themes`**, sorted by number, looping back to the first after the last and skipping gaps. With 39 and 420 listed, the month after 39 is 420. `season` and `season_start` are then ignored, the season's theme follows automatically, and the files are generated on every request, so it rolls over without a restart. Only the start date moves: the end stays at `season_end` (2050 by default), so a season never ends under a connected player, and the season changes when the game next connects. Each served file is logged as `[D3 Season]` (and rifts as `[D3 Rift]`).

```json
"season_rotation": { "enabled": true, "anchor": "2026-09" }
```

| field | default | meaning |
|---|---|---|
| `enabled` | `false` | Turn rotation on. |
| `anchor` | `"2026-09"` | Month (`YYYY-MM`) in which the first listed season runs. |
| `first`, `last` | `0`, `0` | Optional inclusive bounds on which seasons take part; `0` is no bound. |
| `interval_seconds` | `0` | For testing: seconds per season instead of a month, counted from `anchor` (`YYYY-MM-DD` works too). |

Before enabling it: each month the season changes, so characters created in the season leave it, as at a real season end (use a fixed `season` for a stable seasonal character). Season numbers outside the ones the game shipped with have not been tested.

### Dates

The format is `Www, DD Mon YYYY hh:mm:ss GMT`, for example `Sat, 09 Feb 2025 00:00:00 GMT`. The day must have **two digits** (`09`, not `9`) or the file cannot be parsed.

## Challenge Rifts

When the Challenge Rift menu opens (it needs a high character level), the game asks for `challengerift_config.dat` (challenge number, start, end, hash) and then `challengerift_<number>.dat` (the weekly rift). d3hack feeds the game the same two files from `sd:/config/d3hack-nx/rift_data/`; this server can serve them instead.

1. Copy `challengerift_config.dat` and the `challengerift_NN.dat` files from d3hack's release zip (`config/d3hack-nx/rift_data/`) into `D3_RIFTDATA` (default `riftdata/`). They are captured game data, not part of this repository.
2. That is all; files are read on every request. Without a `challengerift_config.dat` there, the requests are answered as absent.

The server rewrites the config's times as d3hack does (start 0, end 2038), since captured configs carry a week long past. The number it puts in the config picks the file the game asks for next, mapped onto your files and wrapping to the first after the last.

```json
"challenge_rifts": { "mode": "weekly" }
```

| `mode` | behaviour |
|---|---|
| `weekly` (default) | The number advances every week, cycling through your files. |
| `fixed` | Always the file for `number`: `{ "mode": "fixed", "number": 3 }`. |
| `random` | A different file on each request. |

Untested on a live game: the request names come from d3hack's source, and the menu's level requirement has prevented a real test.

Any other file in `pubfiles/` is served by name (case-insensitive) to a client that asks for it. In the logged sessions the game asked only for the three generated files and `update-1.cpk`, which is answered as absent.

## Not supported yet

- **Blacklist entries.** d3hack documents `[GBID]` lines as `<GBIDName>,<allowDrop>` and `[SNO]` lines as `<SNOGroup>,<SNOName>,<allowSpawn>` (no quotes), but what the `0`/`1` means is unconfirmed on a live game, so there is no setting yet. Hand edits do not last.
- **More than one season block**, and **`Config.txt` keys beyond those above**: the keys here are the ones d3hack writes offline and all the game has been seen to need.
