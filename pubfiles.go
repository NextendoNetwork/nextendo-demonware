package main

// Fichiers « publisher » que Diablo III recupere au demarrage de sa session en
// ligne : saison active, evenements communautaires et liste noire d'objets.
//
// Ce sont trois fichiers TEXTE que le jeu recoit sous forme de chaine, par
// bdStorage.getPublisherFile (10/21) sur le lobby (services.go) :
//
//	seasons_config.txt    [Season N] + fenetre Start/End
//	config.txt            lignes Cle "valeur", dont tous les CommunityBuff*
//	blacklist_config.txt   sections [GBID] / [SNO]
//
// pubfiles.json pilote le contenu. Les fichiers sont ecrits au demarrage et par
// « server pubfiles » ; le lobby les relit a chaque requete.

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

// pubConfig pilote le contenu servi. Un seul fichier a editer pour changer la
// saison ou allumer un evenement.
type pubConfig struct {
	// Saison active (37 par defaut, la derniere testee).
	Season uint32 `json:"season"`

	// Fenetre de la saison. Le jeu exige « ???, DD MMM YYYY hh:mm:ss GMT » avec
	// un jour sur DEUX chiffres — un jour a un chiffre casse l'analyse.
	SeasonStart string `json:"season_start"`
	SeasonEnd   string `json:"season_end"`

	// Fenetre des buffs communautaires (independante de la saison).
	BuffStart string `json:"buff_start"`
	BuffEnd   string `json:"buff_end"`

	// Evenements communautaires. Chaque entree devient
	// CommunityBuff<Nom> "1"/"0" dans config.txt.
	Events map[string]bool `json:"events"`

	// Multiplicateurs de trouvaille. Le jeu les lit comme des reels.
	LegendaryFind string `json:"legendary_find"`
	GoldFind      string `json:"gold_find"`
	XP            string `json:"xp"`

	// Divers reglages de config.txt qui ne sont pas des evenements.
	HeroPublishFrequencyMinutes string `json:"hero_publish_frequency_minutes"`
	CrossPlatformSaveMigration  bool   `json:"cross_platform_save_migration"`
	SeasonalGlobalLeaderboards  bool   `json:"seasonal_global_leaderboards"`
	Diablo4Advertisement        bool   `json:"diablo4_advertisement"`
	UpdateVersion               string `json:"update_version"`
}

// knownEvents est la liste complete des buffs que le jeu reconnait, dans
// l'ordre du fichier d'origine. Une cle absente de Events est emise a "0" plutot
// qu'omise : le jeu lit la valeur, et un champ manquant n'est pas la meme chose
// qu'un champ a faux.
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
		// Rien d'allume par defaut : un serveur doit ressembler a la production
		// tant qu'on ne decide pas le contraire.
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

// buildSeasons rend seasons_config.txt. Les deux lignes de commentaire sont
// celles du fichier d'origine : elles documentent la contrainte de format.
func buildSeasons(c pubConfig) string {
	var b strings.Builder
	b.WriteString("# Format for dates MUST be: \" ? ? ? , DD MMM YYYY hh : mm:ss UTC\"\n")
	b.WriteString("# The Day of Month(DD) MUST be 2 - digit; use either preceding zero or trailing space\n")
	fmt.Fprintf(&b, "[Season %d]\n", c.Season)
	fmt.Fprintf(&b, "Start \"%s\"\n", c.SeasonStart)
	fmt.Fprintf(&b, "End \"%s\"\n\n", c.SeasonEnd)
	return b.String()
}

// buildConfig rend config.txt : une ligne Cle "valeur" par reglage.
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

// buildBlacklist rend blacklist_config.txt. Les deux sections doivent exister
// meme vides, sinon le jeu n'a rien a analyser.
func buildBlacklist(_ pubConfig) string {
	return "# Item blacklist served by diablo-3\n[GBID]\n\n[SNO]\n"
}

func loadPubConfig(path string) (pubConfig, error) {
	c := defaultPubConfig()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Premiere execution : on ecrit le fichier par defaut pour qu'il y ait
		// quelque chose a editer, plutot que d'echouer.
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

// writePubfiles genere les trois fichiers de cfgPath dans outDir.
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

// pubfilesHandler sert les fichiers generes en lecture seule (port du tableau
// de bord) : /pubfiles/ les liste, /pubfiles/<nom> en rend un.
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
