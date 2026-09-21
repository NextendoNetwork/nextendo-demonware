package main

// bdMatchMaking (service 21): public games.
//
// Seen live (Citron, quick match): 21/5 findSessions, then 21/1
// createSession, then 21/2 updateSession. Object format read from D3's
// requests and cross-checked against Ezz-lol/boiii-free's bdMatchMakingInfo:
//
//	createSession (21/1)  : info
//	updateSession (21/2)  : 13 blob sessionID[8] | info
//	deleteSession (21/3)  : 13 blob sessionID[8]
//	updatePlayers (21/12) : 13 blob sessionID[8] | 08 u32 numPlayers | info
//	findSessions  (21/5)  : 08 u32 query | 08 u32 start | 08 u32 max | typed filters
//
//	info (client -> server) : 13 blob hostAddr | 08 u32 gameType | 08 u32 maxPlayers | typed D3 attributes
//	result (server -> client) : 13 blob hostAddr | 13 blob sessionID[8] | 08 u32 gameType
//	                            | 08 u32 maxPlayers | 08 u32 numPlayers | typed D3 attributes
//
// hostAddr (67 bytes) is a bdCommonAddr: local IP + port, relay addresses,
// public IP + port, NAT type. The game uses it to connect P2P to the host;
// the server only relays it.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	svcMatchMaking = 21

	mmCreateSession     = 1
	mmUpdateSession     = 2
	mmDeleteSession     = 3
	mmFindSessions      = 5
	mmUpdatePlayers     = 12
	mmRequestSessionID  = 19
	mmInitializeSession = 20
)

type mmSession struct {
	context    string
	id         [8]byte
	owner      uint64 // host's lobby connection number, 0 if disconnected
	ownerPID   uint64
	orphaned   time.Time
	hostAddr   []byte
	gameType   uint32
	maxPlayers uint32
	numPlayers uint32
	attrs      []byte // typed bytes exactly as sent by the host, after maxPlayers
	updated    time.Time
	reserved   bool // ID allocated, but no room information published yet.
}

// orphanGrace: the client re-authenticates in a loop, so a game must survive its
// host reconnecting in order to stay joinable.
const orphanGrace = 2 * time.Minute

func (l *lobbyConn) pid() uint64 {
	if l.player == nil {
		return 0
	}
	return l.player.PID
}

func (l *lobbyConn) owns(s *mmSession) bool {
	if s.owner == l.n {
		return true
	}
	pid := l.pid()
	return pid != 0 && s.ownerPID == pid
}

// reapSessions removes games whose host never came back.
func reapSessions() {
	for range time.Tick(30 * time.Second) {
		reapSessionsOnce()
	}
}

func reapSessionsOnce() {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	for id, s := range sessions {
		if s.owner == 0 && time.Since(s.orphaned) > orphanGrace {
			delete(sessions, id)
		}
	}
}

var (
	sessionsMu sync.Mutex
	sessions   = map[[8]byte]*mmSession{}
)

func (r *bdReader) u32() (uint32, error) {
	if err := r.tag(tagU32); err != nil {
		return 0, err
	}
	if r.off+4 > len(r.b) {
		return 0, errShort
	}
	v := binary.LittleEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v, nil
}

func (r *bdReader) blob() ([]byte, error) {
	if err := r.tag(tagBlob); err != nil {
		return nil, err
	}
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	if r.off+int(n) > len(r.b) {
		return nil, errShort
	}
	v := append([]byte{}, r.b[r.off:r.off+int(n)]...)
	r.off += int(n)
	return v, nil
}

// rest returns the remaining bytes without the null byte that ends every request.
func (r *bdReader) rest() []byte {
	v := r.b[r.off:]
	if len(v) > 0 && v[len(v)-1] == 0 {
		v = v[:len(v)-1]
	}
	return append([]byte{}, v...)
}

func (w *bdWriter) raw(p []byte) { w.b = append(w.b, p...) }

// readInfo reads a bdMatchMakingInfo sent by the client.
func readInfo(r *bdReader) (host []byte, gameType, maxPlayers uint32, attrs []byte, err error) {
	if host, err = r.blob(); err != nil {
		return
	}
	if gameType, err = r.u32(); err != nil {
		return
	}
	if maxPlayers, err = r.u32(); err != nil {
		return
	}
	attrs = r.rest()
	return
}

// CTR wraps its title-specific attributes when uploading MatchMakingInfo,
// but its result deserializer reads those fields directly after the base info.
// Trailing request options are outside that blob and are not result fields.
func readContextInfo(r *bdReader, context string) (host []byte, gameType, maxPlayers uint32, attrs []byte, err error) {
	if !strings.HasPrefix(context, "ctr:") {
		return readInfo(r)
	}
	if host, err = r.blob(); err != nil {
		return
	}
	if gameType, err = r.u32(); err != nil {
		return
	}
	if maxPlayers, err = r.u32(); err != nil {
		return
	}
	if r.off < len(r.b) {
		attrs, err = r.blob()
	}
	return
}

// dropSessionsOf detaches the games of a closed connection; reapSessions removes
// them if the host does not come back.
func dropSessionsOf(conn uint64) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	for id, s := range sessions {
		if s.owner != conn {
			continue
		}
		if s.ownerPID == 0 {
			delete(sessions, id)
			continue
		}
		s.owner, s.orphaned = 0, time.Now()
	}
}

// onFriendSessions (138/14): "of these ids, who hosts a game?"
// Without an answer the game shows NOT JOINABLE for every online friend.
func (l *lobbyConn) onFriendSessions(task byte, r *bdReader, context string) []byte {
	ids := r.u64s()
	me := l.pid()
	seen := map[[8]byte]bool{}
	type hit struct {
		id uint64
		s  *mmSession
	}
	var found []hit
	for _, id := range ids {
		pid := pidOnline(id)
		if pid == 0 {
			// On Citron the friend id IS the PID; the host may be
			// reconnecting and so missing from the online list.
			pid = id
		}
		if pid == me {
			continue
		}
		sessionsMu.Lock()
		for _, s := range sessions {
			if s.ownerPID == pid && s.context == context && !s.reserved && !seen[s.id] {
				seen[s.id] = true
				copy := *s
				found = append(found, hit{id, &copy})
			}
		}
		sessionsMu.Unlock()
	}
	l.logf("matchmaking FRIENDS context=%q %d asked -> %d game(s)", context, len(ids), len(found))
	// The game ties each result to a friend by the online id carried BY the game
	// (Net::MatchMakingInfo +0x170, inside attrs): nothing prefixes the record,
	// which is a raw bdMatchMakingInfo.
	return taskReply(task, 0, func(w *bdWriter) uint32 {
		for _, h := range found {
			w.blobv(h.s.hostAddr)
			w.blobv(h.s.id[:])
			w.u32(h.s.gameType)
			w.u32(h.s.maxPlayers)
			w.u32(h.s.numPlayers)
			w.raw(h.s.attrs)
		}
		return uint32(len(found))
	})
}

// onMatchMaking handles service 21. Returns nil if the task isn't handled.
func (l *lobbyConn) onMatchMaking(task byte, r *bdReader) []byte {
	return l.onMatchMakingContext(task, r, "d3")
}

func (l *lobbyConn) onMatchMakingContext(task byte, r *bdReader, context string) []byte {
	switch task {
	case mmRequestSessionID:
		s := &mmSession{context: context, owner: l.n, ownerPID: l.pid(), reserved: true, updated: time.Now()}
		if _, err := rand.Read(s.id[:]); err != nil {
			return taskReply(task, errUnhandled, nil)
		}
		sessionsMu.Lock()
		for id, old := range sessions {
			if old.reserved && old.context == context && l.owns(old) {
				delete(sessions, id)
			}
		}
		sessions[s.id] = s
		sessionsMu.Unlock()
		l.logf("matchmaking RESERVE session=%X", s.id)
		return taskReply(task, 0, func(w *bdWriter) uint32 { w.blobv(s.id[:]); return 1 })
	case mmCreateSession:
		host, gt, maxp, attrs, err := readContextInfo(r, context)
		if err != nil {
			l.logf("createSession unreadable: %v", err)
			return taskReply(task, errUnhandled, nil)
		}
		s := &mmSession{owner: l.n, ownerPID: l.pid(), hostAddr: host, gameType: gt, maxPlayers: maxp, numPlayers: 1, attrs: attrs, updated: time.Now()}
		_, _ = rand.Read(s.id[:])
		s.context = context
		sessionsMu.Lock()
		// A host recreating its game after reconnecting replaces the old one.
		if s.ownerPID != 0 {
			for old, prev := range sessions {
				if prev.ownerPID == s.ownerPID && prev.context == context {
					delete(sessions, old)
				}
			}
		}
		sessions[s.id] = s
		n := len(sessions)
		sessionsMu.Unlock()
		l.logf("matchmaking CREATE session=%X gameType=%d max=%d host=%s (%d open games)",
			s.id, gt, maxp, hex.EncodeToString(host[:min(6, len(host))]), n)
		return taskReply(task, 0, func(w *bdWriter) uint32 { w.blobv(s.id[:]); return 1 })

	case mmUpdateSession, mmInitializeSession:
		sid, err := r.blob()
		if err != nil || len(sid) != 8 {
			l.logf("updateSession: id unreadable (%v)", err)
			return taskReply(task, 0, nil)
		}
		host, gt, maxp, attrs, err := readContextInfo(r, context)
		if err != nil {
			l.logf("updateSession %X: info unreadable: %v", sid, err)
			return taskReply(task, 0, nil)
		}
		var id [8]byte
		copy(id[:], sid)
		sessionsMu.Lock()
		s, ok := sessions[id]
		if ok && s.context == context && l.owns(s) {
			s.owner, s.ownerPID = l.n, l.pid()
			s.hostAddr, s.gameType, s.maxPlayers, s.attrs, s.updated = host, gt, maxp, attrs, time.Now()
			if task == mmInitializeSession {
				s.reserved = false
				s.numPlayers = 1
			}
		} else {
			sessionsMu.Unlock()
			return taskReply(task, errUnhandled, nil)
		}
		sessionsMu.Unlock()
		l.logf("matchmaking UPDATE session=%X known=%v", id, ok)
		return taskReply(task, 0, nil)

	case mmUpdatePlayers:
		// Seen live when the console joined Citron's game:
		// 13 blob sessionID | 08 u32 numPlayers | full info.
		sid, err := r.blob()
		if err != nil || len(sid) != 8 {
			l.logf("updateSessionPlayers: id unreadable (%v)", err)
			return taskReply(task, 0, nil)
		}
		players, _ := r.u32()
		host, gt, maxp, attrs, err := readContextInfo(r, context)
		var id [8]byte
		copy(id[:], sid)
		sessionsMu.Lock()
		s, ok := sessions[id]
		if ok && s.context == context && l.owns(s) {
			s.owner, s.ownerPID = l.n, l.pid()
			s.numPlayers = players
			if err == nil {
				s.hostAddr, s.gameType, s.maxPlayers, s.attrs = host, gt, maxp, attrs
			}
			s.updated = time.Now()
		}
		sessionsMu.Unlock()
		l.logf("matchmaking PLAYERS session=%X players=%d/%d known=%v", id, players, maxp, ok)
		return taskReply(task, 0, nil)

	case mmDeleteSession:
		sid, err := r.blob()
		if err == nil && len(sid) == 8 {
			var id [8]byte
			copy(id[:], sid)
			sessionsMu.Lock()
			if s := sessions[id]; s != nil && s.context == context && l.owns(s) {
				delete(sessions, id)
			}
			sessionsMu.Unlock()
			l.logf("matchmaking DELETE session=%X", id)
		}
		return taskReply(task, 0, nil)

	case mmFindSessions:
		query, _ := r.u32()
		start, _ := r.u32()
		maxr, _ := r.u32()
		if maxr == 0 || maxr > 50 {
			maxr = 50
		}
		sessionsMu.Lock()
		found := make([]*mmSession, 0, len(sessions))
		for _, s := range sessions {
			if !l.owns(s) && s.context == context && !s.reserved {
				found = append(found, s)
			}
		}
		sessionsMu.Unlock()
		if int(start) < len(found) {
			found = found[start:]
		} else {
			found = nil
		}
		if len(found) > int(maxr) {
			found = found[:maxr]
		}
		l.logf("matchmaking FIND query=%d start=%d max=%d -> %d game(s)", query, start, maxr, len(found))
		return taskReply(task, 0, func(w *bdWriter) uint32 {
			for _, s := range found {
				w.blobv(s.hostAddr)
				w.blobv(s.id[:])
				w.u32(s.gameType)
				w.u32(s.maxPlayers)
				w.u32(s.numPlayers)
				w.raw(s.attrs)
			}
			return uint32(len(found))
		})
	}
	return nil
}
