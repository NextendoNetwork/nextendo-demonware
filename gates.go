package main

// Nextendo gates at login — the same contract as the NEX game servers
// (luigis-mansion-3 gates.go and resolveUser):
//
//	identity      the Nextendo PID from the id_token's nnex claim (identity.go),
//	              "nx2.<b64 PID.nickname.expiry>.<HMAC-SHA256>" signed with the
//	              Nextendo secret. With NEXTENDO_SECRET(_FILE) set the signature is
//	              checked; NEXTENDO_REQUIRE_SIGNED_TOKEN=1 refuses an unproven PID.
//	online-check  POST /internal/online-check {pid, kind, ip} on nextendo-account:
//	              verified e-mail, bans, one place online. Fail-open when the
//	              account server cannot be reached, like the other servers.
//	account       NEXTENDO_REQUIRE_ACCOUNT=1 refuses a login without a Nextendo PID
//	              and enforces online-check refusals. With 0 both are only logged:
//	              for a local stack whose nextendo-account does not hold the
//	              players' real Nextendo accounts.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	accountBaseURL = envOr("NEXTENDO_ACCOUNT_URL", "http://127.0.0.1:8080")
	internalKey    = os.Getenv("NEXTENDO_INTERNAL_KEY")
	gateClient     = &http.Client{Timeout: 3 * time.Second}

	nextendoSecret     []byte
	requireAccount     bool
	requireSignedToken bool
)

func loadGates() {
	nextendoSecret = loadNextendoSecret()
	requireAccount = os.Getenv("NEXTENDO_REQUIRE_ACCOUNT") == "1"
	v := os.Getenv("NEXTENDO_REQUIRE_SIGNED_TOKEN")
	requireSignedToken = v == "1" || v == "true"
	log.Printf("[D3 Gates] account=%s secret=%v require_account=%v require_signed_token=%v",
		accountBaseURL, len(nextendoSecret) > 0, requireAccount, requireSignedToken)
}

// admitPlayer decides whether a login gets a ticket; false comes with a reason.
func admitPlayer(id *playerID, nnex, ip string) (bool, string) {
	proven := false
	if nnex != "" && len(nextendoSecret) > 0 {
		pid, ok := nextendoPIDFromToken(nnex)
		proven = ok && pid == id.PID
		if !proven {
			log.Printf("[D3 Gates] pid=%d %q: nx2 not proven by this secret (valid=%v proves=%d)", id.PID, id.Username, ok, pid)
		}
	}
	if requireSignedToken && !proven {
		return false, "identity not proven (signed nx2 token required)"
	}
	if id.PID == 0 {
		if requireAccount {
			return false, "no Nextendo account in the login token"
		}
		log.Printf("[D3 Gates] %q has no Nextendo PID: allowed (NEXTENDO_REQUIRE_ACCOUNT=0)", id.Username)
		return true, ""
	}
	if allow, reason := nextendoOnlineCheck(id.PID, id.Kind, ip); !allow {
		if requireAccount {
			return false, "online-check: " + reason
		}
		log.Printf("[D3 Gates] pid=%d online-check refused (%s): allowed (NEXTENDO_REQUIRE_ACCOUNT=0)", id.PID, reason)
	}
	return true, ""
}

func nextendoOnlineCheck(pid uint64, kind, ip string) (bool, string) {
	body, _ := json.Marshal(map[string]any{"pid": pid, "kind": kind, "ip": ip})
	req, err := http.NewRequest("POST", accountBaseURL+"/internal/online-check", bytes.NewReader(body))
	if err != nil {
		return true, ""
	}
	req.Header.Set("Content-Type", "application/json")
	if internalKey != "" {
		req.Header.Set("X-Internal-Key", internalKey)
	}
	resp, err := gateClient.Do(req)
	if err != nil {
		return true, "" // fail-open
	}
	defer resp.Body.Close()
	var out struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return true, ""
	}
	return out.Allow, out.Reason
}

// revokedNexPayloads lists leaked nex_token payloads ("pid.username.expiry") that must be
// rejected even though their HMAC is valid. Keep in sync with nextendo-account and the
// sibling servers.
var revokedNexPayloads = map[string]bool{
	"1800000006.Kazuu.1787343209": true, // leaked in the 1.6.5-win release (Kazuu / PID 1800000006)
}

func nextendoPIDFromToken(s string) (uint64, bool) {
	if len(nextendoSecret) == 0 || !strings.HasPrefix(s, "nx2.") {
		return 0, false
	}
	parts := strings.Split(s[len("nx2."):], ".")
	if len(parts) != 2 {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, false
	}
	mac := hmac.New(sha256.New, nextendoSecret)
	mac.Write([]byte("nex:" + string(raw)))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return 0, false
	}
	if revokedNexPayloads[string(raw)] {
		return 0, false
	}
	f := strings.SplitN(string(raw), ".", 3)
	if len(f) != 3 {
		return 0, false
	}
	pid, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0, false
	}
	if exp, err := strconv.ParseInt(f[2], 10, 64); err != nil || time.Now().Unix() > exp {
		return 0, false
	}
	return pid, true
}

func loadNextendoSecret() []byte {
	if v := os.Getenv("NEXTENDO_SECRET"); v != "" {
		return []byte(v)
	}
	path := envOr("NEXTENDO_SECRET_FILE", "nextendo_secret.key")
	if b, err := os.ReadFile(path); err == nil {
		if dec, derr := hex.DecodeString(strings.TrimSpace(string(b))); derr == nil && len(dec) >= 16 {
			return dec
		}
	}
	return nil
}
