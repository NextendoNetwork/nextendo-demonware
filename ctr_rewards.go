package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

// Extracted from the Switch 1.0.15 CSystemData::gpMapData table, not a
// replacement flat rate. Keys use the client's seed-4 FNV track-name hash.
//
//go:embed ctr-race-rates.json
var raceRatesJSON []byte

type raceRate struct {
	Track     string   `json:"track"`
	Positions [4]int64 `json:"positions"`
}

var raceRates = func() map[uint64]raceRate {
	m := map[uint64]raceRate{}
	if err := json.Unmarshal(raceRatesJSON, &m); err != nil {
		panic(err)
	}
	return m
}()

type achievementEvent struct {
	Name      string
	Timestamp uint64
	ID        uint64
	Values    map[string]uint64
}

func balanceUpdatePayload(pid uint64, amount int64) []byte {
	owner := pbVarintField(nil, 1, pid)
	owner = pbBytesField(owner, 2, []byte("nintendo"))
	entry := pbBytesField(nil, 1, owner)
	entry = pbVarintField(entry, 2, wumpaCurrency)
	entry = pbVarintField(entry, 3, 0xffffffff) // legacy 32-bit balance sentinel
	entry = pbVarintField(entry, 4, 0)
	entry = pbVarintField(entry, 5, zigzag(amount))
	return pbBytesField(nil, 1, entry)
}
func (l *lobbyConn) pushCurrentBalance() {
	a, err := economy.snapshot(l.pid())
	if err != nil {
		l.logf("CTR balance push read: %v", err)
		return
	}
	if err := l.sendPush(76, func(w *bdWriter) { w.structv(balanceUpdatePayload(l.pid(), a.Balances[10])) }); err != nil {
		l.logf("CTR balance push: %v", err)
	}
}

type economyProtoField struct {
	number uint64
	wire   uint64
	value  uint64
	data   []byte
}

func economyProto(b []byte) ([]economyProtoField, error) {
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("event too large")
	}
	var out []economyProtoField
	for i := 0; i < len(b); {
		tag, next, err := readVarint(b, i)
		if err != nil || tag>>3 == 0 {
			return nil, fmt.Errorf("bad protobuf tag")
		}
		i = next
		f := economyProtoField{number: tag >> 3, wire: tag & 7}
		switch f.wire {
		case 0:
			f.value, i, err = readVarint(b, i)
			if err != nil {
				return nil, err
			}
		case 2:
			n, nxt, e := readVarint(b, i)
			if e != nil || n > uint64(len(b)-nxt) {
				return nil, fmt.Errorf("truncated protobuf")
			}
			i = nxt
			f.data = b[i : i+int(n)]
			i += int(n)
		default:
			return nil, fmt.Errorf("unsupported event wire type")
		}
		out = append(out, f)
		if len(out) > 4096 {
			return nil, fmt.Errorf("too many fields")
		}
	}
	return out, nil
}
func parseRewardEvents(payload []byte) ([]achievementEvent, error) {
	sb := structPayload(payload)
	if sb == nil {
		return nil, fmt.Errorf("missing or truncated event struct")
	}
	fields, err := economyProto(sb)
	if err != nil {
		return nil, err
	}
	var out []achievementEvent
	for _, f := range fields {
		if f.number != 2 || f.wire != 2 {
			continue
		}
		parts, err := economyProto(f.data)
		if err != nil {
			return nil, err
		}
		e := achievementEvent{Values: map[string]uint64{}}
		for _, p := range parts {
			switch p.number {
			case 1:
				e.Name = string(p.data)
			case 2:
				e.Timestamp = p.value
			case 4:
				e.ID = p.value
			case 3:
				kv, err := economyProto(p.data)
				if err != nil {
					return nil, err
				}
				var key string
				var value uint64
				for _, k := range kv {
					if k.number == 1 {
						key = string(k.data)
					}
					if k.number == 2 {
						value = k.value
					}
				}
				if key == "" || len(key) > 100 {
					return nil, fmt.Errorf("invalid event key")
				}
				if _, ok := e.Values[key]; ok {
					return nil, fmt.Errorf("duplicate event key")
				}
				e.Values[key] = value
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *economyStore) awardRaces(pid uint64, events []achievementEvent, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.load(pid)
	if err != nil {
		return 0, err
	}
	if a.RaceReceipts == nil {
		a.RaceReceipts = map[string]bool{}
	}
	if a.OnlineSeconds == nil {
		a.OnlineSeconds = map[string]uint64{}
	}
	var total int64
	for _, e := range events {
		if e.Name != "end_race" {
			continue
		}
		rate, ok := raceRates[e.Values["Track"]]
		if !ok {
			continue
		}
		rank, seconds, laps := e.Values["Rank"], e.Values["Time"], e.Values["CompletedLap"]
		if rank < 1 || rank > 8 || seconds == 0 || seconds > 86400 || laps == 0 || laps > 100 || e.Timestamp == 0 {
			continue
		}
		tm := time.Unix(int64(e.Timestamp), 0).UTC()
		if tm.After(now.Add(5*time.Minute)) || tm.Before(now.Add(-7*24*time.Hour)) {
			continue
		}
		// Normalize key ordering before hashing; retries and re-ordered event fields
		// must not grant the same race twice, including across server restarts.
		b, _ := json.Marshal(e)
		receipt := fmt.Sprintf("%x", sha256.Sum256(b))
		if a.RaceReceipts[receipt] {
			continue
		}
		pos := rank - 1
		if pos > 3 {
			pos = 3
		}
		amount := rate.Positions[pos]
		if e.Values["RaceMode"] == 15 {
			n := laps
			if n > 8 {
				n = 8
			}
			amount = amount * int64(n) / 3
		}
		if e.Values["Online"] != 0 {
			day := tm.Format("2006-01-02")
			if a.OnlineSeconds[day] < 1800 {
				amount *= 5
			}
			a.OnlineSeconds[day] += seconds
		}
		// CNetworkConfig defaults: Friday 17:00 UTC through Monday 08:00 UTC.
		// The client applies the weekend multiplier to local races as well.
		startDay := tm.Add(-17 * time.Hour).Weekday()
		endDay := tm.Add(-8 * time.Hour).Weekday()
		if startDay == time.Friday || startDay == time.Saturday || endDay == time.Sunday {
			amount *= 2
		}
		// Golden Wumpa is a fixed 200 added after race multipliers.
		if e.Values["RewardGoldenWumpa"] == 1 {
			amount += 200
		}
		a.RaceReceipts[receipt] = true
		a.Balances[wumpaCurrency] += amount
		total += amount
	}
	if total != 0 {
		if err := s.save(pid, a); err != nil {
			return 0, err
		}
	}
	return total, nil
}
