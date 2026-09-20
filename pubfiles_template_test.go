package main

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The template in the shipped pubfiles.json lists every event the server knows,
// all off, and must not count as a season.
func TestTemplateEntry(t *testing.T) {
	raw, err := os.ReadFile("pubfiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		SeasonThemes struct {
			Template map[string]int `json:"template"`
		} `json:"season_themes"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.SeasonThemes.Template) != len(knownEvents) {
		t.Fatalf("template has %d events, the server knows %d", len(f.SeasonThemes.Template), len(knownEvents))
	}
	for _, name := range knownEvents {
		if v, ok := f.SeasonThemes.Template[name]; !ok || v != 0 {
			t.Errorf("template event %s: %v (present=%v), want 0", name, v, ok)
		}
	}
	c, err := loadPubConfig("pubfiles.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(seasonList(seasonRotation{}, c.SeasonThemes)), len(seasonList(seasonRotation{}, nil)); got != want {
		t.Fatalf("the template changed the season list: %d, want %d", got, want)
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

// The shipped pubfiles.json must say what the docs say the defaults are, so a
// fresh install behaves like the documented one (no event on, rotation off).
func TestShippedPubfilesMatchDefaults(t *testing.T) {
	got, err := loadPubConfig("pubfiles.json")
	if err != nil {
		t.Fatal(err)
	}
	want := defaultPubConfig()
	for name, on := range got.Events {
		if on {
			t.Errorf("shipped pubfiles.json switches event %s on", name)
		}
	}
	if got.Season != want.Season || got.SeasonStart != want.SeasonStart || got.SeasonEnd != want.SeasonEnd ||
		got.BuffStart != want.BuffStart || got.BuffEnd != want.BuffEnd {
		t.Errorf("season or buff window differs from the defaults")
	}
	if got.XP != want.XP || got.GoldFind != want.GoldFind || got.LegendaryFind != want.LegendaryFind {
		t.Errorf("multipliers differ from the defaults")
	}
	if got.HeroPublishFrequencyMinutes != want.HeroPublishFrequencyMinutes || got.UpdateVersion != want.UpdateVersion ||
		got.CrossPlatformSaveMigration != want.CrossPlatformSaveMigration ||
		got.SeasonalGlobalLeaderboards != want.SeasonalGlobalLeaderboards || got.Diablo4Advertisement != want.Diablo4Advertisement {
		t.Errorf("flags differ from the defaults")
	}
	if got.SeasonRotation != want.SeasonRotation || got.ChallengeRifts != want.ChallengeRifts {
		t.Errorf("rotation or rift settings differ from the defaults: %+v %+v", got.SeasonRotation, got.ChallengeRifts)
	}
}

// Every built-in season is listed in the shipped pubfiles.json, exactly as the
// built-in table has it, so the file is an accurate menu.
func TestShippedSeasonsMatchBuiltIn(t *testing.T) {
	c, err := loadPubConfig("pubfiles.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range seasonList(seasonRotation{}, nil) {
		key := strconv.Itoa(int(n))
		listed, ok := c.SeasonThemes[key]
		if !ok {
			t.Errorf("season %s is not listed in the shipped pubfiles.json", key)
			continue
		}
		th, _ := themeOf(n)
		if strings.Join(listed, ",") != strings.Join(th.events, ",") {
			t.Errorf("season %s: shipped %v, built-in %v", key, listed, th.events)
		}
	}
}
