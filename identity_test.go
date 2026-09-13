package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// jwt builds an unsigned id_token carrying the given claims (only the payload
// segment is read by the server).
func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

// authBody builds a Demonware auth request the way the game sends it:
// extra_data is a JSON string inside the JSON body.
func authBody(username, token string) []byte {
	extra, _ := json.Marshal(map[string]string{"username": username, "token": token})
	body, _ := json.Marshal(map[string]string{"auth_task": "78", "title_id": "5745", "extra_data": string(extra)})
	return body
}

func signNnex(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("nex:" + payload))
	return "nx2." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func expiry(d time.Duration) string { return strconv.FormatInt(time.Now().Add(d).Unix(), 10) }

func TestPlayerIdentityConsole(t *testing.T) {
	nn := signNnex([]byte("0123456789abcdef"), "1800000101.alice."+expiry(time.Hour))
	// Claims seen on a Switch: no di / sn.
	id, got := playerIdentity(authBody("alice", jwt(t, map[string]any{"sub": "0123456789abcdef", "bs:did": "x", "nnex": nn})))
	if id.PID != 1800000101 || id.Username != "alice" || id.Kind != "switch" || id.Sub != "0123456789abcdef" {
		t.Fatalf("identity = %+v", id)
	}
	if got != nn {
		t.Fatalf("nnex = %q, want %q", got, nn)
	}
}

func TestPlayerIdentityEmulator(t *testing.T) {
	nn := signNnex([]byte("0123456789abcdef"), "1800000102.bob."+expiry(time.Hour))
	// Claims seen on Citron: di and sn present.
	id, _ := playerIdentity(authBody("", jwt(t, map[string]any{"sub": "s", "di": "d", "sn": "n", "nnex": nn})))
	if id.PID != 1800000102 || id.Kind != "ryujinx" {
		t.Fatalf("identity = %+v", id)
	}
	if id.Username != "bob" {
		t.Fatalf("username from nnex = %q", id.Username)
	}
}

func TestPlayerIdentityWithoutToken(t *testing.T) {
	id, nn := playerIdentity([]byte(`{"auth_task":"78"}`))
	if id.PID != 0 || nn != "" || id.Kind != "switch" {
		t.Fatalf("identity = %+v nnex=%q", id, nn)
	}
}

func TestNextendoPIDFromToken(t *testing.T) {
	saved := nextendoSecret
	defer func() { nextendoSecret = saved }()
	nextendoSecret = []byte("0123456789abcdef")

	if pid, ok := nextendoPIDFromToken(signNnex(nextendoSecret, "1800000102.bob."+expiry(time.Hour))); !ok || pid != 1800000102 {
		t.Fatalf("valid token: pid=%d ok=%v", pid, ok)
	}
	if _, ok := nextendoPIDFromToken(signNnex([]byte("another-secret!!"), "1800000102.bob."+expiry(time.Hour))); ok {
		t.Fatal("token signed with another secret accepted")
	}
	if _, ok := nextendoPIDFromToken(signNnex(nextendoSecret, "1800000102.bob."+expiry(-time.Hour))); ok {
		t.Fatal("expired token accepted")
	}
	if _, ok := nextendoPIDFromToken(signNnex(nextendoSecret, "1800000006.Kazuu.1787343209")); ok {
		t.Fatal("revoked token accepted")
	}
}

func TestAdmitPlayer(t *testing.T) {
	savedURL, savedReq, savedSigned, savedSecret := accountBaseURL, requireAccount, requireSignedToken, nextendoSecret
	defer func() {
		accountBaseURL, requireAccount, requireSignedToken, nextendoSecret = savedURL, savedReq, savedSigned, savedSecret
	}()
	accountBaseURL = "http://127.0.0.1:1" // unreachable: online-check fails open
	nextendoSecret = nil
	requireSignedToken = false

	requireAccount = false
	if ok, why := admitPlayer(&playerID{Username: "guest"}, "", "127.0.0.1"); !ok {
		t.Fatalf("local mode refused a player without PID: %s", why)
	}
	requireAccount = true
	if ok, _ := admitPlayer(&playerID{Username: "guest"}, "", "127.0.0.1"); ok {
		t.Fatal("NEXTENDO_REQUIRE_ACCOUNT=1 accepted a player without PID")
	}
	if ok, why := admitPlayer(&playerID{Username: "alice", PID: 1800000101}, "", "127.0.0.1"); !ok {
		t.Fatalf("unreachable account server must fail open: %s", why)
	}

	requireSignedToken = true
	nextendoSecret = []byte("0123456789abcdef")
	good := signNnex(nextendoSecret, "1800000101.alice."+expiry(time.Hour))
	if ok, why := admitPlayer(&playerID{PID: 1800000101}, good, "127.0.0.1"); !ok {
		t.Fatalf("signed token refused: %s", why)
	}
	if ok, _ := admitPlayer(&playerID{PID: 1800000001}, good, "127.0.0.1"); ok {
		t.Fatal("token proving another PID accepted")
	}
}

func TestTaskName(t *testing.T) {
	cases := map[[2]byte]string{
		{21, 5}:  "bdMatchMaking::findSessions",
		{10, 21}: "bdStorage::getPublisherFile",
		{99, 7}:  "Service-99::t7",
	}
	for in, want := range cases {
		if got := taskName(in[0], in[1]); got != want {
			t.Errorf("taskName(%d,%d) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
