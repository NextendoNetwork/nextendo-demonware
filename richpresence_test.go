package main

import (
	"encoding/binary"
	"testing"
)

// request builders in the documented layout
func rpStr(s string) []byte  { return append(append([]byte{tagString}, s...), 0) }
func rpU64(v uint64) []byte  { return binary.LittleEndian.AppendUint64([]byte{tagU64}, v) }
func rpU32(v uint32) []byte  { return binary.LittleEndian.AppendUint32([]byte{tagU32}, v) }
func rpBlob(b []byte) []byte { return append(append([]byte{tagBlob}, rpU32(uint32(len(b)))...), b...) }

func rpSetRequest(ctx string, id uint64, platform string, flag byte, data []byte) []byte {
	out := rpStr(ctx)
	out = append(out, rpU64(id)...)
	out = append(out, rpStr(platform)...)
	out = append(out, tagU8, flag)
	return append(out, rpBlob(data)...)
}

func rpGetRequest(ctx string, ids ...uint64) []byte {
	out := append(rpStr(ctx), rpU32(uint32(len(ids)))...)
	for _, id := range ids {
		out = append(append(out, rpU64(id)...), rpStr("nx")...)
	}
	return out
}

// parse a get reply: transaction, error, task, count, [total, rows]
func rpRows(t *testing.T, reply []byte) (rows []richPresence, ids []uint64) {
	t.Helper()
	r := &bdReader{b: reply}
	if _, err := r.u64(); err != nil {
		t.Fatal(err)
	}
	if code, err := r.u32(); err != nil || code != 0 {
		t.Fatalf("error code %d %v", code, err)
	}
	if _, err := r.u8(); err != nil {
		t.Fatal(err)
	}
	n, err := r.u32()
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return nil, nil
	}
	if _, err := r.u32(); err != nil { // total
		t.Fatal(err)
	}
	for i := uint32(0); i < n; i++ {
		id, e1 := r.u64()
		plat, e2 := r.str()
		flag, e3 := r.u8()
		data, e4 := r.blob()
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			t.Fatalf("row %d unreadable", i)
		}
		rows, ids = append(rows, richPresence{plat, flag, data}), append(ids, id)
	}
	return rows, ids
}

func withOnline(t *testing.T, players map[uint64]*playerID) {
	t.Helper()
	onlineMu.Lock()
	old := online
	online = players
	onlineMu.Unlock()
	richMu.Lock()
	rich = map[uint64]richPresence{}
	richMu.Unlock()
	t.Cleanup(func() { onlineMu.Lock(); online = old; onlineMu.Unlock() })
}

func TestRichPresenceSetAndGet(t *testing.T) {
	alice := &playerID{Username: "player1", PID: 1800000101}
	bob := &playerID{Username: "player2", PID: 1800000102}
	withOnline(t, map[uint64]*playerID{1: alice, 2: bob})
	la, lb := &lobbyConn{n: 1, player: alice}, &lobbyConn{n: 2, player: bob}

	// player1 sets a presence, as the game does (id 0 = me)
	la.onRichPresence(rpSet, &bdReader{b: rpSetRequest("", 0, "", 1, []byte("hello"))})

	// player2 asks about player1, by PID (the way Citron identifies a friend)
	rows, ids := rpRows(t, lb.onRichPresence(rpGetAndSubscribe, &bdReader{b: rpGetRequest("", alice.PID)}))
	if len(rows) != 1 || string(rows[0].data) != "hello" || rows[0].flag != 1 || ids[0] != alice.PID {
		t.Fatalf("got %+v ids %v", rows, ids)
	}
	// nothing for someone who set no presence, or is not online
	rows, _ = rpRows(t, lb.onRichPresence(rpGet, &bdReader{b: rpGetRequest("", bob.PID, 999)}))
	if len(rows) != 0 {
		t.Fatalf("unexpected rows %+v", rows)
	}
	// id 0 means the asker
	rows, _ = rpRows(t, la.onRichPresence(rpGet, &bdReader{b: rpGetRequest("", 0)}))
	if len(rows) != 1 {
		t.Fatalf("own presence: %+v", rows)
	}
}

func TestRichPresenceGoneWhenThePlayerLeaves(t *testing.T) {
	alice := &playerID{Username: "player1", PID: 1800000101}
	bob := &playerID{Username: "player2", PID: 1800000102}
	withOnline(t, map[uint64]*playerID{1: alice, 2: bob})
	la, lb := &lobbyConn{n: 1, player: alice}, &lobbyConn{n: 2, player: bob}
	la.onRichPresence(rpSet, &bdReader{b: rpSetRequest("", 0, "", 1, []byte("x"))})

	dropOnline(1)
	if rows, _ := rpRows(t, lb.onRichPresence(rpGet, &bdReader{b: rpGetRequest("", alice.PID)})); len(rows) != 0 {
		t.Fatalf("a player who left is still shown: %+v", rows)
	}
	richMu.Lock()
	_, kept := rich[alice.PID]
	richMu.Unlock()
	if kept {
		t.Error("presence not dropped")
	}
}

func TestRichPresenceFromTheRealLog(t *testing.T) {
	// the bytes after "service 68 task 3" from a real Diablo III login
	raw := []byte{0x10, 0x00, 0x0a, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0x00, 0x03, 0x01, 0x13, 0x08, 0x04, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef}
	alice := &playerID{Username: "player1", PID: 1800000101}
	withOnline(t, map[uint64]*playerID{1: alice})
	l := &lobbyConn{n: 1, player: alice}
	l.onRichPresence(rpSet, &bdReader{b: raw})
	richMu.Lock()
	p, ok := rich[alice.PID]
	richMu.Unlock()
	if !ok || p.flag != 1 || len(p.data) != 4 || p.data[0] != 0xde {
		t.Fatalf("stored %+v ok=%v", p, ok)
	}
}

func TestRichPresenceNeedsAPlayer(t *testing.T) {
	withOnline(t, map[uint64]*playerID{})
	guest := &lobbyConn{n: 9} // no identity
	guest.onRichPresence(rpSet, &bdReader{b: rpSetRequest("", 0, "", 1, []byte("x"))})
	richMu.Lock()
	n := len(rich)
	richMu.Unlock()
	if n != 0 {
		t.Errorf("a player without a PID stored a presence")
	}
	// a truncated request must not panic
	guest.onRichPresence(rpSet, &bdReader{b: []byte{tagString, 0, tagU64, 1}})
	guest.onRichPresence(rpGet, &bdReader{b: rpU32(1 << 30)})
}
