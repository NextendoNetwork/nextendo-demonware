package main

// bdStats service 4. Wire layouts recovered from CTR 1.0.15's bdStatsInfo
// and COctaneLeaderboardStatsInfo serializers, not from the dashboard API.
import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type ctrStat struct {
	Board   uint32
	PID     uint64
	Score   int64
	Driver  uint32
	Name    string
	Updated uint32
	Rank    uint64 `json:"-"`
}
type ctrStatWrite struct {
	ctrStat
	Operation byte
}
type ctrStatsStore struct {
	mu  sync.Mutex
	dir string
}

var ctrStats = &ctrStatsStore{}

func timeBoard(board uint32) bool { return board > 1000 && board < 1159 }
func statsEnd(r *bdReader) bool   { return r.off == len(r.b) || (r.off+1 == len(r.b) && r.b[r.off] == 0) }
func statsInt64(r *bdReader) (int64, error) {
	if err := r.tag(9); err != nil {
		return 0, err
	}
	if len(r.b)-r.off < 8 {
		return 0, errShort
	}
	v := int64(binary.LittleEndian.Uint64(r.b[r.off:]))
	r.off += 8
	return v, nil
}
func parseStatWrites(r *bdReader, pid uint64, name string) ([]ctrStatWrite, error) {
	out := []ctrStatWrite{}
	for !statsEnd(r) {
		if len(out) >= 128 {
			return nil, fmt.Errorf("too many writes")
		}
		board, e1 := r.u32()
		entity, e2 := r.u64()
		op, e3 := r.u8()
		score, e4 := statsInt64(r)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || entity != pid || pid == 0 {
			return nil, fmt.Errorf("invalid stats write or owner")
		}
		if op != 0 && op != 1 && op != 4 {
			return nil, fmt.Errorf("unsupported write operation %d", op)
		}
		row := ctrStatWrite{ctrStat: ctrStat{Board: board, PID: pid, Score: score, Name: name, Updated: uint32(time.Now().Unix())}, Operation: op}
		if timeBoard(board) {
			var err error
			row.Driver, err = r.u32()
			if err != nil {
				return nil, err
			}
			if op != 4 || score <= 0 || score > math.MaxUint32 {
				return nil, fmt.Errorf("invalid encoded time record")
			}
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty stats write")
	}
	return out, nil
}
func (s *ctrStatsStore) path() string {
	d := s.dir
	if d == "" {
		d = economy.dir
	}
	return filepath.Join(d, "ctr-leaderboards-v1.json")
}
func (s *ctrStatsStore) load() (map[uint32]map[uint64]ctrStat, error) {
	rows := map[uint32]map[uint64]ctrStat{}
	b, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return rows, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, fmt.Errorf("null leaderboard store")
	}
	return rows, nil
}
func (s *ctrStatsStore) write(writes []ctrStatWrite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return err
	}
	for _, w := range writes {
		board := rows[w.Board]
		if board == nil {
			board = map[uint64]ctrStat{}
			rows[w.Board] = board
		}
		old, exists := board[w.PID]
		next := w.ctrStat
		switch w.Operation {
		case 1:
			if (w.Score > 0 && old.Score > math.MaxInt64-w.Score) || (w.Score < 0 && old.Score < math.MinInt64-w.Score) {
				return fmt.Errorf("score overflow")
			}
			next.Score += old.Score
		case 4:
			// CTR encodes elapsed time into a descending uint32 score: faster is larger.
			if exists && old.Score >= w.Score {
				continue
			}
		}
		board[w.PID] = next
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path()), ".ctr-stats-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}
func (s *ctrStatsStore) read(board uint32) ([]ctrStat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	rows := []ctrStat{}
	for _, v := range all[board] {
		rows = append(rows, v)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Score == rows[j].Score {
			return rows[i].PID < rows[j].PID
		}
		return rows[i].Score > rows[j].Score
	})
	for i := range rows {
		rows[i].Rank = uint64(i + 1)
	}
	return rows, nil
}
func statsReply(task byte, rows []ctrStat, total uint32) []byte {
	w := &bdWriter{}
	w.u64(transactions.Add(1))
	w.u32(0)
	w.u8(task)
	w.u32(uint32(len(rows)))
	if len(rows) == 0 {
		return w.b
	}
	w.u32(total)
	for _, r := range rows {
		w.u64(r.PID)
		w.i64(r.Score)
		w.u64(r.Rank)
		name := r.Name
		if len(name) > 63 {
			name = name[:63]
		}
		w.strv(name)
		w.u32(r.Updated)
		if timeBoard(r.Board) {
			w.u32(r.Driver)
		}
	}
	return w.b
}
func (l *lobbyConn) onCTRStats(task byte, r *bdReader) []byte {
	pid := l.pid()
	onlineMu.Lock()
	p := online[l.n]
	name := ""
	allowed := p != nil && p.Title == 5775 && pid != 0
	if allowed {
		name = p.Username
	}
	onlineMu.Unlock()
	fail := func(err error) []byte {
		l.logf("CTR stats task=%d failed: %v", task, err)
		return taskReply(task, errUnhandled, nil)
	}
	if !allowed {
		return fail(fmt.Errorf("CTR identity required"))
	}
	if task == 1 {
		writes, err := parseStatWrites(r, pid, name)
		if err != nil {
			return fail(err)
		}
		if err = ctrStats.write(writes); err != nil {
			return fail(err)
		}
		l.logf("CTR stats saved pid=%d records=%d", pid, len(writes))
		return taskReply(task, 0, nil)
	}
	if task != 4 && task != 13 {
		return fail(fmt.Errorf("unsupported stats task"))
	}
	board, err := r.u32()
	if err != nil {
		return fail(err)
	}
	var start uint64
	var limit uint32
	ids := map[uint64]bool{}
	if task == 4 {
		start, err = r.u64()
		if err != nil {
			return fail(err)
		}
		limit, err = r.u32()
		if err != nil || limit > 1000 {
			return fail(fmt.Errorf("invalid rank limit"))
		}
		if start == 0 {
			start = 1
		}
	} else {
		n, e := r.u32()
		if e != nil || n > 1000 {
			return fail(fmt.Errorf("invalid entity count"))
		}
		for i := uint32(0); i < n; i++ {
			id, e := r.u64()
			if e != nil {
				return fail(e)
			}
			ids[id] = true
		}
	}
	cols, e := r.u32()
	if e != nil || cols != 0 || !statsEnd(r) {
		return fail(fmt.Errorf("unsupported column selection"))
	}
	all, err := ctrStats.read(board)
	if err != nil {
		return fail(err)
	}
	rows := []ctrStat{}
	for _, row := range all {
		if task == 4 {
			if row.Rank >= start && uint32(len(rows)) < limit {
				rows = append(rows, row)
			}
		} else if ids[row.PID] {
			rows = append(rows, row)
		}
	}
	total := uint32(len(all))
	if task == 13 {
		total = uint32(len(rows))
	}
	l.logf("CTR stats read task=%d board=%d rows=%d total=%d", task, board, len(rows), total)
	return statsReply(task, rows, total)
}
