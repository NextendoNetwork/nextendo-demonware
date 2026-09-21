package main

import (
	"encoding/json"
	"testing"
)

func mergeTestRooms(t *testing.T) (*ctrMatchmaker, *ctrRoom, *ctrRoom) {
	m := &ctrMatchmaker{rooms: map[uint64]*ctrRoom{}}
	a := fillTestRoom(t, m, 10000, 3)
	a.hostDoc["lobby_open"] = false
	b := fillTestRoom(t, m, 20000, 3)
	a.hostDoc["lobby_open"] = true
	return m, a, b
}

func TestCTRMergeThreePlusThreeAfterDestinationAck(t *testing.T) {
	m, a, b := mergeTestRooms(t)
	h, _ := a.documents()
	ok, out := m.syncDoc(a.host.conn, a.id, a.version, h)
	if !ok || len(out) != 1 || out[0].event != ctrUpdateLobby {
		t.Fatal("merge did not reserve destination first")
	}
	if len(m.rooms) != 2 || a.occupancy() != 6 || len(a.players) != 3 || b.mergeTarget != a.id {
		t.Fatal("invalid reserved merge state")
	}
	w := &bdWriter{}
	out[0].write(w)
	var backend struct {
		Players map[string]string `json:"players"`
	}
	json.Unmarshal([]byte(out[0].backend), &backend)
	if len(backend.Players) != 6 {
		t.Fatal("destination not told about all incoming members")
	}
	if ok, _ := m.syncDoc(a.host.conn, a.id, a.version-1, h); ok {
		t.Fatal("stale host sync accepted")
	}
	h, _ = a.documents()
	ok, out = m.syncDoc(a.host.conn, a.id, a.version, h)
	if !ok || len(out) != 3 || len(m.rooms) != 1 || a.occupancy() != 6 {
		t.Fatal("merge not committed")
	}
	for _, n := range out {
		if n.event != ctrMergeLobby || len(n.ids) != 3 || n.ids[1] != b.id || n.ids[2] != a.id {
			t.Fatal("invalid merge event routing")
		}
		w := &bdWriter{}
		n.write(w)
		rd := &bdReader{b: w.b}
		for _, want := range n.ids {
			if got, err := rd.u64(); err != nil || got != want {
				t.Fatal("merge wire IDs")
			}
		}
		if _, err := rd.str(); err != nil {
			t.Fatal(err)
		}
		if _, err := rd.str(); err != nil {
			t.Fatal(err)
		}
		if rd.off != len(rd.b) {
			t.Fatal("trailing wire bytes")
		}
	}
	if ok, out := m.syncDoc(a.host.conn, a.id, a.version, h); !ok || len(out) != 0 {
		t.Fatal("duplicate merge dispatch")
	}
	// Clients may acknowledge disbanding their OLD room after the handoff.
	m.leave(b.host.conn, b.id)
	if len(a.players) != 6 {
		t.Fatal("old-room teardown removed destination membership")
	}
}

func TestCTRMergeCancelsIfSourceClosesOrLeaves(t *testing.T) {
	for _, closed := range []bool{true, false} {
		m, a, b := mergeTestRooms(t)
		if len(m.startMerge(a)) != 1 {
			t.Fatal("no reservation")
		}
		if closed {
			b.hostDoc["lobby_open"] = false
		} else {
			m.leave(b.host.conn, b.id)
		}
		h, _ := a.documents()
		ok, out := m.syncDoc(a.host.conn, a.id, a.version, h)
		if !ok || len(out) != 1 || out[0].event != ctrUpdateLobby || a.mergeSource != nil || a.occupancy() != 3 {
			t.Fatal("unsafe merge not cancelled")
		}
	}
}

func TestCTRMergeEligibility(t *testing.T) {
	for _, reason := range []string{"closed", "filter", "capacity", "overlap", "unconfirmed"} {
		t.Run(reason, func(t *testing.T) {
			m, a, b := mergeTestRooms(t)
			switch reason {
			case "closed":
				b.hostDoc["lobby_open"] = false
			case "filter":
				b.host.filter = "other"
			case "capacity":
				a.capacity = 5
			case "overlap":
				b.players[1].doc.Members = []uint64{a.host.pid}
			case "unconfirmed":
				b.hostDoc["player_state"].(map[string]any)["20001"] = json.Number("1")
			}
			if len(m.startMerge(a)) != 0 {
				t.Fatalf("merged despite %s", reason)
			}
		})
	}
}

func TestCTRMergeKeepsPartyMembersTogether(t *testing.T) {
	m, a, b := mergeTestRooms(t)
	// A three-player party issues one search from its leader.
	p := b.players[0]
	p.doc.Members = []uint64{20000, 20001, 20002}
	p.seats = 3
	b.players = []*ctrSearch{p}
	if len(m.startMerge(a)) != 1 {
		t.Fatal("party merge not reserved")
	}
	h, _ := a.documents()
	ok, out := m.syncDoc(a.host.conn, a.id, a.version, h)
	if !ok || len(out) != 1 || out[0].conn != p.conn || a.occupancy() != 6 {
		t.Fatal("party split or leader not notified")
	}
	var backend struct {
		Players map[string]string `json:"players"`
	}
	json.Unmarshal([]byte(out[0].backend), &backend)
	for _, pid := range []string{"20000", "20001", "20002"} {
		if backend.Players[pid] != "expected" {
			t.Fatal("missing party member", pid)
		}
	}
}
