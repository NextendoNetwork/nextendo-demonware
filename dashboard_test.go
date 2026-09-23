package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDashboardCTRMembershipAndTitleSplit(t *testing.T) {
	oldOnline := online
	oldStats := connStats
	oldRooms := ctrMM.rooms
	oldSessions := sessions
	defer func() { online = oldOnline; connStats = oldStats; ctrMM.rooms = oldRooms; sessions = oldSessions }()
	online = map[uint64]*playerID{1: {PID: 101, Username: "host", Title: 5775}, 2: {PID: 102, Username: "joined", Title: 5775}, 3: {PID: 103, Username: "pending", Title: 5775}, 4: {PID: 104, Username: "diablo"}}
	connStats = map[uint64]*connStat{}
	for id := range online {
		connStats[id] = &connStat{first: time.Now(), last: time.Now()}
	}
	host := &ctrSearch{pid: 101, doc: ctrSearchDoc{Members: []uint64{101}}}
	guest := &ctrSearch{pid: 102, doc: ctrSearchDoc{Members: []uint64{102, 103}}}
	ctrMM.rooms = map[uint64]*ctrRoom{88: {id: 88, host: host, players: []*ctrSearch{host, guest}, capacity: 8, hostDoc: map[string]any{"player_state": map[string]any{"101": json.Number("2"), "102": json.Number("2"), "103": json.Number("1")}}}}
	sessions = map[[8]byte]*mmSession{{1}: {owner: 1, numPlayers: 2, maxPlayers: 8}, {2}: {owner: 2, numPlayers: 1, maxPlayers: 8}}
	s := buildGameStats("ctr")
	if s.Connected != 3 || s.ActiveLobbies != 1 || s.InLobby != 2 || s.Gatherings[0].Count != 2 {
		t.Fatalf("wrong counters: %+v", s)
	}
	for _, p := range s.Players {
		if p.PID == 102 && (p.IsHost || p.Gathering != 88) {
			t.Fatalf("guest misclassified %+v", p)
		}
		if p.PID == 103 && p.Gathering != 0 {
			t.Fatal("reservation counted as joined")
		}
	}
	d := buildGameStats("d3")
	if d.Connected != 1 || d.ActiveLobbies != 0 || d.Server.AccessKey != "demonware-d3" {
		t.Fatalf("title split %+v", d)
	}
}
