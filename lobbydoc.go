package main

// Document d'hote rendu par bdAsyncMatchMaking::getLobbyDocuments (145/3).
//
// Net::igNetLobbyHostDoc::deserialize lit, dans cet ordre : update_time (u64), update_id (u64),
// game_id (u64, precede d'un hasKey), lobby_open (bool), lobby_open_for_pres_join (bool), puis
// player_state, listen_server, dedicated_server, team_balance{can_change_teams},
// ruleset_payload et attachment. listen_server porte host_address (bdCommonAddr en base64,
// passe a bdBase64::decode) et host_player_id (u64) ; player_state est parcouru par index,
// chaque champ etant un u32.
//
// Le document que le client TELEVERSE en 145/2 a une forme differente
// (listen_server{local_address,security_id,security_key,nat}) : le rendre tel quel ne donne
// aucune des cles attendues. On en extrait seulement l'adresse pour composer celui-ci.

import (
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"
)

var lobbyDocUpdateID atomic.Uint64

type lobbyHostDoc struct {
	UpdateTime           uint64            `json:"update_time"`
	UpdateID             uint64            `json:"update_id"`
	GameID               uint64            `json:"game_id"`
	LobbyOpen            bool              `json:"lobby_open"`
	LobbyOpenForPresJoin bool              `json:"lobby_open_for_pres_join"`
	PlayerState          map[string]uint32 `json:"player_state"`
	ListenServer         lobbyListenServer `json:"listen_server"`
	TeamBalance          lobbyTeamBalance  `json:"team_balance"`
}

type lobbyListenServer struct {
	HostAddress  string `json:"host_address"`
	HostPlayerID uint64 `json:"host_player_id"`
}

type lobbyTeamBalance struct {
	CanChangeTeams bool `json:"can_change_teams"`
}

// uploadedHostAddress rend listen_server.local_address du document televerse en 145/2.
func uploadedHostAddress(doc string) string {
	if doc == "" {
		return ""
	}
	var parsed struct {
		ListenServer struct {
			LocalAddress string `json:"local_address"`
		} `json:"listen_server"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return ""
	}
	return parsed.ListenServer.LocalAddress
}

func (l *lobbyConn) hostDocument() string {
	pid := l.pid()
	doc := lobbyHostDoc{
		UpdateTime:           uint64(time.Now().Unix()),
		UpdateID:             lobbyDocUpdateID.Add(1),
		GameID:               pid,
		LobbyOpen:            true,
		LobbyOpenForPresJoin: true,
		PlayerState:          map[string]uint32{strconv.FormatUint(pid, 10): 1},
		ListenServer: lobbyListenServer{
			HostAddress:  uploadedHostAddress(l.lobbyDoc),
			HostPlayerID: pid,
		},
		TeamBalance: lobbyTeamBalance{CanChangeTeams: true},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	return string(out)
}
