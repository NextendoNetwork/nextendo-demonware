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
)

// onlinePIDs returns the Nextendo PIDs of players connected to the lobby.
func onlinePIDs() []uint64 {
	onlineMu.Lock()
	defer onlineMu.Unlock()
	seen := map[uint64]bool{}
	out := []uint64{}
	for _, p := range online {
		if p.PID != 0 && !seen[p.PID] {
			seen[p.PID] = true
			out = append(out, p.PID)
		}
	}
	return out
}

// startPresenceReporter posts the connected PIDs to nextendo-account on a loop.
func startPresenceReporter() {
	base := envOr("NEXTENDO_ACCOUNT_URL", "http://127.0.0.1:8080")
	key := os.Getenv("NEXTENDO_INTERNAL_KEY")
	client := &http.Client{Timeout: 5 * time.Second}
	log.Printf("[presence] reporting D3 players to %s/internal/presence-batch every %s", base, presenceInterval)
	go func() {
		lastLogged := -1
		for {
			time.Sleep(presenceInterval)
			pids := onlinePIDs()
			if len(pids) == 0 {
				continue
			}
			body, err := json.Marshal(map[string]any{"appId": d3AppID, "status": presenceStatus, "pids": pids})
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
			if resp.StatusCode != http.StatusOK || len(pids) != lastLogged {
				log.Printf("[presence] %d player(s) %v -> HTTP %d", len(pids), pids, resp.StatusCode)
				lastLogged = len(pids)
			}
		}
	}()
}
