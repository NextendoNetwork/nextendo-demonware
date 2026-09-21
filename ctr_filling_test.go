package main

import (
	"encoding/json"
	"strconv"
	"testing"
)

func fillTestRoom(t *testing.T, m *ctrMatchmaker, base uint64, count int) *ctrRoom {
	t.Helper()
	a := testCTRSearch(t, base, 0)
	m.enqueue(a)
	out := m.enqueue(testCTRSearch(t, base+1, 0))
	r := m.rooms[out[0].ids[1]]
	for i := 2; i < count; i++ {
		m.enqueue(testCTRSearch(t, base+uint64(i), 0))
	}
	states := map[string]int{}
	for _, p := range r.players {
		states[strconv.FormatUint(p.pid, 10)] = 2
	}
	r.hostDoc["player_state"] = states
	raw, _ := json.Marshal(r.hostDoc)
	if ok, _ := m.syncDoc(a.conn, r.id, r.version, string(raw)); !ok {
		t.Fatal("sync failed")
	}
	return r
}

func TestCTRFailedReservationsAllowFivePlusThree(t *testing.T) {
	m := ctrMatchmaker{rooms: map[uint64]*ctrRoom{}}
	r := fillTestRoom(t, &m, 1000, 8)
	states := map[string]int{}
	for i := 0; i < 8; i++ {
		state := 2
		if i >= 5 {
			state = 3
		}
		states[strconv.Itoa(1000+i)] = state
	}
	r.hostDoc["player_state"] = states
	raw, _ := json.Marshal(r.hostDoc)
	if ok, _ := m.syncDoc(r.host.conn, r.id, r.version, string(raw)); !ok {
		t.Fatal("sync rejected")
	}
	if r.occupancy() != 5 {
		t.Fatalf("failed joins consume seats: %d", r.occupancy())
	}
	party := testCTRSearch(t, 2000, 0)
	party.doc.Members = []uint64{2000, 2001, 2002}
	party.seats = 3
	out := m.enqueue(party)
	if len(out) != 1 || out[0].event != ctrUpdateLobby || out[0].ids[0] != r.id || len(m.rooms) != 1 {
		t.Fatal("party did not backfill five-player room")
	}
	h, b := r.documents()
	var backend struct {
		Players map[string]string `json:"players"`
	}
	json.Unmarshal([]byte(b), &backend)
	if len(backend.Players) != 8 || backend.Players["1005"] != "" || backend.Players["2002"] != "expected" {
		t.Fatal("incorrect expected membership")
	}
	ok, notices := m.syncDoc(r.host.conn, r.id, r.version, h)
	if !ok || len(notices) != 1 || notices[0].conn != party.conn || notices[0].event != ctrJoinLobby {
		t.Fatal("party not released after host sync")
	}
	if ok, notices := m.syncDoc(r.host.conn, r.id, r.version, h); !ok || len(notices) != 0 {
		t.Fatal("sync generated repeated notifications")
	}
}

func TestCTRFullestOpenRoomWins(t *testing.T) {
	m := ctrMatchmaker{rooms: map[uint64]*ctrRoom{}}
	fuller := fillTestRoom(t, &m, 3000, 5)
	fuller.hostDoc["lobby_open"] = false
	smaller := fillTestRoom(t, &m, 4000, 2)
	fuller.hostDoc["lobby_open"] = true
	out := m.enqueue(testCTRSearch(t, 5000, 0))
	if len(out) != 1 || out[0].ids[0] != fuller.id {
		t.Fatal("did not fill fullest room")
	}
	fuller.hostDoc["lobby_open"] = false
	out = m.enqueue(testCTRSearch(t, 5001, 0))
	if len(out) != 1 || out[0].ids[0] != smaller.id {
		t.Fatal("selected closed room")
	}
}

func TestCTRRejectDuplicatePartyMembers(t *testing.T) {
	s := testCTRSearch(t, 6000, 0)
	s.doc.Members = []uint64{6000, 6000}
	raw, _ := json.Marshal(s.doc)
	if _, err := parseCTRSearch(s.conn, string(raw)); err == nil {
		t.Fatal("duplicate members accepted")
	}
	s.doc.Members = []uint64{6000, 0}
	raw, _ = json.Marshal(s.doc)
	if _, err := parseCTRSearch(s.conn, string(raw)); err == nil {
		t.Fatal("zero member accepted")
	}
}

func TestCTRWaitingSearchBackfilledWhenHostOpens(t *testing.T) {
	m := ctrMatchmaker{rooms: map[uint64]*ctrRoom{}}
	r := fillTestRoom(t, &m, 7000, 5)
	r.hostDoc["lobby_open"] = false
	guest := testCTRSearch(t, 8000, 0)
	if out := m.enqueue(guest); len(out) != 0 || len(m.waiting) != 1 {
		t.Fatal("guest should wait while room closed")
	}
	r.hostDoc["lobby_open"] = true
	h, _ := r.documents()
	ok, out := m.syncDoc(r.host.conn, r.id, r.version, h)
	if !ok || len(out) != 1 || out[0].event != ctrUpdateLobby || len(m.waiting) != 0 {
		t.Fatal("opening room did not pick up existing waiter")
	}
	h, _ = r.documents()
	ok, out = m.syncDoc(r.host.conn, r.id, r.version, h)
	if !ok || len(out) != 1 || out[0].event != ctrJoinLobby || out[0].conn != guest.conn {
		t.Fatal("waiter not released after host acknowledged membership")
	}
}
