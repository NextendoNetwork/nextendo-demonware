package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func testCTRSearch(t *testing.T, pid uint64, playlist int) *ctrSearch {
	t.Helper()
	info := ctrPlayerInfo{}
	info.Listen.Address = base64.StdEncoding.EncodeToString(make([]byte, 67))
	info.Listen.ID = base64.StdEncoding.EncodeToString(make([]byte, 8))
	info.Listen.Key = base64.StdEncoding.EncodeToString(make([]byte, 16))
	raw, _ := json.Marshal(info)
	l := &lobbyConn{n: pid, player: &playerID{PID: pid}, lobbyDoc: string(raw)}
	search, err := parseCTRSearch(l, fmt.Sprintf(`{"members":[%d],"lobby_slots":{"min":2,"max":8},"ruleset_payload":{"filter":{"playlist_id":%d,"matchmaking_version":200}}}`, pid, playlist))
	if err != nil {
		t.Fatal(err)
	}
	return search
}
func TestCTRPublicPairingAndDocuments(t *testing.T) {
	m := ctrMatchmaker{rooms: map[uint64]*ctrRoom{}}
	a, b, c := testCTRSearch(t, 101, 0), testCTRSearch(t, 102, 0), testCTRSearch(t, 103, 1)
	if len(m.enqueue(a)) != 0 || len(m.enqueue(c)) != 0 {
		t.Fatal("matched incompatible searches")
	}
	out := m.enqueue(b)
	if len(out) != 1 || out[0].event != ctrCreateLobby || out[0].conn != a.conn {
		t.Fatal("host must create lobby before guests join")
	}
	id := out[0].ids[1]
	ready, notifications := m.syncDoc(a.conn, id, 1, out[0].host)
	if !ready || len(notifications) != 1 || notifications[0].event != ctrJoinLobby || notifications[0].conn != b.conn {
		t.Fatal("ready host did not release guest join")
	}
	out = append(out, notifications[0])

	if len(m.waiting) != 1 || m.waiting[0] != c {
		t.Fatal("lost incompatible search")
	}
	for _, n := range out {
		w := &bdWriter{}
		n.write(w)
		r := &bdReader{b: w.b}
		wantIDs := 2
		if n.event == 2005 {
			wantIDs = 3
		}
		for i := 0; i < wantIDs; i++ {
			if id, err := r.u64(); err != nil || id == 0 {
				t.Fatal("invalid notification ID", err)
			}
		}
		h, e1 := r.str()
		b, e2 := r.str()
		if e1 != nil || e2 != nil || r.off != len(r.b) {
			t.Fatal("notification wire layout")
		}
		var host, backend map[string]json.RawMessage
		if json.Unmarshal([]byte(h), &host) != nil || json.Unmarshal([]byte(b), &backend) != nil {
			t.Fatal("invalid documents")
		}
		for _, k := range []string{"update_id", "update_time", "lobby_open", "lobby_open_for_pres_join", "player_state", "listen_server", "team_balance", "ruleset_payload", "attachment"} {
			if _, ok := host[k]; !ok {
				t.Fatal("missing host field", k)
			}
		}
		for _, k := range []string{"update_id", "update_time", "lobby_id", "create_time", "players", "dtls", "ruleset_payload"} {
			if _, ok := backend[k]; !ok {
				t.Fatal("missing backend field", k)
			}
		}
		var players map[string]string
		json.Unmarshal(backend["players"], &players)
		if len(players) != 2 || players["101"] != "expected" || players["102"] != "expected" {
			t.Fatal("incorrect expected players")
		}
	}

	h, _, ok := m.documents(a.conn, id)
	if !ok {
		t.Fatal("host cannot get room")
	}
	if ok, _ := m.syncDoc(b.conn, id, 1, h); ok {
		t.Fatal("guest modified host document")
	}
	// The host re-syncs at the SAME version: the version tracks membership and
	// nobody has joined since. This is what the real client does -- it echoes the
	// update_id of the document it composed, so requiring a higher number here
	// refused every host sync from the second one onwards, and with it the host's
	// ability to close its own lobby.
	if ok, updates := m.syncDoc(a.conn, id, 1, h); !ok || len(updates) != 0 {
		t.Fatal("host sync failed")
	}
	if ok, _ := m.syncDoc(a.conn, id, 99, h); ok {
		t.Fatal("wrong version accepted")
	}
	m.leave(a.conn, 0)
	if len(m.rooms) != 0 {
		t.Fatal("disconnected host room retained")
	}
	if !m.cancel(c.conn, c.id) || len(m.waiting) != 0 {
		t.Fatal("search cancellation failed")
	}
}
func TestCTRSearchRetryCannotMatchItself(t *testing.T) {
	m := ctrMatchmaker{rooms: map[uint64]*ctrRoom{}}
	a := testCTRSearch(t, 201, 0)
	m.enqueue(a)
	retry := testCTRSearch(t, 201, 0)
	if len(m.enqueue(retry)) != 0 || len(m.waiting) != 1 {
		t.Fatal("duplicate account matched")
	}
	m.leave(retry.conn, 0)
	if len(m.waiting) != 0 {
		t.Fatal("disconnected search retained")
	}
}
