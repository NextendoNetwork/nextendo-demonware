package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSeasonList(t *testing.T) {
	themes := defaultPubConfig().SeasonThemes
	list := seasonList(seasonRotation{}, themes)
	if len(list) != 26 || list[0] != 14 || list[len(list)-1] != 39 {
		t.Fatalf("default list: %v", list)
	}
	list = seasonList(seasonRotation{}, map[string]seasonEvents{"420": {"Pandemonium"}, "40": nil, "x": nil, "0": nil, "template": nil, "14": nil})
	if len(list) != 3 || list[0] != 14 || list[1] != 40 || list[2] != 420 {
		t.Fatalf("with additions: %v", list)
	}
	if got := seasonList(seasonRotation{First: 30, Last: 32}, themes); len(got) != 3 || got[0] != 30 || got[2] != 32 {
		t.Fatalf("bounded: %v", got)
	}
	if got := seasonList(seasonRotation{}, map[string]seasonEvents{"1": nil, "14": nil}); got[0] != 1 {
		t.Fatalf("season 1 not listed: %v", got)
	}
}

func TestRotatedSeason(t *testing.T) {
	list := []uint32{14, 15, 39, 420}
	r := seasonRotation{Enabled: true, Anchor: "2026-09"}
	at := func(y int, m time.Month) time.Time { return time.Date(y, m, 15, 12, 0, 0, 0, time.UTC) }

	for _, c := range []struct {
		when time.Time
		want uint32
	}{
		{at(2026, time.September), 14},
		{at(2026, time.October), 15},
		{at(2026, time.November), 39},
		{at(2026, time.December), 420}, // jumps over the gap
		{at(2027, time.January), 14},   // wraps to the beginning
		{at(2026, time.August), 14},    // before the anchor
	} {
		got, start, end := rotatedSeason(list, r, c.when)
		if got != c.want {
			t.Errorf("%v: season %d, want %d", c.when.Format("2006-01"), got, c.want)
		}
		if start.Day() != 1 || !end.After(c.when) || end.Sub(start) < 28*24*time.Hour {
			t.Errorf("%v: window %v..%v", c.when.Format("2006-01"), start, end)
		}
	}
}

func TestRotatedSeasonInterval(t *testing.T) {
	list := []uint32{14, 15, 420}
	r := seasonRotation{Enabled: true, Anchor: "2026-09-01", IntervalSeconds: 2}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		offset time.Duration
		want   uint32
	}{
		{-time.Hour, 14}, {0, 14}, {1900 * time.Millisecond, 14},
		{2 * time.Second, 15}, {4 * time.Second, 420}, {6 * time.Second, 14}, {8 * time.Second, 15},
	} {
		got, start, end := rotatedSeason(list, r, base.Add(c.offset))
		if got != c.want {
			t.Errorf("+%v: season %d, want %d", c.offset, got, c.want)
		}
		if c.offset >= 0 && (base.Add(c.offset).Before(start) || !base.Add(c.offset).Before(end) || end.Sub(start) != 2*time.Second) {
			t.Errorf("+%v: window %v..%v", c.offset, start, end)
		}
	}
}

func eventsOn(c pubConfig) []string {
	var on []string
	for _, e := range knownEvents {
		if c.Events[e] {
			on = append(on, e)
		}
	}
	return on
}

func TestThemeAppliesToTheServedSeason(t *testing.T) {
	c := defaultPubConfig()
	// a fixed season, no rotation: its theme is switched on automatically
	c.Season = 22
	got := effectiveConfig(c, time.Now())
	if on := strings.Join(eventsOn(got), ","); on != "ShadowClones,FourthKanaisCubeSlot" {
		t.Errorf("season 22 events: %s", on)
	}
	// the default season (39) repeats that theme
	if on := strings.Join(eventsOn(effectiveConfig(defaultPubConfig(), time.Now())), ","); on != "ShadowClones,FourthKanaisCubeSlot" {
		t.Errorf("default season events: %s", on)
	}
	// season_theme off: nothing is added, extras still apply
	c.SeasonTheme = false
	c.Events = map[string]bool{"SoulShards": true}
	if on := strings.Join(eventsOn(effectiveConfig(c, time.Now())), ","); on != "SoulShards" {
		t.Errorf("season_theme off: %s", on)
	}
	// extras stay on top of the theme, and the caller's map is untouched
	c.SeasonTheme = true
	c.Events = map[string]bool{"SoulShards": true}
	got = effectiveConfig(c, time.Now())
	if on := strings.Join(eventsOn(got), ","); on != "ShadowClones,FourthKanaisCubeSlot,SoulShards" {
		t.Errorf("theme plus extras: %s", on)
	}
	if c.Events["ShadowClones"] {
		t.Error("effectiveConfig modified the caller's events")
	}
	// seasons without a theme, and 23 with an empty one, add nothing
	for _, season := range []uint32{5, 23} {
		c.Season, c.Events = season, map[string]bool{}
		if on := eventsOn(effectiveConfig(c, time.Now())); len(on) != 0 {
			t.Errorf("season %d: %v", season, on)
		}
	}
}

func TestRotationAppliesSeasonAndTheme(t *testing.T) {
	c := defaultPubConfig()
	c.SeasonRotation = seasonRotation{Enabled: true, First: 14, Last: 15, Anchor: "2026-09"}
	got := effectiveConfig(c, time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC))
	if got.Season != 15 || !got.Events["DoubleBountyBags"] || got.Events["DoubleGoblins"] {
		t.Fatalf("season %d events %v", got.Season, eventsOn(got))
	}
	if got.SeasonEnd != c.SeasonEnd {
		t.Fatalf("season end moved: %q, want %q", got.SeasonEnd, c.SeasonEnd)
	}
	if !strings.Contains(buildSeasons(got), "[Season 15]") || !strings.Contains(buildSeasons(got), `Start "Thu, 01 Oct 2026 00:00:00 GMT"`) {
		t.Fatalf("seasons file:\n%s", buildSeasons(got))
	}
	// rotation off leaves the configured season
	c.SeasonRotation.Enabled = false
	if got := effectiveConfig(c, time.Now()); got.Season != c.Season {
		t.Fatalf("rotation off changed the season to %d", got.Season)
	}
}

func TestAddedSeasons(t *testing.T) {
	c := defaultPubConfig()
	c.SeasonThemes["40"] = seasonEvents{"SanctifiedItems"}
	c.SeasonThemes["41"] = seasonEvents{"Pandemonium", "SoulShards"}
	c.SeasonThemes["420"] = seasonEvents{}
	c.SeasonRotation = seasonRotation{Enabled: true, First: 40, Last: 420, Anchor: "2026-09"}
	for _, tc := range []struct {
		month time.Month
		want  uint32
		on    string
	}{
		{time.September, 40, "SanctifiedItems"},
		{time.October, 41, "Pandemonium,SoulShards"},
		{time.November, 420, ""},
		{time.December, 40, "SanctifiedItems"}, // loops back
	} {
		got := effectiveConfig(c, time.Date(2026, tc.month, 10, 0, 0, 0, 0, time.UTC))
		if got.Season != tc.want || strings.Join(eventsOn(got), ",") != tc.on {
			t.Errorf("%v: season %d events %q, want %d %q", tc.month, got.Season, strings.Join(eventsOn(got), ","), tc.want, tc.on)
		}
	}
}

func TestDefaultThemeTable(t *testing.T) {
	c := defaultPubConfig()
	for season, want := range map[string]string{
		"14": "DoubleGoblins", "20": "KanaiPowers", "22": "ShadowClones,FourthKanaisCubeSlot",
		"23": "", "31": "KanaiPowers", "33": "ShadowClones,FourthKanaisCubeSlot", "37": "KanaiPowers",
		"38": "EtherealItems", "39": "ShadowClones,FourthKanaisCubeSlot",
	} {
		got, ok := c.SeasonThemes[season]
		if !ok || strings.Join(got, ",") != want {
			t.Errorf("season %s: %v (listed=%v), want %q", season, got, ok, want)
		}
	}
	// every theme uses only events the game knows
	known := map[string]bool{}
	for _, e := range knownEvents {
		known[e] = true
	}
	for season, events := range c.SeasonThemes {
		for _, e := range events {
			if !known[e] {
				t.Errorf("season %s uses unknown event %q", season, e)
			}
		}
	}
}

func TestSeasonEventsForms(t *testing.T) {
	var m map[string]seasonEvents
	err := json.Unmarshal([]byte(`{"a": ["SoulShards","Pandemonium"], "b": {"SoulShards": 1, "Pandemonium": 0, "SwarmRifts": true, "DarkAlchemy": false}, "c": [], "d": {}}`), &m)
	if err != nil {
		t.Fatal(err)
	}
	if len(m["a"]) != 2 {
		t.Errorf("list form: %v", m["a"])
	}
	if got := m["b"]; len(got) != 2 || got[0] != "SoulShards" || got[1] != "SwarmRifts" {
		t.Errorf("object form: %v", got)
	}
	if len(m["c"]) != 0 || len(m["d"]) != 0 {
		t.Errorf("empty forms: %v %v", m["c"], m["d"])
	}
	if err := json.Unmarshal([]byte(`{"a": {"SoulShards": "yes"}}`), &m); err == nil {
		t.Error("a string value was accepted")
	}
}
