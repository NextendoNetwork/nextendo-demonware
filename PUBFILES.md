# Publisher files

When a Diablo III session starts, the game asks the lobby for three small text files: the active season, the community events and multipliers, and an item blacklist. This page explains how they are produced and every setting you can change.

## How it works

| the game asks for | file the server writes (`D3_PUBFILES`, default `pubfiles/`) | content |
|---|---|---|
| `Config.txt` | `config.txt` | `Key "value"` lines: community events, multipliers, a few flags |
| `Seasons.txt` | `seasons_config.txt` | one `[Season N]` block with a start and end date |
| `Blacklist.txt` | `blacklist_config.txt` | `[GBID]` and `[SNO]` sections (empty by default) |

The files are generated from `pubfiles.json` (`D3_PUBFILES_CONFIG`). Generated files are overwritten on every start, so **edit `pubfiles.json`, never the files in `pubfiles/`** (the `pubfiles/` folder is not committed to git).

1. Edit `pubfiles.json`.
2. Run `server pubfiles` to regenerate the files and exit, or restart the server (it regenerates on start).
3. The lobby reads the files on every request, so no restart is needed after regenerating.
4. The game fetches the files when its online session starts, so players see a change **the next time they connect**, not mid-session.

If `pubfiles.json` does not exist, the server creates it with the defaults below.

To see what is being served: `http://<server>:8093/pubfiles/` lists the files and `/pubfiles/<name>` returns one (port `DASH_PORT`).

## pubfiles.json

| field | default | effect |
|---|---|---|
| `season` | `37` | Season number written to `seasons_config.txt`. |
| `season_start`, `season_end` | 2025-02-09 to 2036-02-09 | Season window. See [Dates](#dates). |
| `buff_start`, `buff_end` | 2023-09-16 to 2027-12-01 | Window in which the community buffs are active. Independent of the season. See [Dates](#dates). |
| `events` | `{}` | Community events, by name, `true` or `false`. See [Events](#events). |
| `legendary_find`, `gold_find`, `xp` | `"1.0"` | Multipliers, as text. The game reads them as floating-point numbers; `"1.0"` is the normal value. |
| `hero_publish_frequency_minutes` | `"30"` | `HeroPublishFrequencyMinutes` in `config.txt`. |
| `cross_platform_save_migration` | `true` | `EnableCrossPlatformSaveMigration`. |
| `seasonal_global_leaderboards` | `true` | `SeasonalGlobalLeaderboardsEnabled`. |
| `diablo4_advertisement` | `false` | `EnableDiablo4Advertisement`. |
| `update_version` | `"1"` | `UpdateVersion`. |

A fresh install has no event switched on, so the server behaves like the production configuration until you decide otherwise.

Example, a double-goblins weekend with 2x experience:

```json
{
  "season": 37,
  "events": { "DoubleGoblins": true, "DoubleBountyBags": true },
  "xp": "2.0"
}
```

Fields you leave out keep their defaults.

### Events

Each entry in `events` becomes `CommunityBuff<Name> "1"` (on) or `"0"` (off) in `config.txt`. The game recognizes exactly these 21 names, and the server always writes all of them (a name you leave out is written as `"0"`, because the game reads the value and a missing line is not the same as an off one):

`DoubleGoblins`, `DoubleBountyBags`, `RoyalGrandeur`, `LegacyOfNightmares`, `TriunesWill`, `Pandemonium`, `KanaiPowers`, `TrialsOfTempests`, `SeasonOnly`, `ShadowClones`, `FourthKanaisCubeSlot`, `EtherealItems`, `SoulShards`, `SwarmRifts`, `SanctifiedItems`, `DarkAlchemy`, `ParagonCap`, `NestingPortals`, `EasterEggWorld`, `DoubleRiftKeystones`, `DoubleBloodShards`.

A name that is not in this list is ignored. What each event does in game is defined by the game itself, not by this server; the names are the game's own.

Two cautions, both from the d3hack source and not yet tested here:

- `SeasonOnly`: a comment in d3hack's built-in configuration says any value other than `1` (or leaving the line out) crashes on item drop. d3hack's own generated file writes `0`, and this server has run with `0`, so leave it off unless you are testing it.
- `ParagonCap`: same advice, leave it off.

### Dates

The game is strict about the format `Www, DD Mon YYYY hh:mm:ss GMT`, for example `Sat, 09 Feb 2025 00:00:00 GMT`. The day of the month must have **two digits** (`09`, not `9`) or the file cannot be parsed. Season dates are read from `seasons_config.txt`; the buff window uses the same format in `config.txt`.

## Serving other files

The lobby looks a requested file up in the `pubfiles/` folder by name, ignoring case, so any file you put there is served to a client that asks for it by that name. In every logged session the game asked for exactly four files: the three generated ones and `update-1.cpk`, which is answered as absent.

### Challenge Rifts

These are not served by this server. The game gets the weekly Challenge Rift data (two protobuf messages, `ChallengeData` and `WeeklyChallengeData`) through a different request than the publisher files: in the logged sessions it never asked for `challengerift_config.dat`, and the requests this server answers with an empty success (services 68/3, 29/11, 27/2, 23/1 and 10/10) have not been identified as the rift request.

d3hack does not fetch them from a server either. It hooks the game's result callback and feeds it files from `sd:/config/d3hack-nx/rift_data/` (`challengerift_config.dat` and numbered `challengerift_NN.dat`), forcing the start time to 0 and the end time far into the future so a week never expires. To serve them from here, the request would first have to be found by opening the Challenge Rift menu while connected and reading the log for unhandled tasks.

## Not supported yet

- **Blacklist entries.** `blacklist_config.txt` is always written with two empty sections. d3hack documents the line formats as `[GBID]` with `<GBIDName>,<allowDrop>` and `[SNO]` with `<SNOGroup>,<SNOName>,<allowSpawn>` (strings without quotes). What the `0`/`1` value means has not been confirmed on a running game, so there is no `pubfiles.json` field for it yet. Editing the generated file by hand does not last, because it is rewritten on every start.
- **More than one season block**, and **any `Config.txt` key beyond the ones above.** The keys above are the ones d3hack itself writes when it plays offline, and they are all the game has been observed to need.
