package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripJSONComments(t *testing.T) {
	in := "// header\n{\n  \"a\": \"http://x // not a comment\", // trailing\n  /* block\n  spanning */ \"b\": 1,\n  \"c\": \"quote \\\" // still a string\"\n}\n"
	var v map[string]any
	if err := json.Unmarshal(stripJSONComments([]byte(in)), &v); err != nil {
		t.Fatalf("%v\n%s", err, stripJSONComments([]byte(in)))
	}
	if v["a"] != "http://x // not a comment" || v["b"].(float64) != 1 || v["c"] != "quote \" // still a string" {
		t.Errorf("parsed %v", v)
	}
	if got := strings.Count(string(stripJSONComments([]byte(in))), "\n"); got != strings.Count(in, "\n") {
		t.Errorf("line count changed: %d, want %d", got, strings.Count(in, "\n"))
	}
}

// The shipped pubfiles.json is the source of the defaults, and it is what a
// fresh install starts from: it must say what the docs say the defaults are.
func TestShippedDefaults(t *testing.T) {
	c := defaultPubConfig()
	if c.Season != 39 || c.SeasonStart != "Wed, 01 Jan 2020 00:00:00 GMT" || c.SeasonEnd != "Tue, 01 Jan 2036 00:00:00 GMT" {
		t.Errorf("season %d %q %q", c.Season, c.SeasonStart, c.SeasonEnd)
	}
	if !c.SeasonTheme {
		t.Error("season_theme is off by default")
	}
	if len(c.Events) != len(knownEvents) {
		t.Errorf("shipped events list %d of %d events", len(c.Events), len(knownEvents))
	}
	for _, e := range knownEvents {
		if on, listed := c.Events[e]; !listed || on {
			t.Errorf("event %s: listed=%v on=%v, want listed and off", e, listed, on)
		}
	}
	if c.SeasonRotation.Enabled || c.SeasonRotation.IntervalSeconds != 0 || c.SeasonRotation.First != 0 || c.SeasonRotation.Last != 0 {
		t.Errorf("rotation: %+v", c.SeasonRotation)
	}
	if c.ChallengeRifts.Mode != "weekly" {
		t.Errorf("rifts: %+v", c.ChallengeRifts)
	}
	if c.XP != "1.0" || c.GoldFind != "1.0" || c.LegendaryFind != "1.0" {
		t.Errorf("multipliers %q %q %q", c.XP, c.GoldFind, c.LegendaryFind)
	}
}

// The template lists every event, all off, and is not a season.
func TestTemplateEntry(t *testing.T) {
	var f struct {
		SeasonThemes struct {
			Template map[string]int `json:"template"`
		} `json:"season_themes"`
	}
	if err := json.Unmarshal(stripJSONComments(defaultPubfiles), &f); err != nil {
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
	if got := len(seasonList(seasonRotation{}, defaultPubConfig().SeasonThemes)); got != 26 {
		t.Fatalf("the template changed the season list: %d", got)
	}
}

// A config file on disk is read over the defaults: it may be short, and it may
// carry comments.
func TestLoadPubConfigOverDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pubfiles.json")
	if err := os.WriteFile(p, []byte("// mine\n{ \"season\": 37, /* extra */ \"events\": { \"SoulShards\": true } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadPubConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Season != 37 || !c.Events["SoulShards"] || len(c.SeasonThemes) != 27 || c.SeasonEnd != defaultPubConfig().SeasonEnd {
		t.Errorf("season %d, events %v, %d themes", c.Season, eventsOn(c), len(c.SeasonThemes))
	}
	// a missing file is created from the shipped one, comments and all
	missing := filepath.Join(dir, "new.json")
	if _, err := loadPubConfig(missing); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(missing)
	if err != nil || string(raw) != string(defaultPubfiles) {
		t.Errorf("created file differs from the shipped one (err=%v)", err)
	}
}

// A date past 19 Jan 2038 wraps in the game's 32-bit time and ends the season, so
// it is replaced when the config is read.
func TestDatesPastTheGameLimitAreClamped(t *testing.T) {
	for in, want := range map[string]string{
		"Sat, 01 Jan 2050 00:00:00 GMT": latestSafeDate,
		"Tue, 19 Jan 2038 03:14:08 GMT": latestSafeDate,
		"Tue, 19 Jan 2038 03:14:07 GMT": "Tue, 19 Jan 2038 03:14:07 GMT", // the last second that fits
		"Tue, 01 Jan 2036 00:00:00 GMT": "Tue, 01 Jan 2036 00:00:00 GMT",
		"not a date":                    "not a date",
	} {
		if got, _ := clampGameDate(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if _, changed := clampGameDate(latestSafeDate); changed {
		t.Error("the replacement date is itself out of range")
	}
	p := filepath.Join(t.TempDir(), "pubfiles.json")
	if err := os.WriteFile(p, []byte(`{"season_end": "Sat, 01 Jan 2050 00:00:00 GMT", "buff_end": "Sat, 01 Jan 2060 00:00:00 GMT"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadPubConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.SeasonEnd != latestSafeDate || c.BuffEnd != latestSafeDate {
		t.Errorf("loaded season_end %q buff_end %q", c.SeasonEnd, c.BuffEnd)
	}
}
