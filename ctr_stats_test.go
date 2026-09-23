package main

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"
)

func TestCTRStatsPersistenceBestAndWire(t *testing.T) {
	s := &ctrStatsStore{dir: t.TempDir()}
	row := ctrStatWrite{ctrStat: ctrStat{Board: 1001, PID: 12, Score: 4000000000, Driver: 123, Name: "Racer", Updated: 100}, Operation: 4}
	if err := s.write([]ctrStatWrite{row}); err != nil {
		t.Fatal(err)
	}
	row.Score--
	row.Driver = 999
	if err := s.write([]ctrStatWrite{row}); err != nil {
		t.Fatal(err)
	}
	other := row
	other.PID = 13
	other.Score = 4100000000
	if err := s.write([]ctrStatWrite{other}); err != nil {
		t.Fatal(err)
	}
	rows, err := (&ctrStatsStore{dir: s.dir}).read(1001)
	if err != nil || len(rows) != 2 || rows[0].PID != 13 || rows[1].Driver != 123 || rows[1].Rank != 2 {
		t.Fatalf("bad persistent ranks: %+v %v", rows, err)
	}
	reply := statsReply(4, rows[1:], 2)
	r := &bdReader{b: reply}
	r.u64()
	r.u32()
	r.u8()
	n, _ := r.u32()
	total, _ := r.u32()
	pid, _ := r.u64()
	score, _ := statsInt64(r)
	rank, _ := r.u64()
	name, _ := r.str()
	stamp, _ := r.u32()
	driver, e := r.u32()
	if n != 1 || total != 2 || pid != 12 || score != 4000000000 || rank != 2 || name != "Racer" || stamp != 100 || driver != 123 || e != nil || r.off != len(reply) {
		t.Fatalf("bad reply %x", reply)
	}
	before, _ := os.ReadFile(s.path())
	row.Score = 4200000000
	if err := s.write([]ctrStatWrite{row}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.path())
	if bytes.Equal(before, after) {
		t.Fatal("faster score not saved")
	}
	if err := os.WriteFile(s.path(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.write([]ctrStatWrite{row}); err == nil {
		t.Fatal("corrupt data overwritten")
	}
}
func TestCTRStatsCapturedInitializationAndOwnership(t *testing.T) {
	// Actual 4/1 initialization from the user's Highscores attempt, including task terminator.
	packet, _ := hex.DecodeString("04030108e80300000a4edf496b00000000030009000000000000000000")
	writes, err := parseStatWrites(&bdReader{b: packet, off: 3}, 1800003406, "Collecting")
	if err != nil || len(writes) != 1 || writes[0].Board != 1000 {
		t.Fatalf("capture failed: %+v %v", writes, err)
	}
	if _, err = parseStatWrites(&bdReader{b: packet, off: 3}, 7, "forged"); err == nil {
		t.Fatal("accepted other account")
	}
	w := &bdWriter{}
	w.u32(1001)
	w.u64(12)
	w.u8(4)
	w.i64(4000000000)
	w.u32(123)
	if rows, err := parseStatWrites(&bdReader{b: w.b}, 12, "test"); err != nil || rows[0].Driver != 123 {
		t.Fatal(rows, err)
	}
	if _, err := parseStatWrites(&bdReader{b: w.b[:len(w.b)-1]}, 12, "test"); err == nil {
		t.Fatal("accepted truncated driver")
	}
}
func TestCTRStatsRankAndFriendsRequests(t *testing.T) {
	oldOnline, oldStore := online, ctrStats
	defer func() { online = oldOnline; ctrStats = oldStore }()
	online = map[uint64]*playerID{9: {PID: 12, Username: "test", Title: 5775}}
	ctrStats = &ctrStatsStore{dir: t.TempDir()}
	l := &lobbyConn{n: 9, player: online[9]}
	if err := ctrStats.write([]ctrStatWrite{{ctrStat: ctrStat{Board: 1001, PID: 12, Score: 400, Name: "A"}, Operation: 4}, {ctrStat: ctrStat{Board: 1001, PID: 13, Score: 500, Name: "B"}, Operation: 4}}); err != nil {
		t.Fatal(err)
	}
	for _, task := range []byte{4, 13} {
		w := &bdWriter{}
		w.u32(1001)
		if task == 4 {
			w.u64(2)
			w.u32(1)
		} else {
			w.u32(1)
			w.u64(12)
		}
		w.u32(0)
		w.b = append(w.b, 0)
		r := &bdReader{b: l.onCTRStats(task, &bdReader{b: w.b})}
		r.u64()
		code, _ := r.u32()
		r.u8()
		n, _ := r.u32()
		r.u32()
		pid, _ := r.u64()
		if code != 0 || n != 1 || pid != 12 {
			t.Fatalf("task %d wrong page/filter", task)
		}
	}
}
