package main

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func TestInventoryCompleteClientWireLayout(t *testing.T) {
	w := &bdWriter{}
	writeInventoryItem(w, 42, 688)
	// Independent bdMarketplaceInventory::deserialize wire fixture. In particular,
	// signed int64 uses 09; 14 is the MAX_INT64 sentinel with no trailing bytes.
	want, err := hex.DecodeString("0a2a00000000000000106e696e74656e646f0008b00200000801000000080000000013080000000008000000000900000000000000000600000800000000")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.b, want) {
		t.Fatalf("inventory wire\ngot %x\nwant %x", w.b, want)
	}
}

func TestPitStopRefreshRetryRestartLimitAndReset(t *testing.T) {
	s := &economyStore{dir: t.TempDir()}
	now := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	e := achievementEvent{Name: "pitstop_daily_refresh", Timestamp: uint64(now.Unix()), Values: map[string]uint64{"value": 1}}
	b, err := s.reportPitStopRefresh(42, []achievementEvent{e, e}, now)
	if err != nil || len(b) == 0 {
		t.Fatalf("%x %v", b, err)
	}
	s = &economyStore{dir: s.dir}
	s.reportPitStopRefresh(42, []achievementEvent{e}, now)
	a, _ := s.snapshot(42)
	if got := currentPitStopRefresh(a, now); got.Progress != 1 || got.Status != 2 {
		t.Fatalf("retry %+v", got)
	}
	e.Timestamp++
	s.reportPitStopRefresh(42, []achievementEvent{e}, now.Add(time.Second))
	e.Timestamp++
	s.reportPitStopRefresh(42, []achievementEvent{e}, now.Add(2*time.Second))
	a, _ = s.snapshot(42)
	if got := currentPitStopRefresh(a, now); got.Progress != 2 || got.Status != 3 {
		t.Fatalf("limit %+v", got)
	}
	reset := time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC)
	if got := currentPitStopRefresh(a, reset); got.Progress != 0 {
		t.Fatalf("reset %+v", got)
	}
	b, err = s.reportPitStopRefresh(42, []achievementEvent{e}, reset)
	if err != nil || len(b) != 0 {
		t.Fatalf("stale refresh accepted: %x %v", b, err)
	}
}

func TestRefreshNotificationCanBeDecodedAndNamesRealCounter(t *testing.T) {
	s := currentPitStopRefresh(&economyAccount{}, time.Now())
	f, err := economyProto(s.encode())
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint64]economyProtoField{}
	for _, v := range f {
		got[v.number] = v
	}
	if string(got[1].data) != "pitstop_daily_refresh" || got[2].value != 1 {
		t.Fatal("wrong refresh callback")
	}
	for _, tag := range []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 11} {
		if _, ok := got[tag]; !ok {
			t.Fatalf("missing required field %d", tag)
		}
	}
	if got[5].value < 1 || got[5].value > 5 {
		t.Fatal("client rejects invalid achievement status")
	}
	w := &bdWriter{b: []byte{125, 3, 3}}
	w.structv(pbVarintField(nil, 2, 3))
	if achievementRequested(w.b, s) {
		t.Fatal("refresh included in daily challenge query")
	}
	w = &bdWriter{b: []byte{125, 3, 3}}
	w.structv(pbVarintField(nil, 2, 1))
	if !achievementRequested(w.b, s) {
		t.Fatal("refresh missing from progression query")
	}
}
