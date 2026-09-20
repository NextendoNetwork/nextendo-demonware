package main

import (
	"encoding/json"
	"os"
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
