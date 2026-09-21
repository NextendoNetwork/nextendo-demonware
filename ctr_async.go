package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
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
	ctrMergeLobby  = 2008
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
	conn     *lobbyConn
	id, pid  uint64
	doc      ctrSearchDoc
	info     ctrPlayerInfo
	filter   string
	filterID string

	// Seats this search needs: one per party member. Only the party leader sends
	// the search, so counting searches instead of members would sit a party of
	// three in a single seat and overfill the lobby.
	seats int
}
type ctrRoom struct {
	id, version uint64
	created     int64
	host        *ctrSearch
	players     []*ctrSearch
	hostDoc     map[string]any
	capacity    int
	notified    map[*lobbyConn]bool
	mergeSource *ctrRoom
	mergeTarget uint64
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
	// A party searches as ONE request issued by its leader, listing every member.
	// Refusing anything but a lone member meant a duo or trio never entered the
	// queue at all: they watched a lobby wait for players and were never placed
	// in it. The leader must be among the members it claims.
	if json.Unmarshal([]byte(raw), &s.doc) != nil || s.pid == 0 || len(s.doc.Members) < 1 || len(s.doc.Members) > 8 || s.doc.Slots.Min < 1 || s.doc.Slots.Max < s.doc.Slots.Min || s.doc.Slots.Max > 8 || len(s.doc.Rules.Filter) == 0 {
		return nil, errors.New("invalid search or unsupported party membership")
	}
	// The leader must be among the members it claims to be searching for.
	leader := false
	seen := make(map[uint64]bool)
	for _, pid := range s.doc.Members {
		if pid == 0 || seen[pid] {
			return nil, errors.New("zero or duplicate party member")
		}
		seen[pid] = true
		if pid == s.pid {
			leader = true
		}
	}
	if !leader || len(s.doc.Members) > s.doc.Slots.Max {
		return nil, errors.New("invalid search or unsupported party membership")
	}
	s.seats = len(s.doc.Members)
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
	// Players only match when their filters are byte-identical, so a filter that
	// carries anything player-specific silently fragments matchmaking into pairs.
	// The fingerprint makes that visible without logging the payload itself.
	sum := sha256.Sum256(filter)
	s.filterID = hex.EncodeToString(sum[:4])
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

// occupancy counts PEOPLE in the room, not searches: one search may be a party.
func (r *ctrRoom) occupancy() int {
	n := 0
	for _, p := range r.players {
		for _, pid := range p.doc.Members {
			if !r.failed(pid) {
				n++
			}
		}
	}
	if r.mergeSource != nil {
		n += r.mergeSource.occupancy()
	}
	return n
}

// CTR updateLobbyPlayerStatus sets state 2 for an actual peer, and state 3
// when an expected peer times out. A failed join must not reserve a seat forever.
func (r *ctrRoom) state(pid uint64) int {
	states, _ := r.hostDoc["player_state"].(map[string]any)
	v, _ := states[strconv.FormatUint(pid, 10)].(json.Number)
	n, _ := v.Int64()
	return int(n)
}
func (r *ctrRoom) failed(pid uint64) bool {
	return pid != r.host.pid && r.state(pid) == 3
}
func (r *ctrRoom) connected() int {
	n := 0
	for _, p := range r.players {
		for _, pid := range p.doc.Members {
			if r.state(pid) == 2 {
				n++
			}
		}
	}
	return n
}

func (r *ctrRoom) documents() (string, string) {
	expected := map[string]string{}
	players := append([]*ctrSearch(nil), r.players...)
	if r.mergeSource != nil {
		players = append(players, r.mergeSource.players...)
	}
	for _, p := range players {
		// Every member of a party is expected, not just the leader who searched.
		for _, pid := range p.doc.Members {
			if r.failed(pid) {
				continue
			}
			expected[strconv.FormatUint(pid, 10)] = "expected"
		}
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
	// A player who searches again is NOT in a lobby any more, whatever this side
	// still believes. Returning nil here left them stuck forever: they matched
	// once, the client failed to actually join, and every later search was
	// answered with silence because the server still counted them as a member of
	// a room they were never in. Evict them and let them match normally.
	for roomID, r := range m.rooms {
		if r.host.pid == s.pid {
			// The host re-queueing means that lobby is gone.
			delete(m.rooms, roomID)
			continue
		}
		for i, p := range r.players {
			if p.pid == s.pid {
				r.players = append(r.players[:i], r.players[i+1:]...)
				delete(r.notified, p.conn)
				break
			}
		}
	}
	rooms := make([]*ctrRoom, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	sort.Slice(rooms, func(i, j int) bool {
		a, b := rooms[i], rooms[j]
		if a.occupancy() != b.occupancy() {
			return a.occupancy() > b.occupancy()
		}
		return a.id < b.id
	})
	for _, r := range rooms {
		if r.mergeTarget != 0 && m.rooms[r.mergeTarget] == nil {
			r.mergeTarget = 0
		}
		open, _ := r.hostDoc["lobby_open"].(bool)
		reason := "selected"
		switch {
		case r.mergeTarget != 0 || r.mergeSource != nil:
			reason = "merge-pending"
		case !open:
			reason = "closed"
		case r.host.filter != s.filter:
			reason = "filter"
		case r.occupancy()+s.seats > r.capacity:
			reason = "room-capacity"
		case r.occupancy()+s.seats > s.doc.Slots.Max:
			reason = "search-capacity"
		}
		s.conn.logf("CTR candidate search=%d seats=%d filter=%s room=%d room_filter=%s open=%t occupied=%d connected=%d capacity=%d search_max=%d reason=%s", s.id, s.seats, s.filterID, r.id, r.host.filterID, open, r.occupancy(), r.connected(), r.capacity, s.doc.Slots.Max, reason)
		if reason == "selected" {
			// A retry must not inherit its previous failed state in this room.
			if states, ok := r.hostDoc["player_state"].(map[string]any); ok {
				for _, pid := range s.doc.Members {
					delete(states, strconv.FormatUint(pid, 10))
				}
			}
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
	seats := 0
	minimum, maximum := 2, 8
	for _, p := range m.waiting {
		if p.filter != s.filter {
			continue
		}
		nextMin, nextMax := max(minimum, p.doc.Slots.Min), min(maximum, p.doc.Slots.Max)
		if nextMin > nextMax || seats+p.seats > nextMax {
			continue
		}
		group = append(group, p)
		seats += p.seats
		minimum, maximum = nextMin, nextMax
	}
	if seats < minimum {
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
	// The version tracks MEMBERSHIP and is bumped only when someone joins (see
	// enqueue). It used to be bumped here as well, on every accepted sync, which
	// put the room permanently one ahead of the host: the host echoes the
	// update_id of the document IT composed, so its next sync carried the old
	// number and was refused -- and so was every sync after that.
	//
	// A host that cannot sync cannot CLOSE its lobby either. lobby_open stayed
	// true on the server's original document, so the matchmaker kept sending new
	// players into a lobby that had already started racing and could not take
	// them: "you can only join once, and after it starts nobody else gets in".
	doc["update_id"] = r.version
	doc["update_time"] = time.Now().Unix()
	r.hostDoc = doc
	mergeNotices := m.finishMerge(r)
	// Remove only members the host explicitly rejected. Keep connected party
	// members, and do not expire a reservation merely because time passed here.
	kept := r.players[:0]
	released := 0
	for _, p := range r.players {
		members := make([]uint64, 0, len(p.doc.Members))
		for _, pid := range p.doc.Members {
			if r.failed(pid) {
				released++
				continue
			}
			members = append(members, pid)
		}
		if len(members) == 0 {
			delete(r.notified, p.conn)
			continue
		}
		copy := *p
		copy.doc.Members, copy.seats = members, len(members)
		if p == r.host {
			r.host = &copy
		}
		kept = append(kept, &copy)
	}
	r.players = kept
	if released > 0 {
		conn.logf("CTR released failed reservations room=%d seats=%d", r.id, released)
	}
	conn.logf("CTR room-state room=%d version=%d open=%v occupied=%d connected=%d searches=%d", r.id, r.version, doc["lobby_open"], r.occupancy(), r.connected(), len(r.players))
	// 2006 asks the host to produce a new document; it is not an ACK to
	// broadcast. Echoing it on every sync causes an endless sync/push loop.
	// Release pending guests only after the host has supplied its document.
	out := mergeNotices
	for _, p := range r.players {
		if !r.notified[p.conn] {
			r.notified[p.conn] = true
			out = append(out, r.notice(p, ctrJoinLobby))
		}
	}
	// A search already waiting must be reconsidered when the host opens its
	// lobby or reports failed joins. Otherwise it waits for an unrelated new
	// search even though this sync has made suitable seats available.
	if open, _ := doc["lobby_open"].(bool); open && r.mergeTarget == 0 && r.mergeSource == nil {
		remaining := m.waiting[:0]
		added := 0
		for _, p := range m.waiting {
			if p.filter != r.host.filter || r.occupancy()+p.seats > min(r.capacity, p.doc.Slots.Max) {
				remaining = append(remaining, p)
				continue
			}
			if states, ok := doc["player_state"].(map[string]any); ok {
				for _, pid := range p.doc.Members {
					delete(states, strconv.FormatUint(pid, 10))
				}
			}
			r.players = append(r.players, p)
			r.capacity = min(r.capacity, p.doc.Slots.Max)
			added++
			conn.logf("CTR backfill waiting search=%d seats=%d room=%d", p.id, p.seats, r.id)
		}
		m.waiting = remaining
		if added > 0 {
			r.version++
			doc["update_id"] = r.version
			out = append(out, r.notice(r.host, ctrUpdateLobby))
		}
	}
	if len(out) == 0 {
		out = append(out, m.startMerge(r)...)
	}
	return true, out
}
