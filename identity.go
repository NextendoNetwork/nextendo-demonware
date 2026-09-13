package main

// Identite du joueur lue dans la requete d'authentification.
//
// extra_data (chaine JSON) porte « username » (le surnom Nintendo) et « token »
// (id_token NSA). Le champ « nnex » du jeton vaut « nx2.<b64>.<sig> » ou <b64>
// decode donne « <PID Nextendo>.<surnom>.<expiration> ». Ce PID est le compte
// Nextendo du joueur ; Citron s'en sert aussi comme identifiant d'ami. La
// signature est verifiee dans gates.go.
//
// Nature de l'appareil, pour l'online-check de nextendo-account : l'id_token de
// l'emulateur porte les claims « di » et « sn », celui de la console (emis via
// le BaaS Nextendo) non. Constate sur Citron 2.7.7 et une Switch.

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

// playerIdentity rend l'identite du joueur et le claim nnex brut ("" s'il manque).
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
		id.Kind = "ryujinx" // nom Nextendo de la nature « emulateur »
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
