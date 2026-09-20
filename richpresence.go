package main

// bdRichPresence (service 68): what a player's friends see of them.
//
// The layout is confirmed two ways: our own logs (Diablo III sends 68/3 on every
// login and game change) and the Crash Team Racing pull request on this
// repository (nx-mod/diablo-3 #1, by CollectingW), which uses the same Demonware
// SDK and documents the get/set/subscribe tasks. Written from that layout, not
// copied from it.
//
//	bdUserAccountID    : 0A u64 id | 10 platform string     (id 0 means "me")
//	bdRichPresenceData : bdUserAccountID | 03 u8 flag | 13 blob
//
//	68/3 set               : 10 ctx | bdRichPresenceData
//	68/5 getAndSubscribe   : 10 ctx | 08 u32 count | count x bdUserAccountID
//	68/4 get               : same request as 68/5
//	68/7 unsubscribe       : answered with an empty success
//
// The reply to a get is one bdRichPresenceData per friend that is online and has
// set a presence. A friend who has left is never returned, or the game would
// offer a joinable game that no longer exists.
//
// UNCONFIRMED on a live game: the reply rows (taken from the pull request), and
// that the game needs nothing pushed to it after a subscribe. Everything is
// logged as "richpresence".

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

// maxPresenceIDs bounds one request; the game asks about a friend list.
const maxPresenceIDs = 4096

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
	rich   = map[uint64]richPresence{} // by the player's Nextendo PID
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
	if err != nil || n > maxPresenceIDs {
		return nil
	}
	out := make([]accountID, 0, n)
	for i := uint32(0); i < n; i++ {
		id, err1 := r.u64()
		platform, err2 := r.str()
		if err1 != nil || err2 != nil {
			break
		}
		out = append(out, accountID{id, platform})
	}
	return out
}

// dropPresence forgets a player's presence (they left, or replaced it).
func dropPresence(pid uint64) {
	richMu.Lock()
	delete(rich, pid)
	richMu.Unlock()
}

func (l *lobbyConn) onRichPresence(task byte, r *bdReader) []byte {
	ctx, _ := r.str()
	switch task {
	case rpSet:
		id, err1 := r.u64()
		platform, err2 := r.str()
		flag, err3 := r.u8()
		data, err4 := r.blob()
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			l.logf("richpresence SET unreadable ctx=%q", ctx)
			return taskReply(task, 0, nil)
		}
		// id 0 is the player themself. A player we cannot identify (a guest with
		// no Nextendo PID) has nowhere to be stored.
		if l.player == nil || l.player.PID == 0 {
			return taskReply(task, 0, nil)
		}
		richMu.Lock()
		rich[l.player.PID] = richPresence{platform, flag, data}
		richMu.Unlock()
		l.logf("richpresence SET pid=%d id=%d ctx=%q flag=%d %d bytes", l.player.PID, id, ctx, flag, len(data))
		return taskReply(task, 0, nil)

	case rpGet, rpGetAndSubscribe:
		ids := r.accountIDs()
		type hit struct {
			id uint64
			p  richPresence
		}
		var hits []hit
		for _, a := range ids {
			var pid uint64
			if a.id == 0 && l.player != nil {
				pid = l.player.PID
			} else if p := onlinePlayerFor(a.id); p != nil {
				pid = p.PID
			}
			if pid == 0 {
				continue
			}
			richMu.Lock()
			p, ok := rich[pid]
			richMu.Unlock()
			if ok {
				hits = append(hits, hit{a.id, p}) // the id as the game asked for it
			}
		}
		l.logf("richpresence GET task=%d ctx=%q %d asked -> %d returned", task, ctx, len(ids), len(hits))
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
