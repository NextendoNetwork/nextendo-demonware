package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A stand-in for nextendo-account's /internal/resolve.
func fakeAccount(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/internal/resolve" || r.Header.Get("X-Internal-Key") != "k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("baas") {
		case "b6181eb6d6b908fe":
			w.Write([]byte(`{"pid": 1800000102, "nickname": "player2"}`))
		case "boom":
			http.Error(w, "x", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
}

func withAccount(t *testing.T, url string) {
	t.Helper()
	oldURL, oldKey := accountBaseURL, internalKey
	accountBaseURL, internalKey = url, "k"
	resolveMu.Lock()
	resolveCache, resolveDown = map[uint64]resolved{}, time.Time{}
	resolveMu.Unlock()
	t.Cleanup(func() { accountBaseURL, internalKey = oldURL, oldKey })
}

func TestPIDForConsoleID(t *testing.T) {
	var hits atomic.Int32
	srv := fakeAccount(t, &hits)
	defer srv.Close()
	withAccount(t, srv.URL)

	pid, ok := pidForConsoleID(0xb6181eb6d6b908fe)
	if !ok || pid != 1800000102 {
		t.Fatalf("resolved %d %v", pid, ok)
	}
	pidForConsoleID(0xb6181eb6d6b908fe) // answered from the cache
	if hits.Load() != 1 {
		t.Errorf("account called %d times for one id, want 1", hits.Load())
	}
	if _, ok := pidForConsoleID(0x1234); ok {
		t.Error("an unknown id resolved")
	}
	pidForConsoleID(0x1234)
	if hits.Load() != 2 {
		t.Errorf("a miss was not cached: %d calls", hits.Load())
	}
}

func TestPIDForConsoleIDWhenAccountIsDown(t *testing.T) {
	var hits atomic.Int32
	srv := fakeAccount(t, &hits)
	withAccount(t, srv.URL)
	srv.Close() // unreachable from here on
	start := time.Now()
	for i := 0; i < 20; i++ {
		if _, ok := pidForConsoleID(uint64(i + 100)); ok {
			t.Fatal("resolved with the account service down")
		}
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("20 lookups took %v: each should not wait for its own timeout", time.Since(start))
	}
}

func TestNicknameOnlineUsesTheAccountService(t *testing.T) {
	var hits atomic.Int32
	srv := fakeAccount(t, &hits)
	defer srv.Close()
	withAccount(t, srv.URL)

	onlineMu.Lock()
	old := online
	online = map[uint64]*playerID{7: {Username: "player2", PID: 1800000102}}
	onlineMu.Unlock()
	t.Cleanup(func() { onlineMu.Lock(); online = old; onlineMu.Unlock() })

	// the console's friend id is not the PID, and no baas-proxy log is present
	if got := nicknameOnline(0xb6181eb6d6b908fe); got != "player2" {
		t.Errorf("console friend resolved to %q, want player2", got)
	}
	// a friend the account knows but who is not online is not reported
	if got := nicknameOnline(0x99); got != "" {
		t.Errorf("unknown friend resolved to %q", got)
	}
	// a PID still matches directly, without asking the account service
	before := hits.Load()
	if got := nicknameOnline(1800000102); got != "player2" {
		t.Errorf("PID lookup gave %q", got)
	}
	if hits.Load() != before {
		t.Error("a PID lookup called the account service")
	}
}
