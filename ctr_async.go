package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event layouts verified against CTR 1.0.15's bdAsyncMatchMakingEventHandler.
const (
	ctrJoinLobby   = 2001
	ctrCreateLobby = 2005
	ctrUpdateLobby = 2006
)

type ctrSearchDoc struct {
	Members []uint64 `json:"members"`
	Slots   struct {
		Min int `json:"min"`
		Max int `json:"max"`
	} `json:"lobby_slots"`
	Rules struct {
		Filter map[string]json.RawMessage `json:"filter"`
	} `json:"ruleset_payload"`
}
type ctrPlayerInfo struct {
	Listen struct {
		Address string `json:"local_address"`
		ID      string `json:"security_id"`
		Key     string `json:"security_key"`
	} `json:"listen_server"`
}
type ctrSearch struct {
	conn    *lobbyConn
	id, pid uint64
	doc     ctrSearchDoc
	info    ctrPlayerInfo
	filter  string
}
type ctrRoom struct {
	id, version uint64
	created     int64
	host        *ctrSearch
	players     []*ctrSearch
	hostDoc     map[string]any
	capacity    int
	notified    map[*lobbyConn]bool
}
type ctrMatchmaker struct {
	mu      sync.Mutex
	waiting []*ctrSearch
	rooms   map[uint64]*ctrRoom
}

var ctrMM = ctrMatchmaker{rooms: make(map[uint64]*ctrRoom)}
var ctrIDs atomic.Uint64

func parseCTRSearch(l *lobbyConn, raw string) (*ctrSearch, error) {
	s := &ctrSearch{conn: l, pid: l.pid()}
	if json.Unmarshal([]byte(raw), &s.doc) != nil || s.pid == 0 || len(s.doc.Members) != 1 || s.doc.Members[0] != s.pid || s.doc.Slots.Min < 1 || s.doc.Slots.Max < s.doc.Slots.Min || s.doc.Slots.Max > 8 || len(s.doc.Rules.Filter) == 0 {
		return nil, errors.New("invalid search or unsupported party membership")
	}
	if json.Unmarshal([]byte(l.lobbyDoc), &s.info) != nil {
		return nil, errors.New("missing player info")
	}
	for encoded, n := range map[string]int{s.info.Listen.Address: 67, s.info.Listen.ID: 8, s.info.Listen.Key: 16} {
		b, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(b) != n {
			return nil, errors.New("invalid player address or DTLS data")
		}
	}
	filter, _ := json.Marshal(s.doc.Rules.Filter)
	s.filter = string(filter)
	s.id = ctrIDs.Add(1)
	return s, nil
}

type ctrNotice struct {
	conn          *lobbyConn
	event         uint32
	ids           []uint64
	host, backend string
}

func (n ctrNotice) write(w *bdWriter) {
	for _, id := range n.ids {
		w.u64(id)
	}
	w.strv(n.host)
	w.strv(n.backend)
}
func sendCTRNotices(notices []ctrNotice) {
	for _, n := range notices {
		if err := n.conn.sendPush(n.event, n.write); err != nil {
			n.conn.logf("CTR matchmaking push event=%d failed: %v", n.event, err)
		} else {
			n.conn.logf("CTR matchmaking push event=%d", n.event)
		}
	}
}

func (r *ctrRoom) documents() (string, string) {
	expected := map[string]string{}
	for _, p := range r.players {
		expected[strconv.FormatUint(p.pid, 10)] = "expected"
	}
	backend := map[string]any{
		"update_id": r.version, "update_time": time.Now().Unix(), "lobby_id": r.id, "create_time": r.created,
		"players": expected, "dtls": map[string]string{"security_id": r.host.info.Listen.ID, "security_key": r.host.info.Listen.Key}, "ruleset_payload": r.host.doc.Rules,
	}
	h, _ := json.Marshal(r.hostDoc)
	b, _ := json.Marshal(backend)
	return string(h), string(b)
}
func (r *ctrRoom) notice(p *ctrSearch, event uint32) ctrNotice {
	h, b := r.documents()
	ids := []uint64{p.id, r.id}
	if event == ctrCreateLobby {
		ids = append(ids, r.version)
	}
	if event == ctrUpdateLobby {
		ids = []uint64{r.id, r.version}
	}
	return ctrNotice{p.conn, event, ids, h, b}
}
func (m *ctrMatchmaker) enqueue(s *ctrSearch) []ctrNotice {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Retries must not match an account with itself or create duplicate searches.
	for i := len(m.waiting) - 1; i >= 0; i-- {
		if m.waiting[i].pid == s.pid {
			m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
		}
	}
	for _, r := range m.rooms {
		for _, p := range r.players {
			if p.pid == s.pid {
				return nil
			}
		}
	}
	for _, r := range m.rooms {
		open, _ := r.hostDoc["lobby_open"].(bool)
		if open && r.host.filter == s.filter && len(r.players) < r.capacity && len(r.players) < s.doc.Slots.Max {
			r.players = append(r.players, s)
			r.capacity = min(r.capacity, s.doc.Slots.Max)
			r.version++
			r.hostDoc["update_id"] = r.version
			r.hostDoc["update_time"] = time.Now().Unix()
			out := []ctrNotice{r.notice(r.host, ctrUpdateLobby)}
			return out
		}
	}
	m.waiting = append(m.waiting, s)
	group := []*ctrSearch{}
	minimum, maximum := 2, 8
	for _, p := range m.waiting {
		if p.filter != s.filter {
			continue
		}
		nextMin, nextMax := max(minimum, p.doc.Slots.Min), min(maximum, p.doc.Slots.Max)
		if nextMin > nextMax || len(group) >= nextMax {
			continue
		}
		group = append(group, p)
		minimum, maximum = nextMin, nextMax
	}
	if len(group) < minimum {
		return nil
	}
	h := group[0]
	r := &ctrRoom{id: ctrIDs.Add(1), version: 1, created: time.Now().Unix(), host: h, players: group, capacity: maximum, notified: map[*lobbyConn]bool{h.conn: true}}
	r.hostDoc = map[string]any{
		"update_id": r.version, "update_time": r.created, "game_id": r.id, "lobby_open": true, "lobby_open_for_pres_join": true,
		"player_state":  map[string]uint32{strconv.FormatUint(h.pid, 10): 1},
		"listen_server": lobbyListenServer{HostAddress: h.info.Listen.Address, HostPlayerID: h.pid},
		"team_balance":  lobbyTeamBalance{CanChangeTeams: true}, "ruleset_payload": map[string]any{}, "attachment": nil,
	}
	m.rooms[r.id] = r
	selected := map[*ctrSearch]bool{}
	out := []ctrNotice{r.notice(h, ctrCreateLobby)}
	for _, p := range group {
		selected[p] = true

	}
	remaining := m.waiting[:0]
	for _, p := range m.waiting {
		if !selected[p] {
			remaining = append(remaining, p)
		}
	}
	m.waiting = remaining
	return out
}

func (m *ctrMatchmaker) cancel(conn *lobbyConn, id uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.waiting {
		if p.conn == conn && p.id == id {
			m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
			return true
		}
	}
	return false
}
func (m *ctrMatchmaker) leave(conn *lobbyConn, id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.waiting) - 1; i >= 0; i-- {
		if m.waiting[i].conn == conn {
			m.waiting = append(m.waiting[:i], m.waiting[i+1:]...)
		}
	}
	for roomID, r := range m.rooms {
		if id != 0 && roomID != id {
			continue
		}
		if r.host.conn == conn {
			delete(m.rooms, roomID)
			continue
		}
		for i, p := range r.players {
			if p.conn == conn {
				r.players = append(r.players[:i], r.players[i+1:]...)
				break
			}
		}
	}
}
func (m *ctrMatchmaker) documents(conn *lobbyConn, id uint64) (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.rooms[id]; r != nil {
		for _, p := range r.players {
			if p.conn == conn {
				h, b := r.documents()
				return h, b, true
			}
		}
	}
	return "", "", false
}
func (m *ctrMatchmaker) syncDoc(conn *lobbyConn, id, version uint64, raw string) (bool, []ctrNotice) {
	var doc map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&doc) != nil || doc == nil {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rooms[id]
	if r == nil || r.host.conn != conn || r.version != version {
		return false, nil
	}
	for _, key := range []string{"lobby_open", "lobby_open_for_pres_join", "player_state", "listen_server", "team_balance", "ruleset_payload", "attachment"} {
		if _, ok := doc[key]; !ok {
			return false, nil
		}
	}
	r.version++
	doc["update_id"] = r.version
	doc["update_time"] = time.Now().Unix()
	r.hostDoc = doc
	// 2006 asks the host to produce a new document; it is not an ACK to
	// broadcast. Echoing it on every sync causes an endless sync/push loop.
	// Release pending guests only after the host has supplied its document.
	var out []ctrNotice
	for _, p := range r.players {
		if !r.notified[p.conn] {
			r.notified[p.conn] = true
			out = append(out, r.notice(p, ctrJoinLobby))
		}
	}
	return true, out
}
