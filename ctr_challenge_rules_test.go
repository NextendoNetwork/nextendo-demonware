package main

import (
	"testing"
	"time"
)

func ruleForTest(t *testing.T, name string) challengeRule {
	t.Helper()
	for _, r := range challengeRules.Rules {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("missing rule %s", name)
	return challengeRule{}
}
func ruleTime(r challengeRule) time.Time {
	return time.Unix(challengeRules.CycleStart+30*challengeRules.CycleSeconds+r.Periods[0].Start+60, 0)
}
func ruleEvent(r challengeRule, now time.Time) achievementEvent {
	p := r.Periods[0]
	e := achievementEvent{Name: r.Name, Timestamp: uint64(now.Unix()), Values: map[string]uint64{"Progress": 1, "X": uint64(p.X), "Y": uint64(p.Y)}}
	for k, vs := range p.Filters {
		e.Values[k] = challengeNameHash(vs[0])
	}
	return e
}
func TestOriginalChallengeCatalog(t *testing.T) {
	if len(challengeRules.Rules) != 59 {
		t.Fatal(len(challengeRules.Rules))
	}
	for _, r := range challengeRules.Rules {
		expected := map[uint64]int64{3: 25, 4: 75, 5: 200}[r.Kind]
		if r.Reward != expected || expected == 0 || len(r.Periods) == 0 {
			t.Fatalf("invalid %s", r.Name)
		}
		for _, p := range r.Periods {
			if p.Target == 0 || p.Start < 0 || p.End <= p.Start || p.End > challengeRules.CycleSeconds {
				t.Fatalf("bad period %s %+v", r.Name, p)
			}
		}
	}
}
func TestDriftThresholdRewardAndRestart(t *testing.T) {
	t.Setenv("CTR_START_BALANCE", "10000")
	s := &economyStore{dir: t.TempDir()}
	r := ruleForTest(t, "drift_for_X_seconds_consecutively")
	now := ruleTime(r)
	e := ruleEvent(r, now)
	e.Values["X"] = 2
	if c, u, err := s.awardChallenges(42, []achievementEvent{e}, now); err != nil || c != 0 || len(u) != 0 {
		t.Fatalf("short drift accepted %d %v", c, err)
	}
	e.Values["X"] = 3
	if c, u, err := s.awardChallenges(42, []achievementEvent{e}, now); err != nil || c != 25 || len(u) == 0 {
		t.Fatalf("drift not rewarded %d %v", c, err)
	}
	s = &economyStore{dir: s.dir}
	if c, u, err := s.awardChallenges(42, []achievementEvent{e}, now); err != nil || c != 0 || len(u) != 0 {
		t.Fatalf("duplicate completion %d %v", c, err)
	}
	a, _ := s.snapshot(42)
	state, _, _ := r.current(a, now)
	if a.Balances[10] != 10025 || state.Status != 3 || state.Progress != 1 {
		t.Fatalf("not persisted %+v", state)
	}
}
func TestWeeklyProgressCountsOccurrencesAndDeduplicatesReplay(t *testing.T) {
	s := &economyStore{dir: t.TempDir()}
	r := ruleForTest(t, "use_juiced_up_powerups_T_times")
	now := ruleTime(r)
	e := ruleEvent(r, now)
	batch := []achievementEvent{e, e, e, e}
	c, u, err := s.awardChallenges(42, batch, now)
	if err != nil || c != 0 || len(u) == 0 {
		t.Fatalf("partial progress %d %v", c, err)
	}
	s = &economyStore{dir: s.dir}
	c, u, err = s.awardChallenges(42, batch, now)
	if err != nil || c != 0 || len(u) != 0 {
		t.Fatal("replay advanced")
	}
	a, _ := s.snapshot(42)
	st, _, _ := r.current(a, now)
	if st.Progress != 4 || st.Status != 2 {
		t.Fatalf("partial state %+v", st)
	}
	e.Timestamp++
	e.Values["Progress"] = 6
	c, u, err = s.awardChallenges(42, []achievementEvent{e}, now.Add(time.Second))
	if err != nil || c != 75 || len(u) == 0 {
		t.Fatalf("weekly completion %d %v", c, err)
	}
	// The next weekly occurrence must not inherit completed progress.
	next := time.Unix(challengeRules.CycleStart+30*challengeRules.CycleSeconds+r.Periods[1].Start+60, 0)
	a, _ = s.snapshot(42)
	st, _, active := r.current(a, next)
	if !active || st.Progress != 0 || st.Completed != 0 {
		t.Fatalf("period leakage %+v", st)
	}
}
func TestChallengeFiltersAndProTimeDirection(t *testing.T) {
	r := ruleForTest(t, "do_a_lap_under_X_in_TRACKNAME")
	p := r.Periods[0]
	e := ruleEvent(r, ruleTime(r))
	if r.accepts(p, e) {
		t.Fatal("equal threshold accepted for under")
	}
	e.Values["X"]--
	if !r.accepts(p, e) {
		t.Fatal("qualifying lap rejected")
	}
	e.Values["Track"]++
	if r.accepts(p, e) {
		t.Fatal("wrong track accepted")
	}
	r = ruleForTest(t, "win_mirror_mode_CHARACTER")
	p = r.Periods[0]
	e = ruleEvent(r, ruleTime(r))
	if !r.accepts(p, e) {
		t.Fatal("original character rejected")
	}
	delete(e.Values, "Character")
	if r.accepts(p, e) {
		t.Fatal("missing character accepted")
	}
}
func TestBattleChallengeRequiresDistinctModes(t *testing.T) {
	s := &economyStore{dir: t.TempDir()}
	r := ruleForTest(t, "win_game_in_T_different_battles_TRACKNAME")
	now := ruleTime(r)
	e := ruleEvent(r, now)
	for i, mode := range []uint64{9, 9, 10, 11} {
		e.Timestamp++
		e.Values["X"] = mode
		c, _, err := s.awardChallenges(42, []achievementEvent{e}, now.Add(10*time.Second))
		if err != nil || (i < 3 && c != 0) || (i == 3 && c != 75) {
			t.Fatalf("mode %d credit %d: %v", mode, c, err)
		}
	}
}
func TestChallengePeriodAndExistingCompletion(t *testing.T) {
	s := &economyStore{dir: t.TempDir()}
	r := ruleForTest(t, "visit_pit_stop")
	now := ruleTime(r)
	e := ruleEvent(r, now)
	e.Values["value"] = 1
	if c, _, err := s.awardWindowShopping(42, []achievementEvent{e}, now); err != nil || c != 25 {
		t.Fatal(c, err)
	}
	if c, u, err := s.awardChallenges(42, []achievementEvent{e}, now); err != nil || c != 0 || len(u) != 0 {
		t.Fatal("legacy completion reawarded")
	}
	e.Timestamp = uint64(now.Unix() - 86400)
	if c, u, err := s.awardChallenges(43, []achievementEvent{e}, now); err != nil || c != 0 || len(u) != 0 {
		t.Fatal("stale event accepted")
	}
}
