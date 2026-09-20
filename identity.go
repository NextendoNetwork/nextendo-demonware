package main

// Player identity read from the authentication request.
//
// extra_data (a JSON string) carries "username" (the Nintendo nickname) and
// "token" (the NSA id_token). The token's "nnex" claim is "nx2.<b64>.<sig>",
// where <b64> decodes to "<Nextendo PID>.<nickname>.<expiry>". This PID is
// the player's Nextendo account; Citron also uses it as its friend identifier
// (seen: player1 = 1800000101). The signature is checked in gates.go.
//
// Device kind, for nextendo-account's online-check: the emulator's id_token
// carries the "di" and "sn" claims, the console's (issued via Nextendo's BaaS)
// does not. Observed 2026-09-13 on Citron 2.7.7 and a CFW Switch.

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

func b64any(s string) []byte {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b
	}
	return nil
}

// playerIdentity returns the player identity and the raw nnex claim ("" if missing).
func playerIdentity(body []byte) (playerID, string) {
	id := playerID{Kind: "switch"}
	var req struct {
		ExtraData string `json:"extra_data"`
	}
	if json.Unmarshal(body, &req) != nil {
		return id, ""
	}
	var extra struct {
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if json.Unmarshal([]byte(req.ExtraData), &extra) != nil {
		return id, ""
	}
	id.Username = extra.Username

	parts := strings.Split(extra.Token, ".")
	if len(parts) < 2 {
		return id, ""
	}
	var claims struct {
		Sub  string          `json:"sub"`
		Nnex string          `json:"nnex"`
		DI   json.RawMessage `json:"di"`
		SN   json.RawMessage `json:"sn"`
	}
	if json.Unmarshal(b64any(parts[1]), &claims) != nil {
		return id, ""
	}
	id.Sub = claims.Sub
	if len(claims.DI) > 0 || len(claims.SN) > 0 {
		id.Kind = "ryujinx" // Nextendo's name for the "emulator" device kind
	}
	nn := strings.TrimPrefix(claims.Nnex, "nx2.")
	if i := strings.IndexByte(nn, '.'); i > 0 {
		nn = nn[:i]
	}
	if dec := string(b64any(nn)); dec != "" {
		f := strings.SplitN(dec, ".", 3)
		if pid, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			id.PID = pid
		}
		if id.Username == "" && len(f) > 1 {
			id.Username = f[1]
		}
	}
	return id, claims.Nnex
}
