package main

// bdRichPresenceService (service 68). CTR polls it for its friends once the
// LSG session is established.
//
//	bdUserAccountID    : 0A u64 id | 10 platform string
//	bdRichPresenceData : bdUserAccountID | 03 u8 | 13 blob
//
// get request: 10 context | 08 u32 count | count x bdUserAccountID.

import (
	"encoding/binary"
	"sync"
)

const svcRichPresence = 68

const (
	rpSet             = 3
	rpGet             = 4
	rpGetAndSubscribe = 5
	rpUnsubscribe     = 7
)

type accountID struct {
	id       uint64
	platform string
}

type richPresence struct {
	platform string
	flag     byte
	data     []byte
}

var (
	richMu sync.Mutex
	rich   = map[uint64]richPresence{}
)

func (r *bdReader) u64() (uint64, error) {
	if err := r.tag(tagU64); err != nil {
		return 0, err
	}
	if r.off+8 > len(r.b) {
		return 0, errShort
	}
	v := binary.LittleEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v, nil
}

// accountIDs reads "u32 count | count x bdUserAccountID".
func (r *bdReader) accountIDs() []accountID {
	n, err := r.u32()
	if err != nil || n > 4096 {
		return nil
	}
	out := make([]accountID, 0, n)
	for i := uint32(0); i < n; i++ {
		id, err1 := r.u64()
		plat, err2 := r.str()
		if err1 != nil || err2 != nil {
			break
		}
		out = append(out, accountID{id, plat})
	}
	return out
}

// onlinePIDSet returns the connected players, indexed for lookup.
func onlinePIDSet() map[uint64]bool {
	retryPending()
	out := map[uint64]bool{}
	for _, pid := range onlinePIDs() {
		out[pid] = true
	}
	return out
}

func (l *lobbyConn) onRichPresence(task byte, r *bdReader) []byte {
	ctx, _ := r.str()
	switch task {
	case rpSet:
		id, err1 := r.u64()
		plat, err2 := r.str()
		flag, err3 := r.u8()
		data, err4 := r.blob()
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			l.logf("richpresence SET illisible ctx=%q", ctx)
			return taskReply(task, 0, nil)
		}
		if id == 0 {
			id = l.pid()
		}
		if id == 0 {
			return taskReply(task, 0, nil)
		}
		richMu.Lock()
		rich[id] = richPresence{plat, flag, data}
		richMu.Unlock()
		l.logf("richpresence SET pid=%d ctx=%q %d bytes", id, ctx, len(data))
		return taskReply(task, 0, nil)

	case rpGet, rpGetAndSubscribe:
		ids := r.accountIDs()
		me := l.pid()
		live := onlinePIDSet()
		type hit struct {
			id uint64
			p  richPresence
		}
		var hits []hit
		richMu.Lock()
		for _, a := range ids {
			id := a.id
			if id == 0 {
				id = me
			}
			// Presence kept for a player who left would advertise a joinable
			// game that no longer exists.
			if id != me && !live[id] {
				continue
			}
			if p, ok := rich[id]; ok {
				hits = append(hits, hit{id, p})
			}
		}
		richMu.Unlock()
		l.logf("richpresence GET ctx=%q %d demande(s) -> %d presence(s)", ctx, len(ids), len(hits))
		return taskReply(task, 0, func(w *bdWriter) uint32 {
			for _, h := range hits {
				w.u64(h.id)
				w.str(h.p.platform)
				w.u8(h.p.flag)
				w.blobv(h.p.data)
			}
			return uint32(len(hits))
		})
	}
	return taskReply(task, 0, nil)
}
