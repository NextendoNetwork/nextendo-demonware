package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCTRUmbrellaIssuedTicket(t *testing.T) {
	old := sessDir
	sessDir = t.TempDir()
	defer func() { sessDir = old }()
	ticket := make([]byte, 128)
	binary.LittleEndian.PutUint32(ticket, ticketMagic)
	binary.LittleEndian.PutUint32(ticket[5:], 5775)
	binary.LittleEndian.PutUint32(ticket[13:], uint32(time.Now().Unix()))
	binary.LittleEndian.PutUint64(ticket[25:], 1800000119)
	copy(ticket[33:], []byte("test-session-key"))
	call := func(titleID any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"ticket": base64.StdEncoding.EncodeToString(ticket), "titleID": titleID})
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1.0/tokens/lsg/", bytes.NewReader(body))
		handleCTRUmbrella(w, r, body, 1, "")
		return w
	}
	if w := call(5775); w.Code != 401 {
		t.Fatalf("unissued ticket accepted: %d", w.Code)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "test.tkt"), ticket, 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(playerID{PID: 1800003406})
	if err := os.WriteFile(filepath.Join(sessDir, fmt.Sprintf("id_%x.json", ticket[33:41])), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The client sends its integers as JSON strings, so both forms must pass.
	if w := call("5775"); w.Code != 200 {
		t.Fatalf("string-encoded titleID rejected: %d %s", w.Code, w.Body)
	}
	w := call(5775)
	if w.Code != 200 {
		t.Fatalf("issued ticket rejected: %d", w.Code)
	}
	var result struct {
		ID       uint64 `json:"umbrellaID"`
		Token    string `json:"accessToken"`
		Expires  int64  `json:"expires"`
		Accounts []any  `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != 1800003406 || result.Token == "" || result.Expires <= time.Now().Unix() || result.Accounts == nil {
		t.Fatal("incomplete CTR account response")
	}
}

// Un ami reste joignable pendant que son hote se reconnecte.
func TestCTRFriendSessionSurvivesReconnect(t *testing.T) {
	host := &lobbyConn{n: 5551, player: &playerID{Username: "Mars", PID: 1800000119}}
	defer dropSessionsOf(host.n)
	info := &bdWriter{}
	info.blobv(make([]byte, 67))
	info.u32(8)
	info.u32(8)
	host.onMatchMakingContext(mmCreateSession, &bdReader{b: info.b}, "ctr:Delta")

	dropSessionsOf(host.n) // l'hote perd sa connexion

	friend := &lobbyConn{n: 5552, player: &playerID{Username: "Collecting", PID: 1800003406}}
	ask := &bdWriter{}
	ask.u64(1800000119)
	reply := friend.onFriendSessions(14, &bdReader{b: ask.b}, "ctr:Delta")
	if n := binary.LittleEndian.Uint32(reply[17:21]); n != 1 {
		t.Fatalf("orphaned host session not offered to friend: %d results", n)
	}

	sessionsMu.Lock()
	for _, s := range sessions {
		if s.ownerPID == 1800000119 {
			s.orphaned = time.Now().Add(-2 * orphanGrace)
		}
	}
	sessionsMu.Unlock()
	reapSessionsOnce()
	reply = friend.onFriendSessions(14, &bdReader{b: ask.b}, "ctr:Delta")
	if n := binary.LittleEndian.Uint32(reply[17:21]); n != 0 {
		t.Fatalf("abandoned session still offered: %d results", n)
	}
}

// Le code HTTP est ecrit dans les champs 1 ET 2 : bdHTTPProxyResponse lit le champ 2, la voie REST
// (bdRESTLSGResponseMessageDeserializer) lit le champ 1. Le corps reste le champ 3.
func TestCTRHTTPProxyReplyEncoding(t *testing.T) {
	body := []byte(`{"enrollments":[]}`)
	reply := httpProxyReply(1, 200, body)
	i := 16 // transaction + error + task; structured replies have no result counts.
	if len(reply) < i+6 || reply[i] != tagStruct {
		t.Fatalf("no struct buffer immediately after task header: %x", reply)
	}
	sb := reply[i+6:] // tagStruct + (tagU32 + u32 longueur)
	want := append([]byte{0x08, 0xC8, 0x01, 0x10, 0xC8, 0x01, 0x1A, byte(len(body))}, body...)
	if !bytes.Equal(sb, want) {
		t.Fatalf("struct buffer\n got %x\nwant %x", sb, want)
	}
}

// La presence d'un ami connecte revient ; celle d'un joueur parti, non.
func TestCTRRichPresenceRoundTrip(t *testing.T) {
	host := &lobbyConn{n: 7771, player: &playerID{Username: "Mars", PID: 1800000119}}
	onlineMu.Lock()
	online[host.n] = host.player
	onlineMu.Unlock()
	defer dropOnline(host.n)

	set := &bdWriter{}
	set.str("")
	set.u64(1800000119)
	set.str("nintendo")
	set.u8(2)
	set.blobv([]byte("racing"))
	host.onRichPresence(rpSet, &bdReader{b: set.b})

	ask := func(id uint64) []byte {
		q := &bdWriter{}
		q.str("")
		q.u32(1)
		q.u64(id)
		q.str("nintendo")
		peer := &lobbyConn{n: 7772, player: &playerID{Username: "Collecting", PID: 1800003406}}
		return peer.onRichPresence(rpGetAndSubscribe, &bdReader{b: q.b})
	}
	if n := binary.LittleEndian.Uint32(ask(1800000119)[17:21]); n != 1 {
		t.Fatalf("online friend presence missing: %d results", n)
	}
	if n := binary.LittleEndian.Uint32(ask(1800999999)[17:21]); n != 0 {
		t.Fatalf("offline player reported present: %d results", n)
	}
}

func TestCTRSessionCreateDelete(t *testing.T) {
	l := &lobbyConn{n: 987654}
	defer dropSessionsOf(l.n)
	info := &bdWriter{}
	info.blobv(make([]byte, 67))
	info.u32(8)
	info.u32(8)
	info.blobv([]byte("CTR attributes"))
	response := l.onMatchMakingContext(1, &bdReader{b: info.b}, "ctr:Delta")
	if len(response) < 26 || binary.LittleEndian.Uint32(response[17:21]) != 1 {
		t.Fatalf("create must return one session ID: %x", response)
	}
	var session *mmSession
	sessionsMu.Lock()
	for _, s := range sessions {
		if s.owner == l.n {
			session = s
			break
		}
	}
	sessionsMu.Unlock()
	if session == nil || session.context != "ctr:Delta" {
		t.Fatal("missing stored CTR session")
	}
	args := &bdWriter{}
	args.blobv(session.id[:])
	other := &lobbyConn{n: 987655}
	other.onMatchMakingContext(3, &bdReader{b: args.b}, "ctr:Delta")
	sessionsMu.Lock()
	_, exists := sessions[session.id]
	sessionsMu.Unlock()
	if !exists {
		t.Fatal("another connection deleted the session")
	}
	l.onMatchMakingContext(3, &bdReader{b: args.b}, "ctr:Delta")
	sessionsMu.Lock()
	_, exists = sessions[session.id]
	sessionsMu.Unlock()
	if exists {
		t.Fatal("owner deletion did not remove the session")
	}
}
