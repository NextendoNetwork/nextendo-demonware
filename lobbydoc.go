package main

// Host document returned by bdAsyncMatchMaking::getLobbyDocuments (145/3).
//
// Net::igNetLobbyHostDoc::deserialize reads, in this order: update_time (u64), update_id (u64),
// game_id (u64, preceded by a hasKey), lobby_open (bool), lobby_open_for_pres_join (bool), then
// player_state, listen_server, dedicated_server, team_balance{can_change_teams},
// ruleset_payload and attachment. listen_server carries host_address (a bdCommonAddr in base64,
// passed to bdBase64::decode) and host_player_id (u64); player_state is walked by index,
// each field being a u32.
//
// The document the client UPLOADS in 145/2 has a different shape
// (listen_server{local_address,security_id,security_key,nat}): returning it as-is yields
// none of the expected keys. Only the address is extracted from it to compose this one.

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

// uploadedHostAddress returns listen_server.local_address from the document uploaded in 145/2.
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
