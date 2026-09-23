package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

// Extracted from Switch 1.0.15 AchievementMetadata, not an invented reward.
// Its progression period repeats March 26–June 18, 2020 (84 days).
//
//go:embed ctr-window-shopping.json
var windowShoppingJSON []byte

type windowShoppingRule struct {
	Name       string  `json:"name"`
	ID         uint64  `json:"id"`
	Kind       uint64  `json:"kind"`
	Reward     int64   `json:"reward"`
	Target     uint64  `json:"target"`
	CycleStart int64   `json:"cycle_start"`
	CycleDays  int64   `json:"cycle_days"`
	ActiveDays []int64 `json:"active_days"`
}

var windowShopping = func() windowShoppingRule {
	var r windowShoppingRule
	if err := json.Unmarshal(windowShoppingJSON, &r); err != nil {
		panic(err)
	}
	return r
}()

// Completion time and coin credit are committed in the same account file.
type challengeCompletion struct {
	Completed uint64 `json:"completed"`
}

func windowShoppingState(a *economyAccount, now time.Time) (ctrAchievementState, bool) {
	r := windowShopping
	_, start, end := pitStopDay(now)
	if int64(start) < r.CycleStart {
		return ctrAchievementState{}, false
	}
	day := ((int64(start) - r.CycleStart) / 86400) % r.CycleDays
	active := false
	for _, d := range r.ActiveDays {
		if day == d {
			active = true
			break
		}
	}
	state := ctrAchievementState{Name: r.Name, ID: r.ID, Kind: r.Kind, Target: r.Target, Status: 2, Start: start, End: end}
	key := fmt.Sprintf("%s:%d", r.Name, start)
	if c, ok := a.Challenges[key]; ok {
		state.Progress = r.Target
		state.Status = 3
		state.Completed = c.Completed
	}
	return state, active
}

func (s *economyStore) awardWindowShopping(pid uint64, events []achievementEvent, now time.Time) (int64, []byte, error) {
	var visits []achievementEvent
	for _, e := range events {
		if e.Name == windowShopping.Name && e.Values["value"] == 1 {
			visits = append(visits, e)
		}
	}
	if len(visits) == 0 {
		return 0, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.load(pid)
	if err != nil {
		return 0, nil, err
	}
	state, active := windowShoppingState(a, now)
	if !active || state.Status == 3 {
		return 0, nil, nil
	}
	var completed uint64
	for _, e := range visits {
		if e.Timestamp >= state.Start && e.Timestamp < state.End && e.Timestamp <= uint64(now.Add(5*time.Minute).Unix()) {
			completed = e.Timestamp
			break
		}
	}
	if completed == 0 {
		return 0, nil, nil
	}
	var credit int64
	if state.Status != 3 {
		if a.Challenges == nil {
			a.Challenges = map[string]challengeCompletion{}
		}
		a.Challenges[fmt.Sprintf("%s:%d", state.Name, state.Start)] = challengeCompletion{Completed: completed}
		credit = windowShopping.Reward
		a.Balances[wumpaCurrency] += credit
		if err := s.save(pid, a); err != nil {
			return 0, nil, err
		}
		state, _ = windowShoppingState(a, now)
	}
	return credit, pbBytesField(nil, 1, state.encode()), nil
}
