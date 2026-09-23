// diablo-3 — Demonware game server for Diablo III on Nintendo Switch
// (title 01001B300B9BE000, Demonware title "crimson"), for Nextendo Network.
//
// Diablo III does not use NEX: its online layer is Demonware. One process
// serves all of it, like every Nextendo game server:
//
//	auth        HTTPS :AUTH_PORT (8460), reached through sni-router   auth.go identity.go gates.go
//	lobby       TCP 3074 (+3075..3080), encrypted remote tasks        lobby.go handshake.go bdcrypto.go services.go
//	matchmaking public games, friend lookups, user data               matchmaking.go friends.go
//	NAT         UDP 3074: IP / NAT discovery, introductions           nat.go
//	pubfiles    season, community events, item blacklist              pubfiles.go pubfiles.json
//	presence    connected players -> nextendo-account                 presence.go
//	dashboard   /api/stats on DASH_PORT (8093) for nextendo-dashboard dashboard.go
//
//	server            run the server
//	server pubfiles   regenerate the publisher files from pubfiles.json and exit
//	                      (the lobby reads them on every request: no restart needed)
package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

var (
	// The server's IPv4 address as the players see it, announced in NAT
	// discovery replies.
	nextendoHost = envOr("NEXTENDO_HOST", "127.0.0.1")
	authPort     = envOrInt("AUTH_PORT", 8460)
	lobbyPorts   = envOr("LOBBY_PORTS", "3074,3075,3076,3077,3078,3079,3080")
	natPort      = envOrInt("NAT_PORT", 3074)
	certFile     = envOr("CERT_FILE", "cert.pem")
	keyFile      = envOr("KEY_FILE", "key.pem")

	// Tickets and player identities written by auth, read back by the lobby.
	sessDir = envOr("D3_SESSIONS", "sessions")
	// pubfiles.json drives the three files written to D3_PUBFILES.
	pubConfigPath = envOr("D3_PUBFILES_CONFIG", "pubfiles.json")
	pubDir        = envOr("D3_PUBFILES", "pubfiles")

	// Raw captures (auth bodies, every lobby frame) under <D3_DUMPS>/<start>/.
	// D3_DUMPS=off disables them.
	dumpRoot = envOr("D3_DUMPS", "off")
	// D3_VERBOSE=1 logs every decrypted lobby message as a hex dump.
	verbose = os.Getenv("D3_VERBOSE") == "1"

	runStamp = time.Now().Format("20060102-150405")
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envOrInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// runDumpDir returns this run's capture directory for one component, "" when
// captures are off.
func runDumpDir(component string) string {
	if dumpRoot == "off" || dumpRoot == "0" {
		return ""
	}
	dir := filepath.Join(dumpRoot, runStamp, component)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[D3] dumps %s: %v", dir, err)
		return ""
	}
	return dir
}

func main() {
	// Logs on stdout like the other game servers (the launchers redirect it to
	// nextendo/logs/diablo-3.log).
	log.SetOutput(os.Stdout)

	if len(os.Args) > 1 && os.Args[1] == "pubfiles" {
		if err := writePubfiles(pubConfigPath, pubDir); err != nil {
			log.Fatalf("[D3 Pubfiles] %v", err)
		}
		return
	}

	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		log.Fatalf("[D3] sessions dir: %v", err)
	}
	if err := writePubfiles(pubConfigPath, pubDir); err != nil {
		log.Printf("[D3 Pubfiles] %v", err)
	}
	loadGates()
	loadWallets()

	go startDashboard()
	startPresenceReporter()
	go serveNAT(natPort)
	startCTRRelay(envOrInt("CTR_RELAY_PORT_LOW", 0), envOrInt("CTR_RELAY_PORT_HIGH", 0), nextendoHost)
	go reapSessions()
	startLobby()

	if err := serveAuth(); err != nil {
		log.Fatalf("[D3 Auth] stopped: %v", err)
	}
}
