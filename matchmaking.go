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
	"sync"
	"time"
)

const (
	svcMatchMaking = 21

	mmCreateSession = 1
	mmUpdateSession = 2
	mmDeleteSession = 3
	mmFindSessions  = 5
	mmUpdatePlayers = 12
)

type mmSession struct {
	id         [8]byte
	owner      uint64 // host's lobby connection number
	hostAddr   []byte
	gameType   uint32
	maxPlayers uint32
	numPlayers uint32
	attrs      []byte // typed bytes exactly as sent by the host, after maxPlayers
	updated    time.Time
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

// dropSessionsOf removes the games of a closed connection.
func dropSessionsOf(conn uint64) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	for id, s := range sessions {
		if s.owner == conn {
			delete(sessions, id)
		}
	}
}

// onMatchMaking handles service 21. Returns nil if the task isn't handled.
func (l *lobbyConn) onMatchMaking(task byte, r *bdReader) []byte {
	switch task {
	case mmCreateSession:
		host, gt, maxp, attrs, err := readInfo(r)
		if err != nil {
			l.logf("createSession unreadable: %v", err)
			return taskReply(task, errUnhandled, nil)
		}
		s := &mmSession{owner: l.n, hostAddr: host, gameType: gt, maxPlayers: maxp, numPlayers: 1, attrs: attrs, updated: time.Now()}
		_, _ = rand.Read(s.id[:])
		sessionsMu.Lock()
		sessions[s.id] = s
		n := len(sessions)
		sessionsMu.Unlock()
		l.logf("matchmaking CREATE session=%X gameType=%d max=%d host=%s (%d open games)",
			s.id, gt, maxp, hex.EncodeToString(host[:min(6, len(host))]), n)
		return taskReply(task, 0, func(w *bdWriter) uint32 { w.blobv(s.id[:]); return 1 })

	case mmUpdateSession:
		sid, err := r.blob()
		if err != nil || len(sid) != 8 {
			l.logf("updateSession: id unreadable (%v)", err)
			return taskReply(task, 0, nil)
		}
		host, gt, maxp, attrs, err := readInfo(r)
		if err != nil {
			l.logf("updateSession %X: info unreadable: %v", sid, err)
			return taskReply(task, 0, nil)
		}
		var id [8]byte
		copy(id[:], sid)
		sessionsMu.Lock()
		s, ok := sessions[id]
		if ok {
			s.hostAddr, s.gameType, s.maxPlayers, s.attrs, s.updated = host, gt, maxp, attrs, time.Now()
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
		host, gt, maxp, attrs, err := readInfo(r)
		var id [8]byte
		copy(id[:], sid)
		sessionsMu.Lock()
		s, ok := sessions[id]
		if ok {
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
			delete(sessions, id)
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
			if s.owner != l.n {
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
