# CTR implementation update (2026-09-23)

The current server includes CTR asynchronous party matchmaking, friend presence, optional UDP relay, persistent marketplace balances and item ownership, catalog purchases, Pit Stop refreshes, and original race reward tables. Marketplace and refresh behavior have been verified in-game. Economy writes are persisted atomically per account.

Wumpa Challenges remain incomplete: Window Shopping has persistent completion and a 25-coin reward, with duplicate completion notifications suppressed; other challenge predicates and client balance notification behavior still need work. Leaderboard service 4 remains unimplemented. The testing starter balance remains 10,000 coins pending an explicitly requested reset.

The older Diablo III assessment below is historical and is not a current audit of CTR. In particular its statements about all state being volatile and the absence of a relay do not describe the new CTR economy and relay implementation.

---
# What is missing, and what is unknown

What this server does today, what a complete Diablo III server would also do, and what we do not know yet. Each point says how it is known: **code** (read from this repository), **log** (seen in a live session), **binary** (from the game's own call sites), or **guess**.

## What is implemented

Auth and tickets; the encrypted lobby; NAT discovery and introductions; public-game matchmaking (create, update, delete, find, player counts); friend name lookups and per-player user data; server time; the publisher files (`Config.txt`, `Seasons.txt`, `Blacklist.txt`); optional weekly Challenge Rifts; presence reporting to nextendo-account. Of the roughly 30 remote-task call sites in the game binary, 10 are answered for real: 12/6, 12/9, 10/21, 21/1, 21/2, 21/3, 21/5, 21/12, 29/1 and 29/4 (binary, code).

## Missing: remote tasks answered with an empty success

The game calls these, and the server accepts each one and returns nothing (code, binary). Service names are guesses except 10, 12 and 21.

| service (guess) | tasks | seen live | what a full server would do |
|---|---|---|---|
| 4 stats / leaderboards | 1, 4, 5, 11, 13 | 4/1 | store scores, return leaderboards (guess) |
| 6 messaging | 14 | no | in-game mail; the season-swap mailbox flow needs it (guess) |
| 10 storage | 10, 12, 13, -1 | 10/10 | keep uploaded hero data (about 2.5 KB) and return it |
| 23 counter | 1 | 23/1 | shared counters |
| 27 DML | 2 | 27/2 | `getUserData` |
| 29 user data | 5, 8, 11 | 29/11 | the rest of the user-data set (29/1 and 29/4 work) |
| 67 event log | 6 | no | telemetry sink |
| 68 rich presence | 3, 5, 7 | 68/3 | now implemented (`richpresence.go`) from the layout in the CTR pull request and our own log; unconfirmed in a live game |

Effect in game: leaderboards show but are empty, hero uploads go nowhere, mail is unavailable (log, code).

## Missing: state and identity

- **Nothing persists.** Matchmaking sessions and per-player user data live in memory and are lost on restart; only login tickets and identity files are written to disk (code).
- **No stable user IDs.** Every ticket carries user id 1, and the lobby connection id is a counter (code). Stable per-account IDs are still open in the notes.
- **No real subscription check.** `nso_subscription_status` is always `1` (code).

## Missing: matchmaking

- **No session expiry.** `updated` is recorded and never read, so a session lives until its host disconnects. A stale host can be found and never joined (code; this matches a failure seen live, see Unknowns).
- **Search filters are ignored.** `findSessions` reads only query, start and max, and returns every other player's session, up to 50 (code).
- **No player list per session, no region or skill logic** (code).
- **Peer-to-peer only.** The server introduces players and relays nothing of the game itself (code).

## Missing: friends and presence

- **Console friends.** Names come from `baas-proxy`'s log (local stack only) or, new, from nextendo-account's `/internal/resolve` (`accountlookup.go`). The account lookup is tested only against a stand-in service, never with real friend ids (code).
- **Only online players resolve** in `getUserNames`; offline friends do not (code).
- **The "friends online" count on devices** is still open: presence has to reach the account service that builds the console's friend list, which is private (notes).

## Missing: hardening

- **The lobby accepts an all-zero key.** It tries recent tickets and an all-zero key for older ones, and anyone can derive the zero key. A client with no ticket can therefore complete the handshake, unidentified, and call tasks such as matchmaking (code).
- **No rate limits, no bans or moderation, no anti-abuse** (code).
- **Account gates fail open** when the account service is unreachable, by design; `NEXTENDO_REQUIRE_ACCOUNT` defaults to off (code).
- **IPv4 only** for NAT (code).

## Missing: content and settings

- **Blacklist entries.** The file is always sent empty (code); see Unknowns.
- **`update-1.cpk`.** The game asks for this content update on every login and is answered "absent" (log). A real server may ship patches through it.
- **Only one season block** in `Seasons.txt` (code).

## Unknown

- **Config keys.** Whether the game reads `Config.txt` keys beyond the 26 we send. The game has its own key list; d3hack's may be shorter (guess). The parser is at `0x6429C` and `0x65314` in the binary; tracing it would settle this.
- **Blacklist values.** What the `0`/`1` in `[GBID]` and `[SNO]` lines means (d3hack documents the line format only).
- **Date range.** The game may keep dates in 32 bits (d3hack limits its rift end to 19 Jan 2038). A season end of 1 Jan 2050 was served during the console trouble, but deleting the save fixed it on the old, unchanged files, so the date was probably not the cause. The server still clamps dates past the limit as a precaution.
- **Season and events.**
  - Whether the game accepts season numbers below 14 or above 39, and whether older builds accept later seasons at all: builds 2.7.6 and 2.7.7 are only tested with season 37.
  - The exact rule behind "not a seasonal hero". Seen once: after serving seasons 37, 69, 39 and 37 in turn, a console refused every new seasonal hero until its save was deleted, on files that had worked before; the server build and files were ruled out. Most likely the save records the season and a lower one is refused; not confirmed.
  - Whether `SeasonOnly` set to `0` crashes on drop: d3hack's comment says yes, its own file and ours write `0`.
  - What `ParagonCap` does.
  - How all 21 events behave together (only four have run in live co-op).
  - Whether the game re-fetches the config mid-session, which decides if a season change reaches a connected player.
- **XP multiplier.** Its exact meaning and any upper limit (`"1.0"` is normal; `"6000.0"` is untested for effect).
- **Challenge Rifts.** The request names come from d3hack's source, not a live capture. Whether the game accepts our rewritten config (times and hash) is untested. The menu needs a high character level.
- **Quick-match refusals.** A joiner once rejected a found game instantly when one session attribute differed (a flag set 1 versus 0). The cause was never identified.
- **Formats of the empty services** above: request and reply layouts for leaderboards, hero upload, mail and rich presence.
- **Other game versions.** Only the Switch title `01001B300B9BE000`, versions 2.7.6 and 2.7.7, protocol versions 200 to 220, have been seen.
- **Load.** One process, in-memory state; nothing is known about behavior with many players.

## How to close the gaps

1. Trace the config parser and the service-4, 10, 6 and 68 handlers in the game binary with Ghidra (the method is in `NOTES.md`). This answers the config keys and the missing formats.
2. Add a small logger to d3hack that records service, task and arguments for every task, so the missing formats can be read from a real session.
3. Test on a real device: the four unverified season and rift behaviors above, and a stale-session quick match.
