package main

import (
	"sync"
	"testing"
	"time"
)

func TestWindowShoppingOriginalScheduleAndReward(t *testing.T) {
	now := time.Date(2026, 9, 23, 3, 29, 0, 0, time.UTC)
	state, active := windowShoppingState(&economyAccount{}, now)
	if !active || state.Target != 1 || state.ID != 11 || windowShopping.Reward != 25 {
		t.Fatalf("wrong original rule: %+v %v", state, active)
	}
	s := &economyStore{dir: t.TempDir()}
	t.Setenv("CTR_START_BALANCE", "10000")
	e := achievementEvent{Name: "visit_pit_stop", Timestamp: uint64(now.Unix()), Values: map[string]uint64{"value": 1}}
	credit, update, err := s.awardWindowShopping(42, []achievementEvent{e, e}, now)
	if err != nil || credit != 25 || len(update) == 0 {
		t.Fatalf("award %d %x %v", credit, update, err)
	}
	s = &economyStore{dir: s.dir}
	credit, update, err = s.awardWindowShopping(42, []achievementEvent{e}, now)
	if err != nil || credit != 0 || len(update) != 0 {
		t.Fatalf("retry %d %v", credit, err)
	}
	a, _ := s.snapshot(42)
	state, _ = windowShoppingState(a, now)
	if a.Balances[10] != 10025 || state.Progress != 1 || state.Status != 3 || state.Completed != e.Timestamp {
		t.Fatalf("not persisted %+v %+v", a, state)
	}
	next := now.Add(24 * time.Hour)
	e.Timestamp = uint64(next.Unix())
	credit, _, err = s.awardWindowShopping(42, []achievementEvent{e}, next)
	if err != nil || credit != 25 {
		t.Fatalf("next active day %d %v", credit, err)
	}
}

func TestWindowShoppingInactiveAndStaleEvents(t *testing.T) {
	s := &economyStore{dir: t.TempDir()}
	// Original cycle day 2 has no Window Shopping challenge.
	now := time.Unix(windowShopping.CycleStart, 0).Add(2 * 24 * time.Hour)
	e := achievementEvent{Name: "visit_pit_stop", Timestamp: uint64(now.Unix()), Values: map[string]uint64{"value": 1}}
	credit, p, err := s.awardWindowShopping(42, []achievementEvent{e}, now)
	if err != nil || credit != 0 || len(p) != 0 {
		t.Fatalf("inactive award %d %x %v", credit, p, err)
	}
	now = time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	credit, p, err = s.awardWindowShopping(42, []achievementEvent{e}, now)
	if err != nil || credit != 0 || len(p) != 0 {
		t.Fatal("stale event rewarded")
	}
}

func TestWindowShoppingConcurrentVisitsAwardOnce(t *testing.T) {
	t.Setenv("CTR_START_BALANCE", "10000")
	s := &economyStore{dir: t.TempDir()}
	now := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	e := achievementEvent{Name: "visit_pit_stop", Timestamp: uint64(now.Unix()), Values: map[string]uint64{"value": 1}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.awardWindowShopping(42, []achievementEvent{e}, now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	a, _ := s.snapshot(42)
	if a.Balances[10] != 10025 {
		t.Fatalf("duplicated award: %d", a.Balances[10])
	}
}
