package main

import "time"

// Achievements use struct task replies; ordinary result-count replies fail decoding.
const svcAchievements = 125

// Push 75 carries named achievement states, not a generic acknowledgement.
const pushAchievementsUpdated = 75

const (
	taskReportEvents         = 1
	taskReportUserEvents     = 2
	taskGetAchievementStates = 3
	taskGetUserState         = 9
)

// onAchievements answers every achievements-engine task with a well formed struct.
func (l *lobbyConn) onAchievements(task byte, payload []byte) []byte {
	switch task {
	case taskReportEvents, taskReportUserEvents:
		var credit int64
		var updates []byte
		if task == taskReportEvents {
			events, err := parseRewardEvents(payload)
			if err != nil {
				l.logf("CTR reward event rejected: %v", err)
				return taskReply(task, errUnhandled, nil)
			}
			for _, e := range events {
				l.logf("CTR reward event=%s timestamp=%d values=%v", e.Name, e.Timestamp, e.Values)
			}
			credit, err = economy.awardRaces(l.pid(), events, time.Now())
			if err != nil {
				l.logf("CTR reward persistence failed: %v", err)
				return taskReply(task, errUnhandled, nil)
			}
			updates, err = economy.reportPitStopRefresh(l.pid(), events, time.Now())
			if err != nil {
				l.logf("CTR refresh persistence failed: %v", err)
				return taskReply(task, errUnhandled, nil)
			}
			challengeCredit, challengeUpdates, err := economy.awardWindowShopping(l.pid(), events, time.Now())
			if err != nil {
				l.logf("CTR challenge persistence failed: %v", err)
				return taskReply(task, errUnhandled, nil)
			}
			credit += challengeCredit
			updates = append(updates, challengeUpdates...)
			if challengeCredit != 0 {
				l.logf("CTR Window Shopping reward=%d persisted", challengeCredit)
			}

			if credit != 0 {
				l.logf("CTR reward credit=%d currency=10 persisted", credit)
			}
		}
		l.afterReply = func() {
			if credit != 0 {
				l.pushCurrentBalance()
			}
			if len(updates) == 0 {
				return
			}
			if err := l.sendPush(pushAchievementsUpdated, func(w *bdWriter) { w.structv(updates) }); err != nil {
				l.logf("CTR achievements push: %v", err)
				return
			}
			l.logf("CTR achievements -> push %d achievements-updated", pushAchievementsUpdated)
		}
		return structTaskReply(task, nil)

	case taskGetAchievementStates:
		a, err := economy.snapshot(l.pid())
		if err != nil {
			return taskReply(task, errUnhandled, nil)
		}
		state := currentPitStopRefresh(a, time.Now())
		var states []byte
		if achievementRequested(payload, state) {
			states = pbBytesField(nil, 1, state.encode())
		}
		if challenge, active := windowShoppingState(a, time.Now()); active && achievementRequested(payload, challenge) {
			states = pbBytesField(states, 1, challenge.encode())
		}
		return structTaskReply(task, pbBytesField(states, 2, nil))

	case taskGetUserState:
		// field 1 is the engine's user-state blob. Empty JSON keeps it valid and lets
		// handleGetUserState fire CAEUserStateRefreshed, which is the refresh confirmation.
		l.logf("CTR achievements getUserState -> empty state")
		return structTaskReply(task, pbBytesField(nil, 1, []byte("{}")))
	}
	// reportEvents and the activate/claim tasks take a request and return no body.
	return structTaskReply(task, nil)
}

// eventNames pulls the printable strings out of a reportEvents struct buffer so the race
// reward event can be identified without dumping the whole payload.
func eventNames(payload []byte) []string {
	sb := structPayload(payload)
	out := []string{}
	for i := 0; i < len(sb) && len(out) < 12; {
		wire := sb[i] & 7
		i++
		switch wire {
		case 0:
			_, next, err := readVarint(sb, i)
			if err != nil {
				return out
			}
			i = next
		case 2:
			ln, next, err := readVarint(sb, i)
			if err != nil || next+int(ln) > len(sb) {
				return out
			}
			i = next
			if s := string(sb[i : i+int(ln)]); printableName(s) {
				out = append(out, s)
			}
			i += int(ln)
		default:
			return out
		}
	}
	return out
}

func printableName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}
