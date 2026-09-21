package main

// Presence reporter, same contract as the Nextendo game servers (splatoon-2,
// animal-crossing-new-horizons, luigis-mansion-3): every player connected to the
// D3 lobby is playing Diablo III online right now, so their Nextendo PIDs are
// posted to nextendo-account every 30 s. nextendo-account keeps them online via
// its TTL (90 s) and serves them to friend lists — "online / playing Diablo III".
//
// PIDs come from the login token (nnex claim) recorded at login (auth.go), i.e. the
// player's Nextendo account PID.
//
// Env:
//   NEXTENDO_ACCOUNT_URL   account server (default http://127.0.0.1:8080)
//   NEXTENDO_INTERNAL_KEY  sent as X-Internal-Key when set

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	presenceInterval = 30 * time.Second
	presenceStatus   = 2 // playing
	d3AppID          = "01001b300b9be000"
	ctrAppID         = "0100f9f00c696000"
)

// appIDForTitle maps the announced Demonware title to the Switch title id the
// account server shows on a player's card. Crash Team Racing runs on this same
// server, so reporting one fixed id told every CTR player's friends they were
// playing Diablo III.
func appIDForTitle(title uint32) string {
	if title == 5775 {
		return ctrAppID
	}
	return d3AppID
}

// onlinePIDs returns the Nextendo PIDs of players connected to the lobby.
// onlinePIDs returns every connected player's PID, regardless of game.
func onlinePIDs() []uint64 {
	out := []uint64{}
	for _, pids := range onlinePIDsByApp() {
		out = append(out, pids...)
	}
	return out
}

func onlinePIDsByApp() map[string][]uint64 {
	onlineMu.Lock()
	defer onlineMu.Unlock()
	seen := map[uint64]bool{}
	out := map[string][]uint64{}
	for _, p := range online {
		if p.PID != 0 && !seen[p.PID] {
			seen[p.PID] = true
			app := appIDForTitle(p.Title)
			out[app] = append(out[app], p.PID)
		}
	}
	return out
}

// startPresenceReporter posts the connected PIDs to nextendo-account on a loop.
func startPresenceReporter() {
	base := envOr("NEXTENDO_ACCOUNT_URL", "http://127.0.0.1:8080")
	key := os.Getenv("NEXTENDO_INTERNAL_KEY")
	client := &http.Client{Timeout: 5 * time.Second}
	log.Printf("[presence] reporting players to %s/internal/presence-batch every %s", base, presenceInterval)
	go func() {
		lastLogged := map[string]int{}
		for {
			time.Sleep(presenceInterval)
			// One batch PER GAME: this server hosts both Diablo III and Crash Team
			// Racing, and a single appId put every CTR player on a Diablo III card.
			for app, pids := range onlinePIDsByApp() {
				if len(pids) == 0 {
					continue
				}
				body, err := json.Marshal(map[string]any{"appId": app, "status": presenceStatus, "pids": pids})
				if err != nil {
					continue
				}
				req, err := http.NewRequest("POST", base+"/internal/presence-batch", bytes.NewReader(body))
				if err != nil {
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				if key != "" {
					req.Header.Set("X-Internal-Key", key)
				}
				resp, err := client.Do(req)
				if err != nil {
					log.Printf("[presence] %v", err)
					continue
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK || len(pids) != lastLogged[app] {
					log.Printf("[presence] app=%s %d player(s) %v -> HTTP %d", app, len(pids), pids, resp.StatusCode)
					lastLogged[app] = len(pids)
				}
			}
		}
	}()
}
