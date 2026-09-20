package main

// Monthly season rotation.
//
// A season is two things to the game: the season number and window in
// Seasons.txt, and its theme, which the game receives as community-event flags
// in Config.txt (d3hack's "season theme mapping" does exactly this offline).
// With rotation on, both follow the calendar: each month the season advances to
// the next one in the list of known seasons (the built-in ones plus those added
// in "season_themes", sorted by number) and wraps to the first after the last.
// The theme events of the current season are switched on. The two files are
// generated per request so a running server rolls over at the month boundary
// without a restart.

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// seasonRotation is the "season_rotation" block of pubfiles.json.
type seasonRotation struct {
	Enabled bool `json:"enabled"`
	// First and Last optionally bound which of the known seasons take part
	// (inclusive); 0 means no bound.
	First uint32 `json:"first"`
	Last  uint32 `json:"last"`
	// Anchor is the month ("YYYY-MM") in which the first season of the list runs.
	Anchor string `json:"anchor"`
	// ThemeEvents switches on the season's theme events (see themeOf).
	ThemeEvents bool `json:"theme_events"`
	// IntervalSeconds, when above 0, replaces the month with this many seconds
	// per season, counted from Anchor. It exists for testing the rotation.
	IntervalSeconds int `json:"interval_seconds"`
}

// seasonEvents is a season's theme in pubfiles.json: either a list of the events
// that are on, ["Pandemonium", "SoulShards"], or an object with a 0 or 1 (or
// false/true) for every event, {"Pandemonium": 1, "SoulShards": 0, ...}. Both
// mean the same thing; the object form lets a season show all its flags.
type seasonEvents []string

func (e *seasonEvents) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*e = list
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("a season theme is a list of events or an object of 0/1 values: %w", err)
	}
	var on []string
	for name, v := range obj {
		switch x := v.(type) {
		case bool:
			if x {
				on = append(on, name)
			}
		case float64:
			if x != 0 {
				on = append(on, name)
			}
		default:
			return fmt.Errorf("event %q: want 0, 1, false or true", name)
		}
	}
	sort.Strings(on)
	*e = on
	return nil
}

// seasonTheme is what makes a season different: a name, and the community
// events that implement it.
type seasonTheme struct {
	name   string
	events []string
}

// originalThemes: seasons 14 to 29 each introduced a theme (seasons 1 to 13
// had none). The event flags are d3hack's season mapping, taken from real season
// configs; the names are the game's.
var originalThemes = map[uint32]seasonTheme{
	14: {"Season of Greed", []string{"DoubleGoblins"}},
	15: {"Boon of the Horadrim", []string{"DoubleBountyBags"}},
	16: {"Season of Grandeur", []string{"RoyalGrandeur"}},
	17: {"Season of Nightmares", []string{"LegacyOfNightmares"}},
	18: {"Season of the Triune", []string{"TriunesWill"}},
	19: {"Eternal Conflict", []string{"Pandemonium"}},
	20: {"Forbidden Archives", []string{"KanaiPowers"}},
	21: {"Trials of the Tempests", []string{"TrialsOfTempests"}},
	22: {"Shades of the Nephalem", []string{"ShadowClones", "FourthKanaisCubeSlot"}},
	23: {"Disciples of Sanctuary", nil}, // followers: built into the game, no event flag
	24: {"Ethereal Memory", []string{"EtherealItems"}},
	25: {"Lords of Hell", []string{"SoulShards"}},
	26: {"Echoing Nightmare", []string{"SwarmRifts"}},
	27: {"Light's Calling", []string{"SanctifiedItems"}},
	28: {"Rites of Sanctuary", []string{"DarkAlchemy"}},
	29: {"Visions of Enmity", []string{"NestingPortals"}},
}

// recycledThemes: from season 30 on the game repeats six earlier themes in a
// fixed order. Only seasons that have actually been announced are listed;
// add the next one here when it is.
var recycledThemes = map[uint32]uint32{
	30: 25, 31: 20, 32: 24, 33: 22, 34: 27, 35: 19,
	36: 25, 37: 20, 38: 24, 39: 22,
}

// themeOf returns the theme of a season (ok is false for seasons without one).
func themeOf(season uint32) (seasonTheme, bool) {
	if orig, recycled := recycledThemes[season]; recycled {
		season = orig
	}
	t, ok := originalThemes[season]
	return t, ok
}

func gmtDate(t time.Time) string {
	return t.UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT"
}

// seasonList returns the seasons that take part in the rotation, in numeric
// order: every season with a built-in theme, plus every number added in
// "season_themes", within the optional First/Last bounds.
func seasonList(r seasonRotation, custom map[string]seasonEvents) []uint32 {
	set := map[uint32]bool{}
	for n := range originalThemes {
		set[n] = true
	}
	for n := range recycledThemes {
		set[n] = true
	}
	for key := range custom {
		if n, err := strconv.ParseUint(key, 10, 32); err == nil && n > 0 {
			set[uint32(n)] = true
		}
	}
	var list []uint32
	for n := range set {
		if (r.First == 0 || n >= r.First) && (r.Last == 0 || n <= r.Last) {
			list = append(list, n)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
	return list
}

// rotatedSeason returns the season running at now, taken from list, and its
// window (the current calendar month). The list is used in order and wraps
// around to its first entry after the last.
func rotatedSeason(list []uint32, r seasonRotation, now time.Time) (season uint32, start, end time.Time) {
	now = now.UTC()
	anchor, err := time.Parse("2006-01", r.Anchor)
	if err != nil {
		if anchor, err = time.Parse("2006-01-02", r.Anchor); err != nil {
			anchor = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		}
	}
	if r.IntervalSeconds > 0 {
		step := time.Duration(r.IntervalSeconds) * time.Second
		n := int64(0)
		if now.After(anchor) {
			n = int64(now.Sub(anchor) / step)
		}
		start = anchor.Add(time.Duration(n) * step)
		return list[n%int64(len(list))], start, start.Add(step)
	}
	start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end = start.AddDate(0, 1, 0)
	months := (now.Year()-anchor.Year())*12 + int(now.Month()) - int(anchor.Month())
	if months < 0 {
		months = 0
	}
	return list[months%len(list)], start, end
}

// effectiveConfig applies the season rotation, if enabled, to a copy of c.
func effectiveConfig(c pubConfig, now time.Time) pubConfig {
	if !c.SeasonRotation.Enabled {
		return c
	}
	list := seasonList(c.SeasonRotation, c.SeasonThemes)
	if len(list) == 0 {
		return c
	}
	season, start, _ := rotatedSeason(list, c.SeasonRotation, now)
	// Only the start moves. The end stays at season_end (far in the future by
	// default), so a season never ends under a connected player: the season
	// changes when the game next connects.
	c.Season, c.SeasonStart = season, gmtDate(start)
	if c.SeasonRotation.ThemeEvents {
		events := make(map[string]bool, len(c.Events)+2)
		for k, v := range c.Events {
			events[k] = v
		}
		themeEvents := []string(nil)
		if t, ok := themeOf(season); ok {
			themeEvents = t.events
		}
		if custom, ok := c.SeasonThemes[strconv.Itoa(int(season))]; ok {
			themeEvents = []string(custom)
		}
		for _, name := range themeEvents {
			events[name] = true
		}
		c.Events = events
	}
	return c
}

// rotatedPubfile generates Seasons.txt and Config.txt on the fly while rotation
// is on. Other names, or rotation off, fall through to the files on disk.
func rotatedPubfile(name string, now time.Time) ([]byte, string, bool) {
	name = strings.ToLower(name)
	var build func(pubConfig) string
	switch name {
	case "seasons.txt", "seasons_config.txt":
		build = buildSeasons
	case "config.txt":
		build = buildConfig
	default:
		return nil, "", false
	}
	cfg, err := loadPubConfig(pubConfigPath)
	if err != nil || !cfg.SeasonRotation.Enabled {
		return nil, "", false
	}
	eff := effectiveConfig(cfg, now)
	logRotation(name, eff)
	return []byte(build(eff)), "generated (season rotation)", true
}

// logRotation records what a rotated file was served with.
func logRotation(name string, c pubConfig) {
	theme := "no theme"
	if t, ok := themeOf(c.Season); ok {
		theme = t.name
	}
	var on []string
	for _, e := range knownEvents {
		if c.Events[e] {
			on = append(on, e)
		}
	}
	if strings.Contains(name, "season") {
		log.Printf("[D3 Season] %s -> season %d (%s), start %s, end %s", name, c.Season, theme, c.SeasonStart, c.SeasonEnd)
		return
	}
	log.Printf("[D3 Season] %s -> season %d, %d events on: %s", name, c.Season, len(on), strings.Join(on, " "))
}
