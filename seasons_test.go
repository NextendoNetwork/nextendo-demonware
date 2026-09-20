package main

import (
	"strings"
	"testing"
	"time"
)

func TestSeasonList(t *testing.T) {
	list := seasonList(seasonRotation{}, nil)
	if list[0] != 14 || list[len(list)-1] != 39 || len(list) != 26 {
		t.Fatalf("built-in list: %v", list)
	}
	list = seasonList(seasonRotation{}, map[string]seasonEvents{"420": {"Pandemonium"}, "40": nil, "x": nil, "0": nil})
	if len(list) != 28 || list[26] != 40 || list[27] != 420 {
		t.Fatalf("with additions: %v", list)
	}
	if got := seasonList(seasonRotation{First: 30, Last: 32}, nil); len(got) != 3 || got[0] != 30 {
		t.Fatalf("bounded: %v", got)
	}
	// seasons 1-13 only take part when added explicitly
	if got := seasonList(seasonRotation{}, map[string]seasonEvents{"1": nil}); got[0] != 1 {
		t.Fatalf("season 1 not added: %v", got)
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

func TestEffectiveConfigThemes(t *testing.T) {
	c := defaultPubConfig()
	c.SeasonRotation = seasonRotation{Enabled: true, First: 14, Last: 15, Anchor: "2026-09", ThemeEvents: true}
	c.Events = map[string]bool{"DoubleGoblins": false, "SwarmRifts": true}
	got := effectiveConfig(c, time.Date(2026, time.October, 3, 0, 0, 0, 0, time.UTC))
	if got.Season != 15 || !got.Events["DoubleBountyBags"] || !got.Events["SwarmRifts"] {
		t.Fatalf("season %d events %v", got.Season, got.Events)
	}
	if got.SeasonEnd != c.SeasonEnd {
		t.Fatalf("season end moved: %q, want %q", got.SeasonEnd, c.SeasonEnd)
	}
	if c.Events["DoubleBountyBags"] {
		t.Fatal("effectiveConfig modified the caller's events")
	}
	if !strings.Contains(buildSeasons(got), "[Season 15]") || !strings.Contains(buildSeasons(got), `Start "Thu, 01 Oct 2026 00:00:00 GMT"`) {
		t.Fatalf("seasons file:\n%s", buildSeasons(got))
	}
	off := defaultPubConfig()
	if effectiveConfig(off, time.Now()).Season != off.Season {
		t.Fatal("rotation applied while disabled")
	}
}

func TestThemeOf(t *testing.T) {
	for season, want := range map[uint32]string{
		14: "DoubleGoblins", 22: "ShadowClones", 33: "ShadowClones", 37: "KanaiPowers",
		38: "EtherealItems", 39: "FourthKanaisCubeSlot",
	} {
		th, ok := themeOf(season)
		found := false
		for _, e := range th.events {
			found = found || e == want
		}
		if !ok || !found {
			t.Errorf("season %d: %+v ok=%v, want event %s", season, th, ok, want)
		}
	}
	if th, ok := themeOf(23); !ok || len(th.events) != 0 || th.name == "" {
		t.Errorf("season 23 should be a named theme without events: %+v", th)
	}
	for _, season := range []uint32{1, 13, 40} {
		if _, ok := themeOf(season); ok {
			t.Errorf("season %d should have no theme", season)
		}
	}
	for _, th := range originalThemes {
		for _, e := range th.events {
			known := false
			for _, k := range knownEvents {
				known = known || k == e
			}
			if !known {
				t.Errorf("theme %q uses unknown event %q", th.name, e)
			}
		}
	}
}

func TestConfigThemes(t *testing.T) {
	c := defaultPubConfig()
	c.SeasonRotation = seasonRotation{Enabled: true, First: 40, Last: 41, Anchor: "2026-09", ThemeEvents: true}
	c.SeasonThemes = map[string]seasonEvents{"40": {"SanctifiedItems"}, "41": {"Pandemonium", "SoulShards"}}
	got := effectiveConfig(c, time.Date(2026, time.September, 10, 0, 0, 0, 0, time.UTC))
	if got.Season != 40 || !got.Events["SanctifiedItems"] || got.Events["Pandemonium"] {
		t.Fatalf("season %d events %v", got.Season, got.Events)
	}
	got = effectiveConfig(c, time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC))
	if got.Season != 41 || !got.Events["Pandemonium"] || !got.Events["SoulShards"] {
		t.Fatalf("season %d events %v", got.Season, got.Events)
	}
	// a custom entry replaces the built-in one
	c.SeasonRotation.First, c.SeasonRotation.Last = 37, 37
	c.SeasonThemes = map[string]seasonEvents{"37": {"DarkAlchemy"}}
	got = effectiveConfig(c, time.Date(2026, time.September, 10, 0, 0, 0, 0, time.UTC))
	if !got.Events["DarkAlchemy"] || got.Events["KanaiPowers"] {
		t.Fatalf("override not applied: %v", got.Events)
	}
}

func TestSeasonWithEmptyTheme(t *testing.T) {
	c := defaultPubConfig()
	c.SeasonRotation = seasonRotation{Enabled: true, First: 420, Anchor: "2026-09", ThemeEvents: true}
	c.SeasonThemes = map[string]seasonEvents{"420": {}}
	c.Events = map[string]bool{}
	got := effectiveConfig(c, time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC))
	if got.Season != 420 {
		t.Fatalf("season %d, want 420", got.Season)
	}
	for name, on := range got.Events {
		if on {
			t.Errorf("event %s is on in a themeless season", name)
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
