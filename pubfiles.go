package main

// "Publisher" files that Diablo III fetches when its online session starts:
// the active season, community events and the item blacklist.
//
// These are three TEXT files the game receives as a string, via
// bdStorage.getPublisherFile (10/21) on the lobby (services.go). The formats
// come from the consumer itself (d3hack, season_events.hpp, which synthesizes
// them client-side to play offline):
//
//	seasons_config.txt    [Season N] + Start/End window
//	config.txt            Key "value" lines, including all the CommunityBuff*
//	blacklist_config.txt   [GBID] / [SNO] sections
//
// pubfiles.json drives the content. The files are written at startup and by
// "server.exe pubfiles"; the lobby re-reads them on every request.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pubConfig drives the served content. One file to edit to change the season
// or turn an event on.
type pubConfig struct {
	// Active season. d3hack ran on 37: we keep the same default so behavior
	// matches what has already been tested.
	Season uint32 `json:"season"`

	// Season window. The game requires "???, DD MMM YYYY hh:mm:ss GMT" with a
	// TWO-digit day — a one-digit day breaks the parsing.
	SeasonStart string `json:"season_start"`
	SeasonEnd   string `json:"season_end"`

	// Community buff window (independent of the season).
	BuffStart string `json:"buff_start"`
	BuffEnd   string `json:"buff_end"`

	// Community events. Each entry becomes CommunityBuff<Name> "1"/"0" in
	// config.txt.
	Events map[string]bool `json:"events"`

	// Find multipliers. The game reads them as floats.
	LegendaryFind string `json:"legendary_find"`
	GoldFind      string `json:"gold_find"`
	XP            string `json:"xp"`

	// Other config.txt settings that aren't events.
	HeroPublishFrequencyMinutes string `json:"hero_publish_frequency_minutes"`
	CrossPlatformSaveMigration  bool   `json:"cross_platform_save_migration"`
	SeasonalGlobalLeaderboards  bool   `json:"seasonal_global_leaderboards"`
	Diablo4Advertisement        bool   `json:"diablo4_advertisement"`
	UpdateVersion               string `json:"update_version"`
}

// knownEvents is the complete list of buffs the game recognizes, in the order
// d3hack emits them. A key missing from Events is emitted as "0" rather than
// omitted: the game reads the value, and a missing field is not the same thing
// as a field set to false.
var knownEvents = []string{
	"DoubleGoblins",
	"DoubleBountyBags",
	"RoyalGrandeur",
	"LegacyOfNightmares",
	"TriunesWill",
	"Pandemonium",
	"KanaiPowers",
	"TrialsOfTempests",
	"SeasonOnly",
	"ShadowClones",
	"FourthKanaisCubeSlot",
	"EtherealItems",
	"SoulShards",
	"SwarmRifts",
	"SanctifiedItems",
	"DarkAlchemy",
	"ParagonCap",
	"NestingPortals",
	"EasterEggWorld",
	"DoubleRiftKeystones",
	"DoubleBloodShards",
}

func defaultPubConfig() pubConfig {
	return pubConfig{
		Season:      37,
		SeasonStart: "Sat, 09 Feb 2025 00:00:00 GMT",
		SeasonEnd:   "Tue, 09 Feb 2036 01:00:00 GMT",
		BuffStart:   "Sat, 16 Sep 2023 00:00:00 GMT",
		BuffEnd:     "Wed, 01 Dec 2027 01:00:00 GMT",
		// Nothing on by default: a server should look like production until
		// we decide otherwise.
		Events:                      map[string]bool{},
		LegendaryFind:               "1.0",
		GoldFind:                    "1.0",
		XP:                          "1.0",
		HeroPublishFrequencyMinutes: "30",
		CrossPlatformSaveMigration:  true,
		SeasonalGlobalLeaderboards:  true,
		Diablo4Advertisement:        false,
		UpdateVersion:               "1",
	}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// buildSeasons returns seasons_config.txt. The two comment lines are those of
// the original file: they document the format constraint.
func buildSeasons(c pubConfig) string {
	var b strings.Builder
	b.WriteString("# Format for dates MUST be: \" ? ? ? , DD MMM YYYY hh : mm:ss UTC\"\n")
	b.WriteString("# The Day of Month(DD) MUST be 2 - digit; use either preceding zero or trailing space\n")
	fmt.Fprintf(&b, "[Season %d]\n", c.Season)
	fmt.Fprintf(&b, "Start \"%s\"\n", c.SeasonStart)
	fmt.Fprintf(&b, "End \"%s\"\n\n", c.SeasonEnd)
	return b.String()
}

// buildConfig returns config.txt: one Key "value" line per setting.
func buildConfig(c pubConfig) string {
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%s \"%s\"\n", k, v) }

	line("HeroPublishFrequencyMinutes", c.HeroPublishFrequencyMinutes)
	line("EnableCrossPlatformSaveMigration", boolStr(c.CrossPlatformSaveMigration))
	line("SeasonalGlobalLeaderboardsEnabled", boolStr(c.SeasonalGlobalLeaderboards))
	line("EnableDiablo4Advertisement", boolStr(c.Diablo4Advertisement))
	line("CommunityBuffStart", c.BuffStart)
	line("CommunityBuffEnd", c.BuffEnd)

	for _, name := range knownEvents {
		line("CommunityBuff"+name, boolStr(c.Events[name]))
	}

	line("CommunityBuffLegendaryFind", c.LegendaryFind)
	line("CommunityBuffGoldFind", c.GoldFind)
	line("CommunityBuffXP", c.XP)
	line("UpdateVersion", c.UpdateVersion)
	return b.String()
}

// buildBlacklist returns blacklist_config.txt. Both sections must exist even
// when empty, otherwise the game has nothing to parse.
func buildBlacklist(_ pubConfig) string {
	return "# Item blacklist served by diablo-3\n[GBID]\n\n[SNO]\n"
}

func loadPubConfig(path string) (pubConfig, error) {
	c := defaultPubConfig()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// First run: write the default file so there is something to edit,
		// rather than failing.
		out, _ := json.MarshalIndent(c, "", "  ")
		if werr := os.WriteFile(path, out, 0o644); werr == nil {
			log.Printf("[D3 Pubfiles] %s created with defaults", path)
		}
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// writePubfiles generates the three files from cfgPath into outDir.
func writePubfiles(cfgPath, outDir string) error {
	cfg, err := loadPubConfig(cfgPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	files := map[string]string{
		"seasons_config.txt":   buildSeasons(cfg),
		"config.txt":           buildConfig(cfg),
		"blacklist_config.txt": buildBlacklist(cfg),
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := filepath.Join(outDir, name)
		if err := os.WriteFile(p, []byte(files[name]), 0o644); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	on := make([]string, 0, len(knownEvents))
	for _, name := range knownEvents {
		if cfg.Events[name] {
			on = append(on, name)
		}
	}
	if len(on) == 0 {
		log.Printf("[D3 Pubfiles] %s: season %d, no community event", outDir, cfg.Season)
	} else {
		log.Printf("[D3 Pubfiles] %s: season %d, events: %s", outDir, cfg.Season, strings.Join(on, ", "))
	}
	return nil
}

// pubfilesHandler serves the generated files read-only (dashboard port):
// /pubfiles/ lists them, /pubfiles/<name> returns one.
func pubfilesHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/pubfiles/")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if name == "" {
		entries, _ := os.ReadDir(pubDir)
		for _, e := range entries {
			fmt.Fprintf(w, "/pubfiles/%s\n", e.Name())
		}
		return
	}
	if name != filepath.Base(name) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(filepath.Join(pubDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(b)
}
