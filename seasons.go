package main

// Seasons and their themes.
//
// A season is two things to the game: the season number and window in
// Seasons.txt, and its theme, which the game receives as community-event flags
// in Config.txt (d3hack's "season theme mapping" does exactly this offline).
//
// Which events make up each season's theme is listed once, in "season_themes" in
// pubfiles.json. With "season_theme" on (the default), the served season's theme
// events are switched on automatically, whether the season is fixed or rotating;
// "events" then only holds extras you force on top.
//
// With rotation on, the season advances every month through the seasons listed in
// "season_themes" (sorted by number, wrapping to the first after the last). The
// files are generated per request, so a running server rolls over at the month
// boundary, and a config edit takes effect, without a restart.

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
	// First and Last optionally bound which of the listed seasons take part
	// (inclusive); 0 means no bound.
	First uint32 `json:"first"`
	Last  uint32 `json:"last"`
	// Anchor is the month ("YYYY-MM") in which the first season of the list runs.
	Anchor string `json:"anchor"`
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

func gmtDate(t time.Time) string {
	return t.UTC().Format("Mon, 02 Jan 2006 15:04:05") + " GMT"
}

// seasonList returns the seasons that take part in the rotation, in numeric
// order: every season number listed in "season_themes" (entries that are not a
// number, like the "template", are skipped), within the optional First/Last
// bounds.
func seasonList(r seasonRotation, themes map[string]seasonEvents) []uint32 {
	var list []uint32
	for key := range themes {
		n, err := strconv.ParseUint(key, 10, 32)
		if err != nil || n == 0 {
			continue
		}
		if (r.First == 0 || uint32(n) >= r.First) && (r.Last == 0 || uint32(n) <= r.Last) {
			list = append(list, uint32(n))
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

// effectiveConfig returns c as it applies at now: the rotating season, if
// rotation is on, and the served season's theme events, if season_theme is on.
func effectiveConfig(c pubConfig, now time.Time) pubConfig {
	if c.SeasonRotation.Enabled {
		if list := seasonList(c.SeasonRotation, c.SeasonThemes); len(list) > 0 {
			season, start, _ := rotatedSeason(list, c.SeasonRotation, now)
			// Only the start moves. The end stays at season_end (far in the
			// future by default), so a season never ends under a connected
			// player: the season changes when the game next connects.
			c.Season, c.SeasonStart = season, gmtDate(start)
		}
	}
	if c.SeasonTheme {
		events := make(map[string]bool, len(c.Events)+2)
		for k, v := range c.Events {
			events[k] = v
		}
		for _, name := range c.SeasonThemes[strconv.FormatUint(uint64(c.Season), 10)] {
			events[name] = true
		}
		c.Events = events
	}
	return c
}

// generatedPubfile builds Seasons.txt and Config.txt from pubfiles.json on every
// request, so a change (or a rotation) applies without a restart. Other names
// fall through to the files on disk.
func generatedPubfile(name string, now time.Time) ([]byte, string, bool) {
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
	if err != nil {
		log.Printf("[D3 Season] %s: %v", pubConfigPath, err)
		return nil, "", false
	}
	eff := effectiveConfig(cfg, now)
	logGenerated(name, eff)
	return []byte(build(eff)), "generated from pubfiles.json", true
}

// logGenerated records what a generated file was served with.
func logGenerated(name string, c pubConfig) {
	if strings.Contains(name, "season") {
		log.Printf("[D3 Season] %s -> season %d, start %s, end %s", name, c.Season, c.SeasonStart, c.SeasonEnd)
		return
	}
	var on []string
	for _, e := range knownEvents {
		if c.Events[e] {
			on = append(on, e)
		}
	}
	log.Printf("[D3 Season] %s -> season %d, %d events on: %s", name, c.Season, len(on), strings.Join(on, " "))
}
