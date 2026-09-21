package main

import (
	"sort"
	"strconv"
)

// All calls hold the matchmaker lock. Reserve seats and ask the destination
// host to acknowledge them before sending CTR's native merge event (2008).
func mergeOpen(r *ctrRoom) bool {
	open, _ := r.hostDoc["lobby_open"].(bool)
	return open && r.connected() == r.occupancy() && r.occupancy() > 0
}

func disjointRooms(a, b *ctrRoom) bool {
	seen := map[uint64]bool{}
	for _, p := range a.players {
		for _, pid := range p.doc.Members {
			seen[pid] = true
		}
	}
	for _, p := range b.players {
		for _, pid := range p.doc.Members {
			if seen[pid] {
				return false
			}
		}
	}
	return true
}

func (m *ctrMatchmaker) startMerge(dst *ctrRoom) []ctrNotice {
	if dst.mergeTarget != 0 && m.rooms[dst.mergeTarget] == nil {
		dst.mergeTarget = 0
	}
	if dst.mergeTarget != 0 || dst.mergeSource != nil || !mergeOpen(dst) {
		return nil
	}
	candidates := []*ctrRoom{}
	for _, src := range m.rooms {
		if src == dst || src.mergeTarget != 0 || src.mergeSource != nil || !mergeOpen(src) || src.host.filter != dst.host.filter {
			continue
		}
		// A stable direction avoids reciprocal merges and moves fewer players.
		if src.occupancy() > dst.occupancy() || (src.occupancy() == dst.occupancy() && src.id < dst.id) {
			continue
		}
		if src.occupancy()+dst.occupancy() > min(src.capacity, dst.capacity) || !disjointRooms(src, dst) {
			continue
		}
		candidates = append(candidates, src)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].occupancy() != candidates[j].occupancy() {
			return candidates[i].occupancy() > candidates[j].occupancy()
		}
		return candidates[i].id < candidates[j].id
	})
	if len(candidates) == 0 {
		return nil
	}
	src := candidates[0]
	dst.mergeSource = src
	src.mergeTarget = dst.id
	if states, ok := dst.hostDoc["player_state"].(map[string]any); ok {
		for _, p := range src.players {
			for _, pid := range p.doc.Members {
				delete(states, strconv.FormatUint(pid, 10))
			}
		}
	}
	dst.version++
	dst.hostDoc["update_id"] = dst.version
	dst.host.conn.logf("CTR merge reserved source=%d destination=%d incoming=%d total=%d version=%d", src.id, dst.id, src.occupancy(), dst.occupancy(), dst.version)
	return []ctrNotice{dst.notice(dst.host, ctrUpdateLobby)}
}

func (m *ctrMatchmaker) finishMerge(dst *ctrRoom) []ctrNotice {
	src := dst.mergeSource
	if src == nil {
		return nil
	}
	open, _ := dst.hostDoc["lobby_open"].(bool)
	valid := m.rooms[src.id] == src && src.mergeTarget == dst.id && open && mergeOpen(src) && disjointRooms(dst, src)
	for _, p := range src.players {
		for _, pid := range p.doc.Members {
			if dst.state(pid) == 3 {
				valid = false
			}
		}
	}
	if !valid {
		dst.mergeSource = nil
		src.mergeTarget = 0
		dst.version++
		dst.hostDoc["update_id"] = dst.version
		dst.host.conn.logf("CTR merge cancelled source=%d destination=%d", src.id, dst.id)
		return []ctrNotice{dst.notice(dst.host, ctrUpdateLobby)}
	}
	dst.mergeSource = nil
	dst.capacity = min(dst.capacity, src.capacity)
	dst.players = append(dst.players, src.players...)
	delete(m.rooms, src.id)
	h, b := dst.documents()
	out := make([]ctrNotice, 0, len(src.players))
	for _, p := range src.players {
		// Verified msgMergeIntoLobby checks the second u64 against the current
		// lobby ID and passes the third to joinLobby; the first is the search ID.
		out = append(out, ctrNotice{p.conn, ctrMergeLobby, []uint64{p.id, src.id, dst.id}, h, b})
		dst.notified[p.conn] = true
	}
	dst.host.conn.logf("CTR merge dispatched source=%d destination=%d searches=%d total=%d", src.id, dst.id, len(src.players), dst.occupancy())
	return out
}
