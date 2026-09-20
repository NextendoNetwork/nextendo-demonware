package main

// Resolving a console friend through nextendo-account.
//
// A console's friend list holds Nintendo ids, not Nextendo PIDs. Other Nextendo
// game servers never keep such a mapping themselves: they ask nextendo-account
// (Luigi's Mansion 3 uses /internal/npln-friends and /api/names). The account
// service has the matching lookup for a BAAS user id, /internal/resolve?baas=,
// authenticated with the same X-Internal-Key as the other internal calls, and
// this uses it as a fallback when the friend was not found by PID or through the
// local baas-proxy log (friends.go), so a deployment does not need that log.
//
// NOT tested against real friend ids yet: only against a stand-in account
// service (see README, Known limits).

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	resolveHitTTL  = 5 * time.Minute  // a friend that resolved
	resolveMissTTL = 30 * time.Second // not found, or the account service failed
	resolveMaxKeep = 4096
)

type resolved struct {
	pid uint64
	ok  bool
	at  time.Time
}

var (
	resolveMu    sync.Mutex
	resolveCache = map[uint64]resolved{}
	resolveDown  time.Time // do not call the account service before this
	resolveHTTP  = &http.Client{Timeout: 3 * time.Second}
)

// pidForConsoleID returns the Nextendo PID that owns a console friend id (a BAAS
// user id), asking nextendo-account and remembering the answer.
func pidForConsoleID(id uint64) (uint64, bool) {
	now := time.Now()
	resolveMu.Lock()
	if r, hit := resolveCache[id]; hit {
		ttl := resolveMissTTL
		if r.ok {
			ttl = resolveHitTTL
		}
		if now.Sub(r.at) < ttl {
			resolveMu.Unlock()
			return r.pid, r.ok
		}
	}
	if now.Before(resolveDown) {
		resolveMu.Unlock()
		return 0, false
	}
	resolveMu.Unlock()

	pid, ok, reachable := fetchResolve(id)

	resolveMu.Lock()
	defer resolveMu.Unlock()
	if !reachable {
		// One failed call is enough: do not make every lookup wait for a timeout.
		resolveDown = now.Add(resolveMissTTL)
		return 0, false
	}
	if len(resolveCache) >= resolveMaxKeep {
		resolveCache = map[uint64]resolved{}
	}
	resolveCache[id] = resolved{pid: pid, ok: ok, at: now}
	return pid, ok
}

// fetchResolve makes the call. reachable is false when the account service did
// not answer properly (network error or a status other than 200 or 404).
func fetchResolve(id uint64) (pid uint64, found, reachable bool) {
	req, err := http.NewRequest("GET", accountBaseURL+"/internal/resolve?baas="+strconv.FormatUint(id, 16), nil)
	if err != nil {
		return 0, false, false
	}
	if internalKey != "" {
		req.Header.Set("X-Internal-Key", internalKey)
	}
	resp, err := resolveHTTP.Do(req)
	if err != nil {
		return 0, false, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return 0, false, true
	case http.StatusOK:
		var out struct {
			PID uint64 `json:"pid"`
		}
		if json.NewDecoder(resp.Body).Decode(&out) != nil || out.PID == 0 {
			return 0, false, true
		}
		return out.PID, true, true
	}
	return 0, false, false
}
