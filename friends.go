package main

// Amis en ligne dans D3.
//
// A la connexion, le jeu demande 12/9 (getUserNames) avec les identifiants de
// ses amis Nintendo. Ces identifiants dependent de la plateforme :
//
//	console : identifiant de compte appareil de l'ami (liste d'amis BaaS)
//	Citron  : PID Nextendo de l'ami
//
// Resultat 12/9 : 0A u64 id | 10 chaine surnom.
// Resultat 29/4 : 0A u64 id | 13 blob.
//
// On repond pour les amis actuellement connectes au lobby. Le joueur d'une
// connexion est connu par son ticket (auth.go ecrit sessions/id_<cle>.json) ;
// les identifiants d'appareil viennent des listes d'amis que la console
// recupere via baas-proxy, relues dans son journal.

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type playerID struct {
	Username string `json:"username"`
	PID      uint64 `json:"pid"`
	Sub      string `json:"sub"`
	Kind     string `json:"kind,omitempty"` // "switch" ou "ryujinx" (emulateur), cf. identity.go
}

var (
	onlineMu sync.Mutex
	online   = map[uint64]*playerID{} // numero de connexion -> joueur

	userDataMu sync.Mutex
	userData   = map[string][]byte{} // surnom|contexte -> blob 29/1

	aliasMu     sync.Mutex
	aliases     = map[uint64]string{} // identifiant d'appareil / NSA -> surnom
	aliasLoaded time.Time
)

// Connexions dont le fichier d'identite n'existait pas encore a la poignee de
// main (ticket emis avant un redemarrage). On retente a chaque recherche
// d'amis : le fichier apparait des que le joueur se reauthentifie.
var pending = map[uint64]pendingID{}

type pendingID struct {
	conn   *lobbyConn
	ticket []byte
}

// loadIdentity retrouve le joueur d'un ticket via le fichier ecrit par auth.go.
func (l *lobbyConn) loadIdentity(ticket []byte) {
	if len(ticket) < 41 {
		return
	}
	if !l.tryIdentity(ticket) {
		onlineMu.Lock()
		pending[l.n] = pendingID{conn: l, ticket: append([]byte{}, ticket...)}
		onlineMu.Unlock()
		l.logf("identite en attente (ticket %x)", ticket[33:41])
	}
}

func (l *lobbyConn) tryIdentity(ticket []byte) bool {
	p := filepath.Join(l.sessDir, "id_"+hex.EncodeToString(ticket[33:41])+".json")
	raw, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	var id playerID
	if json.Unmarshal(raw, &id) != nil || id.Username == "" {
		return false
	}
	l.player = &id
	onlineMu.Lock()
	online[l.n] = &id
	delete(pending, l.n)
	onlineMu.Unlock()
	l.logf("joueur %q pid=%d", id.Username, id.PID)
	return true
}

// retryPending retente les identites manquantes.
func retryPending() {
	onlineMu.Lock()
	todo := make([]pendingID, 0, len(pending))
	for _, p := range pending {
		todo = append(todo, p)
	}
	onlineMu.Unlock()
	for _, p := range todo {
		p.conn.tryIdentity(p.ticket)
	}
}

func dropOnline(conn uint64) {
	onlineMu.Lock()
	delete(online, conn)
	delete(pending, conn)
	onlineMu.Unlock()
}

// refreshAliases relit les listes d'amis passees par baas-proxy (au plus une
// fois par minute) pour apprendre identifiant d'appareil/NSA -> surnom.
func refreshAliases() {
	aliasMu.Lock()
	defer aliasMu.Unlock()
	if time.Since(aliasLoaded) < time.Minute {
		return
	}
	aliasLoaded = time.Now()
	f, err := os.Open(envOr("BAASPROXY_LOG", "../baas-proxy/logs/baas-proxy.log"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, `body: {"count"`)
		if i < 0 {
			continue
		}
		var list struct {
			Items []struct {
				Friend struct {
					ID             string `json:"id"`
					Nickname       string `json:"nickname"`
					DeviceAccounts []struct {
						ID string `json:"id"`
					} `json:"deviceAccounts"`
				} `json:"friend"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(line[i+6:]), &list) != nil {
			continue
		}
		for _, it := range list.Items {
			if it.Friend.Nickname == "" {
				continue
			}
			if v, err := strconv.ParseUint(it.Friend.ID, 16, 64); err == nil {
				aliases[v] = it.Friend.Nickname
			}
			for _, d := range it.Friend.DeviceAccounts {
				if v, err := strconv.ParseUint(d.ID, 16, 64); err == nil {
					aliases[v] = it.Friend.Nickname
				}
			}
		}
	}
}

// nicknameOnline rend le surnom d'un joueur connecte designe par id (PID ou
// identifiant d'appareil/NSA).
func nicknameOnline(id uint64) string {
	retryPending()
	refreshAliases()
	aliasMu.Lock()
	name := aliases[id]
	aliasMu.Unlock()
	onlineMu.Lock()
	defer onlineMu.Unlock()
	for _, p := range online {
		if (p.PID != 0 && p.PID == id) || (name != "" && strings.EqualFold(p.Username, name)) {
			return p.Username
		}
	}
	return ""
}

func (w *bdWriter) str(s string) {
	w.b = append(w.b, tagString)
	w.b = append(w.b, s...)
	w.b = append(w.b, 0)
}

// u64s lit une suite d'u64 types (0A) et, s'il y en a, un tableau d'u64
// (6E | 08 u32 taille | u32 nombre | u64 bruts).
func (r *bdReader) u64s() []uint64 {
	var out []uint64
	for r.off < len(r.b) {
		switch r.b[r.off] {
		case tagU64:
			if r.off+9 > len(r.b) {
				return out
			}
			out = append(out, binary.LittleEndian.Uint64(r.b[r.off+1:]))
			r.off += 9
		case 100 + tagU64:
			r.off++
			if _, err := r.u32(); err != nil || r.off+4 > len(r.b) {
				return out
			}
			n := int(binary.LittleEndian.Uint32(r.b[r.off:]))
			r.off += 4
			for i := 0; i < n && r.off+8 <= len(r.b); i++ {
				out = append(out, binary.LittleEndian.Uint64(r.b[r.off:]))
				r.off += 8
			}
		default:
			return out
		}
	}
	return out
}

// onGetUserNames : 12/9.
func (l *lobbyConn) onGetUserNames(task byte, r *bdReader) []byte {
	ids := r.u64s()
	type hit struct {
		id   uint64
		name string
	}
	var hits []hit
	for _, id := range ids {
		if name := nicknameOnline(id); name != "" {
			hits = append(hits, hit{id, name})
		}
	}
	l.logf("amis getUserNames %d demande(s) -> %d en ligne %v", len(ids), len(hits), hits)
	return taskReply(task, 0, func(w *bdWriter) uint32 {
		for _, h := range hits {
			w.u64(h.id)
			w.str(h.name)
		}
		return uint32(len(hits))
	})
}

// onUserData : service 29. 1 = ecrire ses donnees, 4 = lire celles d'une liste.
func (l *lobbyConn) onUserData(task byte, r *bdReader) []byte {
	ctx, _ := r.str()
	switch task {
	case 1:
		data, err := r.blob()
		if err != nil || l.player == nil {
			l.logf("userdata SET ctx=%q ignore (%v, joueur=%v)", ctx, err, l.player != nil)
			return taskReply(task, 0, nil)
		}
		userDataMu.Lock()
		userData[strings.ToLower(l.player.Username)+"|"+ctx] = data
		userDataMu.Unlock()
		l.logf("userdata SET %s ctx=%q %d octets", l.player.Username, ctx, len(data))
		return taskReply(task, 0, nil)
	case 4:
		if r.off < len(r.b) && r.b[r.off] == tagBool {
			r.off += 2
		}
		ids := r.u64s()
		type hit struct {
			id   uint64
			data []byte
		}
		var hits []hit
		for _, id := range ids {
			name := nicknameOnline(id)
			if name == "" {
				continue
			}
			userDataMu.Lock()
			d, ok := userData[strings.ToLower(name)+"|"+ctx]
			userDataMu.Unlock()
			if ok {
				hits = append(hits, hit{id, d})
			}
		}
		l.logf("userdata GET ctx=%q %d demande(s) -> %d", ctx, len(ids), len(hits))
		return taskReply(task, 0, func(w *bdWriter) uint32 {
			for _, h := range hits {
				w.u64(h.id)
				w.blobv(h.data)
			}
			return uint32(len(hits))
		})
	}
	return nil
}
