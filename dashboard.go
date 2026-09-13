package main

// Monitoring API for nextendo-dashboard: GET /api/stats?key=<DASH_TOKEN>, with
// the same JSON shape as the NEX game servers (luigis-mansion-3 dashboard.go), so
// the aggregator shows Diablo III without game-specific code. Demonware has no
// RMC or gatherings; the mapping is:
//
//	players     lobby connections whose Nextendo player is known
//	gatherings  bdMatchMaking sessions (21/1); id = first 4 bytes of the session id
//	rmc         remote tasks, named "Service::task" (taskName)
//	sessions    successful auth logins
//
// Also /healthz, and /pubfiles/ (read-only view of the generated publisher files).

import (
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	dashStart = time.Now()

	loginsTotal    atomic.Int64 // successful auth logins
	tasksTotal     atomic.Int64 // remote tasks served
	gatheringsMade atomic.Int64 // bdMatchMaking sessions created

	statsMu       sync.Mutex
	connStats     = map[uint64]*connStat{} // lobby connection -> activity
	taskEvents    []taskEvent
	taskCount     = map[string]int64{}
	peakConnected int
)

type connStat struct {
	addr       string
	first      time.Time
	last       time.Time
	calls      int64
	lastAction string
}

type taskEvent struct {
	t      time.Time
	pid    uint64
	action string
}

func registerConn(l *lobbyConn) {
	now := time.Now()
	statsMu.Lock()
	connStats[l.n] = &connStat{addr: l.c.RemoteAddr().String(), first: now, last: now}
	statsMu.Unlock()
}

func unregisterConn(l *lobbyConn) {
	statsMu.Lock()
	delete(connStats, l.n)
	statsMu.Unlock()
}

// connPID returns the Nextendo PID of a lobby connection's player (0 if unknown).
func connPID(conn uint64) uint64 {
	onlineMu.Lock()
	defer onlineMu.Unlock()
	if p := online[conn]; p != nil {
		return p.PID
	}
	return 0
}

// noteTask records one remote task for the dashboard (services.go onTask).
func noteTask(l *lobbyConn, service, task byte) {
	name := taskName(service, task)
	tasksTotal.Add(1)
	if service == svcMatchMaking && task == mmCreateSession {
		gatheringsMade.Add(1)
	}
	pid := connPID(l.n)
	now := time.Now()
	statsMu.Lock()
	if cs := connStats[l.n]; cs != nil {
		cs.calls++
		cs.last = now
		cs.lastAction = name
	}
	taskEvents = append(taskEvents, taskEvent{t: now, pid: pid, action: name})
	if len(taskEvents) > 100 {
		taskEvents = taskEvents[len(taskEvents)-100:]
	}
	taskCount[name]++
	statsMu.Unlock()
}

// Demonware service and task names.
var serviceNames = map[byte]string{
	4: "bdStats", 6: "bdMessaging", 10: "bdStorage", 12: "bdTitleUtilities",
	21: "bdMatchMaking", 23: "bdCounter", 27: "bdDML", 29: "UserData",
	67: "bdEventLog", 68: "bdRichPresence",
}

var taskNames = map[[2]byte]string{
	{10, 10}: "uploadFile", {10, 21}: "getPublisherFile",
	{12, 6}: "getServerTime", {12, 9}: "getUserNames",
	{21, 1}: "createSession", {21, 2}: "updateSession", {21, 3}: "deleteSession",
	{21, 5}: "findSessions", {21, 12}: "updateSessionPlayers",
	{29, 1}: "set", {29, 4}: "get", {29, 11}: "query",
}

func taskName(service, task byte) string {
	sn := serviceNames[service]
	if sn == "" {
		sn = fmt.Sprintf("Service-%d", service)
	}
	tn := taskNames[[2]byte{service, task}]
	if tn == "" {
		tn = fmt.Sprintf("t%d", task)
	}
	return sn + "::" + tn
}

type apiPlayer struct {
	PID        uint64 `json:"pid"`
	Name       string `json:"name"`
	IP         string `json:"ip"`
	State      string `json:"state"`
	Gathering  uint32 `json:"gathering"`
	OnlineSecs int    `json:"onlineSeconds"`
	Calls      int64  `json:"calls"`
	LastAction string `json:"lastAction"`
	IdleSecs   int    `json:"idleSeconds"`
	VR         uint32 `json:"vr"`
	Mode       string `json:"mode"`
	IsHost     bool   `json:"isHost"`
}

type apiLobbyP struct {
	PID  uint64 `json:"pid"`
	Name string `json:"name"`
	VR   uint32 `json:"vr"`
	Host bool   `json:"host"`
}

type apiGathering struct {
	ID       uint32      `json:"id"`
	HostPID  uint64      `json:"hostPid"`
	HostName string      `json:"hostName"`
	Type     string      `json:"type"`
	Mode     uint32      `json:"mode"`
	VR       uint32      `json:"vr"`
	Players  []apiLobbyP `json:"players"`
	Count    int         `json:"count"`
	Max      uint16      `json:"max"`
	State    string      `json:"state"`
	Code     string      `json:"code,omitempty"`
}

type apiEvent struct {
	Ago    int    `json:"agoSeconds"`
	PID    uint64 `json:"pid"`
	Action string `json:"action"`
}

type apiMethod struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type apiServer struct {
	AccessKey  string `json:"accessKey"`
	NexVersion string `json:"nexVersion"`
	AuthPort   string `json:"authPort"`
	SecurePort int    `json:"securePort"`
	SNIHost    string `json:"sniHost"`
	SessionKey int    `json:"sessionKeyLen"`
	Stack      string `json:"stack"`
}

type apiStats struct {
	ServerTime     string         `json:"serverTime"`
	UptimeSeconds  int            `json:"uptimeSeconds"`
	Connected      int            `json:"connected"`
	InLobby        int            `json:"inLobby"`
	ActiveLobbies  int            `json:"activeLobbies"`
	TotalSessions  int64          `json:"totalSessions"`
	TotalRMC       int64          `json:"totalRmc"`
	GatheringsMade int64          `json:"gatheringsMade"`
	PeakConnected  int            `json:"peakConnected"`
	NATIntros      int64          `json:"natIntroductions"`
	Server         apiServer      `json:"server"`
	Players        []apiPlayer    `json:"players"`
	Gatherings     []apiGathering `json:"gatherings"`
	Events         []apiEvent     `json:"events"`
	Methods        []apiMethod    `json:"methods"`
}

func buildStats() apiStats {
	onlineMu.Lock()
	who := make(map[uint64]playerID, len(online))
	for n, p := range online {
		who[n] = *p
	}
	onlineMu.Unlock()

	type sessSnap struct {
		id                               [8]byte
		owner                            uint64
		gameType, maxPlayers, numPlayers uint32
	}
	sessionsMu.Lock()
	snaps := make([]sessSnap, 0, len(sessions))
	for _, s := range sessions {
		snaps = append(snaps, sessSnap{s.id, s.owner, s.gameType, s.maxPlayers, s.numPlayers})
	}
	sessionsMu.Unlock()

	hosting := map[uint64]apiGathering{} // host connection -> its public game
	gs := make([]apiGathering, 0, len(snaps))
	for _, s := range snaps {
		host := who[s.owner]
		state := "en recherche"
		if s.numPlayers >= 2 {
			state = "apparié"
		}
		g := apiGathering{
			ID: binary.LittleEndian.Uint32(s.id[:4]), HostPID: host.PID, HostName: host.Username,
			Type: fmt.Sprintf("Partie publique (type %d)", s.gameType), Mode: s.gameType,
			Players: []apiLobbyP{{PID: host.PID, Name: host.Username, Host: true}},
			Count:   int(s.numPlayers), Max: uint16(s.maxPlayers), State: state,
		}
		hosting[s.owner] = g
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].ID < gs[j].ID })

	statsMu.Lock()
	defer statsMu.Unlock()

	// A player who reconnected can hold two lobby connections: keep the most
	// recently active one.
	byPID := map[uint64]apiPlayer{}
	for n, cs := range connStats {
		p, ok := who[n]
		if !ok || p.PID == 0 {
			continue
		}
		pl := apiPlayer{
			PID: p.PID, Name: p.Username, IP: cs.addr, State: "en ligne",
			OnlineSecs: int(time.Since(cs.first).Seconds()), Calls: cs.calls,
			LastAction: cs.lastAction, IdleSecs: int(time.Since(cs.last).Seconds()),
		}
		if g, hosts := hosting[n]; hosts {
			pl.State, pl.Gathering, pl.Mode, pl.IsHost = "dans un lobby", g.ID, g.Type, true
		}
		if prev, dup := byPID[p.PID]; dup && prev.IdleSecs <= pl.IdleSecs {
			continue
		}
		byPID[p.PID] = pl
	}
	players := make([]apiPlayer, 0, len(byPID))
	inLobby := 0
	for _, pl := range byPID {
		if pl.Gathering != 0 {
			inLobby++
		}
		players = append(players, pl)
	}
	sort.Slice(players, func(i, j int) bool { return players[i].PID < players[j].PID })
	if len(players) > peakConnected {
		peakConnected = len(players)
	}

	ev := make([]apiEvent, 0, len(taskEvents))
	for i := len(taskEvents) - 1; i >= 0; i-- {
		e := taskEvents[i]
		ev = append(ev, apiEvent{Ago: int(time.Since(e.t).Seconds()), PID: e.pid, Action: e.action})
	}
	ms := make([]apiMethod, 0, len(taskCount))
	for name, c := range taskCount {
		ms = append(ms, apiMethod{Name: name, Count: c})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Count > ms[j].Count })

	return apiStats{
		ServerTime:     time.Now().Format("15:04:05"),
		UptimeSeconds:  int(time.Since(dashStart).Seconds()),
		Connected:      len(players),
		InLobby:        inLobby,
		ActiveLobbies:  len(gs),
		TotalSessions:  loginsTotal.Load(),
		TotalRMC:       tasksTotal.Load(),
		GatheringsMade: gatheringsMade.Load(),
		PeakConnected:  peakConnected,
		NATIntros:      natIntros.Load(),
		Server: apiServer{
			NexVersion: "Demonware lobby 220", AuthPort: fmt.Sprint(authPort), SecurePort: natPort,
			SNIHost: envOr("NEXTENDO_SNI_HOST", ""), SessionKey: 24, Stack: "demonware",
		},
		Players:    players,
		Gatherings: gs,
		Events:     ev,
		Methods:    ms,
	}
}

func startDashboard() {
	port := envOr("DASH_PORT", "8093")
	token := envOr("DASH_TOKEN", "")

	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if token != "" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), []byte(token)) == 1 {
			return true
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(buildStats())
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/pubfiles/", pubfilesHandler)

	log.Printf("[D3 Dashboard] stats API on :%s (token=%v)", port, token != "")
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Printf("[D3 Dashboard] HTTP error: %v", err)
	}
}
