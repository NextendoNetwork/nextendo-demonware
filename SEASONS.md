# Seasons and their themes

In Diablo III a season is a number and a window, plus a **theme**. The game gets the number and window from `Seasons.txt`, and the theme as community events in `Config.txt` (see [PUBFILES.md](PUBFILES.md)). This page lists what each season's theme is, so the server can switch the right events on when the season rotates (`season_rotation` in `pubfiles.json`).

## Where the information comes from

- **Event flags:** d3hack's season mapping (`BuildSeasonEventMap` in its `config.cpp`), which was taken from real season configs. It covers seasons 14 to 22 and 24 to 37.
- **Theme names and mechanics:** the game and the Maxroll season-theme guide, and the Diablo Forums list of recycled themes for the repeats.
- **Seasons 38 and 39:** both announced. 38 is Ethereal Memory and 39 is Shades of the Nephalem, repeats of 24 and 22. Nothing later has been announced as far as this list is concerned, so nothing is guessed for it.

Themes by season. The event flags are d3hack's season mapping; the names and mechanics are the game's. Seasons 30 and later repeat six earlier themes in a fixed order.

| season | theme | events switched on |
|---|---|---|
| 1 to 13 | none | none |
| 14 | Season of Greed (doubled treasure goblins) | `DoubleGoblins` |
| 15 | Boon of the Horadrim (bounties give two caches) | `DoubleBountyBags` |
| 16 | Season of Grandeur | `RoyalGrandeur` |
| 17 | Season of Nightmares | `LegacyOfNightmares` |
| 18 | Season of the Triune | `TriunesWill` |
| 19, 35 | Eternal Conflict (Pandemonium killstreak buff) | `Pandemonium` |
| 20, 31, 37 | Forbidden Archives (any Kanai's Cube power in any slot) | `KanaiPowers` |
| 21 | Trials of the Tempests | `TrialsOfTempests` |
| 22, 33, 39 | Shades of the Nephalem (shadow clones, fourth cube slot) | `ShadowClones`, `FourthKanaisCubeSlot` |
| 23 | Disciples of Sanctuary (followers) | none, built into the game |
| 24, 32, 38 | Ethereal Memory | `EtherealItems` |
| 25, 30, 36 | Lords of Hell (soul shards) | `SoulShards` |
| 26 | Echoing Nightmare | `SwarmRifts` |
| 27, 34 | Light's Calling (sanctified items) | `SanctifiedItems` |
| 28 | Rites of Sanctuary | `DarkAlchemy` |
| 29 | Visions of Enmity | `NestingPortals` |

## Notes

- Seasons **1 to 13 had no theme**: Blizzard introduced themed seasons with Season 14, Season of Greed ("Season 14 First Look: Themed Seasons", June 2018, on news.blizzard.com). They are plain seasons, so nothing is switched on for them. Whether the game accepts a season number that low has not been tested here.
- Season **23** (followers) has a name but no event flag, because the change is built into the game.
- Season **28** is mapped to `DarkAlchemy` as in d3hack, although its theme is known by the name Rites of Sanctuary. The flag is what the game reads, so it is the part that matters.
- The themes are listed once, in `season_themes` in `pubfiles.json` (see [PUBFILES.md](PUBFILES.md#season-themes)). When a new season is announced, add a line there; no code change is needed, and the rotation picks it up in numeric order.
