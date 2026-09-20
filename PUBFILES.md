# Publisher files

When an online session starts, Diablo III asks the lobby for three small text files: the season, the community events with their multipliers, and an item blacklist. The server generates them from `pubfiles.json`.

## How it works

| the game asks for | generated file (`D3_PUBFILES`, default `pubfiles/`) | content |
|---|---|---|
| `Config.txt` | `config.txt` | `Key "value"` lines: events, multipliers, flags |
| `Seasons.txt` | `seasons_config.txt` | one `[Season N]` block with a start and end date |
| `Blacklist.txt` | `blacklist_config.txt` | `[GBID]` and `[SNO]` sections, empty for now |

Edit `pubfiles.json` (`D3_PUBFILES_CONFIG`), never the generated files: they are rewritten on every start, and `pubfiles/` is not committed. Then run `server pubfiles` (regenerate and exit) or restart. The lobby reads the files on every request, and the game fetches them when a session starts, so players see a change the next time they connect. A missing `pubfiles.json` is created with the defaults.

Check what is served at `http://<server>:8093/pubfiles/<name>`, for example `config.txt`, `seasons_config.txt` or `challengerift_config.dat`. This shows the current generated content, including rotation and rifts. The listing at `/pubfiles/` only shows the files on disk.

## Settings

Fields you leave out keep their defaults.

| field | default | effect |
|---|---|---|
| `season` | `37` | Season number. |
| `season_start`, `season_end` | 2025-02-09, 2036-02-09 | Season window ([dates](#dates)). |
| `buff_start`, `buff_end` | 2023-09-16, 2027-12-01 | Window in which the community events apply. |
| `events` | `{}` | Community events by name, `true` or `false` ([events](#events)). |
| `legendary_find`, `gold_find`, `xp` | `"1.0"` | Multipliers, as text. `"1.0"` is normal. |
| `hero_publish_frequency_minutes` | `"30"` | `HeroPublishFrequencyMinutes`. |
| `cross_platform_save_migration` | `true` | `EnableCrossPlatformSaveMigration`. |
| `seasonal_global_leaderboards` | `true` | `SeasonalGlobalLeaderboardsEnabled`. |
| `diablo4_advertisement` | `false` | `EnableDiablo4Advertisement`. |
| `update_version` | `"1"` | `UpdateVersion`. |
| `season_rotation`, `season_themes` | off | Monthly rotation ([below](#season-rotation)). |
| `challenge_rifts` | `weekly` | Weekly Challenge Rifts ([below](#challenge-rifts)). |

Example, doubled goblins and bounty bags with 2x experience:

```json
{ "events": { "DoubleGoblins": true, "DoubleBountyBags": true }, "xp": "2.0" }
```

### Events

Each event becomes `CommunityBuff<Name> "1"` or `"0"` in `config.txt`. The game knows exactly these 21 names, and the server always sends all of them, `"0"` for any you leave out (a missing line is not the same as an off one). Unknown names are ignored. What an event does is defined by the game.

`DoubleGoblins` `DoubleBountyBags` `RoyalGrandeur` `LegacyOfNightmares` `TriunesWill` `Pandemonium` `KanaiPowers` `TrialsOfTempests` `SeasonOnly` `ShadowClones` `FourthKanaisCubeSlot` `EtherealItems` `SoulShards` `SwarmRifts` `SanctifiedItems` `DarkAlchemy` `ParagonCap` `NestingPortals` `EasterEggWorld` `DoubleRiftKeystones` `DoubleBloodShards`

Only `DarkAlchemy`, `KanaiPowers`, `NestingPortals` and `SwarmRifts` have been run in live co-op. Leave `SeasonOnly` and `ParagonCap` off: d3hack notes that any `SeasonOnly` value other than `1` crashes on item drop, yet its own generated file writes `0` and so does this server. That conflict is unresolved, and `ParagonCap` is untested.

### Season rotation

A season is a number and window plus a theme, which the game receives as community events ([SEASONS.md](SEASONS.md) lists them; seasons 1 to 13 have none). With rotation on, the season advances every month through **all the seasons the server knows**: the built-in 14 to 39 plus any you add, sorted by number, looping back to the first after the last and skipping gaps. `season` and `season_start` are then ignored, and the files are generated on every request, so it rolls over without a restart. Only the start date moves: the end stays at `season_end` (2036 by default), so a season never ends under a connected player, and the season changes when the game next connects. Each served file is logged as `[D3 Season]` (and rifts as `[D3 Rift]`).

```json
"season_rotation": { "enabled": true, "anchor": "2026-09" }
```

| field | default | meaning |
|---|---|---|
| `enabled` | `false` | Turn rotation on. |
| `anchor` | `"2026-09"` | Month (`YYYY-MM`) in which the first season of the list runs. |
| `theme_events` | `true` | Also switch on the current season's events. Your own `events` stay on. |
| `first`, `last` | `0`, `0` | Optional inclusive bounds on which seasons take part; `0` is no bound. |
| `interval_seconds` | `0` | For testing: seconds per season instead of a month, counted from `anchor` (`YYYY-MM-DD` works too). |

**To add a season, no code change:** give it a theme in `season_themes`, as a list of the events that are on or as an object with `0` or `1` for each. An entry also replaces a built-in theme. An empty list is a season with no theme.

```json
"season_themes": {
  "40":  ["SanctifiedItems"],
  "41":  { "SanctifiedItems": 1, "SoulShards": 1, "Pandemonium": 0 },
  "420": []
}
```

Either way the game always gets all 21 events. The shipped `pubfiles.json` has a `"template"` entry with all 21 set to `0`; it is not a season number, so it is ignored. Copy it to a number and change the `0`s you want to `1`. To play seasons 1 to 13, add them the same way (`"1": []`).

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
