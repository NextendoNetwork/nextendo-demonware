package main

// bdMatchMaking (service 21) : les parties publiques.
//
// Vu en direct (Citron, partie rapide) : 21/5 findSessions, puis 21/1
// createSession, puis 21/2 updateSession. Format des objets lu dans les
// requetes D3 et recoupe avec bdMatchMakingInfo de Ezz-lol/boiii-free :
//
//	createSession (21/1)  : info
//	updateSession (21/2)  : 13 blob sessionID[8] | info
//	deleteSession (21/3)  : 13 blob sessionID[8]
//	updatePlayers (21/12) : 13 blob sessionID[8] | 08 u32 nbJoueurs | info
//	findSessions  (21/5)  : 08 u32 requete | 08 u32 debut | 08 u32 max | filtres types
//
//	info (client -> serveur) : 13 blob hostAddr | 08 u32 gameType | 08 u32 maxPlayers | attributs D3 types
//	resultat (serveur -> client) : 13 blob hostAddr | 13 blob sessionID[8] | 08 u32 gameType
//	                               | 08 u32 maxPlayers | 08 u32 numPlayers | attributs D3 types
//
// hostAddr (67 octets) est un bdCommonAddr : IP locale + port, adresses de
// relais, IP publique + port, type de NAT. Le jeu s'en sert pour se connecter
// en P2P a l'hote ; le serveur ne fait que le retransmettre.

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
	owner      uint64 // numero de connexion lobby de l'hote
	hostAddr   []byte
	gameType   uint32
	maxPlayers uint32
	numPlayers uint32
	attrs      []byte // octets types tels qu'envoyes par l'hote, apres maxPlayers
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

// rest rend les octets restants sans l'octet nul qui termine chaque requete.
func (r *bdReader) rest() []byte {
	v := r.b[r.off:]
	if len(v) > 0 && v[len(v)-1] == 0 {
		v = v[:len(v)-1]
	}
	return append([]byte{}, v...)
}

func (w *bdWriter) raw(p []byte) { w.b = append(w.b, p...) }

// readInfo lit un bdMatchMakingInfo envoye par le client.
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

// dropSessionsOf retire les parties d'une connexion fermee.
func dropSessionsOf(conn uint64) {
	sessionsMu.Lock()
	defer sessionsMu.Unlock()
	for id, s := range sessions {
		if s.owner == conn {
			delete(sessions, id)
		}
	}
}

// onMatchMaking traite le service 21. Rend nil si la tache n'est pas geree.
func (l *lobbyConn) onMatchMaking(task byte, r *bdReader) []byte {
	switch task {
	case mmCreateSession:
		host, gt, maxp, attrs, err := readInfo(r)
		if err != nil {
			l.logf("createSession illisible: %v", err)
			return taskReply(task, errUnhandled, nil)
		}
		s := &mmSession{owner: l.n, hostAddr: host, gameType: gt, maxPlayers: maxp, numPlayers: 1, attrs: attrs, updated: time.Now()}
		_, _ = rand.Read(s.id[:])
		sessionsMu.Lock()
		sessions[s.id] = s
		n := len(sessions)
		sessionsMu.Unlock()
		l.logf("matchmaking CREATE session=%X gameType=%d max=%d hote=%s (%d parties ouvertes)",
			s.id, gt, maxp, hex.EncodeToString(host[:min(6, len(host))]), n)
		return taskReply(task, 0, func(w *bdWriter) uint32 { w.blobv(s.id[:]); return 1 })

	case mmUpdateSession:
		sid, err := r.blob()
		if err != nil || len(sid) != 8 {
			l.logf("updateSession: id illisible (%v)", err)
			return taskReply(task, 0, nil)
		}
		host, gt, maxp, attrs, err := readInfo(r)
		if err != nil {
			l.logf("updateSession %X: info illisible: %v", sid, err)
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
		l.logf("matchmaking UPDATE session=%X connue=%v", id, ok)
		return taskReply(task, 0, nil)

	case mmUpdatePlayers:
		// Vu en direct quand la console a rejoint la partie de Citron :
		// 13 blob sessionID | 08 u32 nbJoueurs | info complete.
		sid, err := r.blob()
		if err != nil || len(sid) != 8 {
			l.logf("updateSessionPlayers: id illisible (%v)", err)
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
		l.logf("matchmaking JOUEURS session=%X joueurs=%d/%d connue=%v", id, players, maxp, ok)
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
		l.logf("matchmaking FIND requete=%d debut=%d max=%d -> %d partie(s)", query, start, maxr, len(found))
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
