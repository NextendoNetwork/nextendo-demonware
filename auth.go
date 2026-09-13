package main

// Authentification Demonware de Diablo III Switch (titre « crimson »).
//
// Le client (bdAuthSwitch, transport bdHTTPCurl) tape
//
//	https://crimson-switch-auth3.prod.demonware.net:<port>/auth/
//
// via sni-router, puis bascule sur le lobby (lobby.go).

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Magic du ticket bdAuth : octets DE AD BD EF.
const ticketMagic uint32 = 0xEFBDADDE

var authReqNum atomic.Uint64

func serveAuth() error {
	dumps := runDumpDir("auth")

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := authReqNum.Add(1)
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body.Close()

		// Ligne de requete seulement : le corps porte l'id_token NSA du joueur.
		log.Printf("[D3 Auth] #%d %s %s%s from %s (%d bytes)", n, r.Method, r.Host, r.URL.RequestURI(), r.RemoteAddr, len(body))
		if dumps != "" && len(body) > 0 {
			name := fmt.Sprintf("%03d_%s_%s.bin", n, r.Method,
				strings.NewReplacer("/", "_", "?", "_", "&", "_", ":", "_").Replace(strings.TrimPrefix(r.URL.RequestURI(), "/")))
			if err := os.WriteFile(filepath.Join(dumps, name), body, 0o644); err != nil {
				log.Printf("[D3 Auth] dump: %v", err)
			}
		}

		// --- reponse d'authentification ---------------------------------
		// Champs attendus par le client :
		//   code          doit valoir 700 (0x2BC), sinon la tache echoue
		//   iv_seed       entier, relu par le client
		//   client_ticket base64 -> exactement 128 octets
		//   server_ticket base64 -> exactement 128 octets, recopies tels quels
		//                 dans l'objet auth puis transmis au lobby. Opaque pour
		//                 le client : son contenu ne regarde que notre lobby.
		var req struct {
			AuthTask string `json:"auth_task"`
			IVSeed   string `json:"iv_seed"`
			TitleID  string `json:"title_id"`
			Identity string `json:"identity"`
		}
		_ = json.Unmarshal(body, &req)

		// Gardes Nextendo AVANT d'emettre le moindre ticket (gates.go).
		id, nnex := playerIdentity(body)
		if ok, reason := admitPlayer(&id, nnex, clientIP(r)); !ok {
			log.Printf("[D3 Auth] #%d %q pid=%d kind=%s REFUSED (%s)", n, id.Username, id.PID, id.Kind, reason)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Le client compare les 4 premiers octets du ticket au magic 0xEFBDADDE
		// (octets DE AD BD EF). S'ils correspondent, le ticket est lu en clair —
		// c'est ce qui permet un serveur autonome, sans cle ni patch du client.
		//
		// Disposition du ticket, 128 octets pile :
		//   +0 u32 magic | +4 u8 | +5 u32 | +9 u32 | +13 u32
		//   +17 u64 | +25 u64 | +33 [64] cle de session | +97 [24] | +121 [3] | +124 [4]
		clientTicket := make([]byte, 128)
		binary.LittleEndian.PutUint32(clientTicket[0:], ticketMagic)
		clientTicket[4] = 1
		binary.LittleEndian.PutUint32(clientTicket[5:], 5745) // title id
		binary.LittleEndian.PutUint32(clientTicket[9:], 0)    // reserve
		binary.LittleEndian.PutUint32(clientTicket[13:], uint32(time.Now().Unix()))
		binary.LittleEndian.PutUint64(clientTicket[17:], 1) // user id
		binary.LittleEndian.PutUint64(clientTicket[25:], uint64(time.Now().Unix()+86400))
		if _, err := rand.Read(clientTicket[33:97]); err != nil { // cle de session
			log.Printf("[D3 Auth] rand: %v", err)
		}
		// +97 [24] : la cle du lobby, qui signe la poignee de main. On y met les
		// 24 premiers octets de la cle de session.
		copy(clientTicket[97:121], clientTicket[33:57])

		// Opaque pour le client : il le recopie tel quel et le transmet au lobby
		// dans le 0x82. C'est notre lobby qui le relit, donc on y met tout.
		serverTicket := append([]byte{}, clientTicket...)

		// Le lobby retrouve la cle de la session en essayant les tickets emis
		// recemment : c'est le tag du 0x82 qui designe le bon. L'identite du
		// joueur (amis en ligne, presence) est retrouvee par les 8 octets de cle.
		if err := os.MkdirAll(sessDir, 0o755); err == nil {
			name := fmt.Sprintf("%d_%x.tkt", time.Now().Unix(), clientTicket[33:37])
			if err := os.WriteFile(filepath.Join(sessDir, name), clientTicket, 0o644); err != nil {
				log.Printf("[D3 Auth] session: %v", err)
			}
			if raw, err := json.Marshal(id); err == nil {
				_ = os.WriteFile(filepath.Join(sessDir, fmt.Sprintf("id_%x.json", clientTicket[33:41])), raw, 0o644)
			}
		}

		ivSeed := req.IVSeed
		if ivSeed == "" {
			ivSeed = "0"
		}

		// auth_task est lu AVANT code : sans lui la reponse est rejetee (735).
		// Ce n'est PAS un echo : requete et reponse s'apparient en n -> n+1
		// (la console envoie 78 et attend 79).
		authTask := "79"
		if v, err := strconv.Atoi(req.AuthTask); err == nil {
			authTask = strconv.Itoa(v + 1)
		}

		// Le client encode TOUS ses entiers en chaines JSON ("auth_task": "78") :
		// on repond dans le meme style. L'ORDRE compte (parseur sequentiel :
		// auth_task, code, iv_seed, client_ticket, server_ticket), d'ou une
		// struct et pas une map.
		//
		// extra_data est une CHAINE contenant du JSON ; le client y lit
		// nso_subscription_status. L'etat d'abonnement fait donc partie de la
		// reponse d'auth : aucun patch du jeu.
		extra, _ := json.Marshal(map[string]string{
			"nso_subscription_status": "1",
		})

		resp := struct {
			AuthTask     string `json:"auth_task"`
			Code         string `json:"code"`
			IVSeed       string `json:"iv_seed"`
			ClientTicket string `json:"client_ticket"`
			ServerTicket string `json:"server_ticket"`
			ExtraData    string `json:"extra_data"`
		}{
			AuthTask:     authTask,
			Code:         "700",
			IVSeed:       ivSeed,
			ClientTicket: base64.StdEncoding.EncodeToString(clientTicket),
			ServerTicket: base64.StdEncoding.EncodeToString(serverTicket),
			ExtraData:    string(extra),
		}
		out, _ := json.Marshal(resp)

		loginsTotal.Add(1)
		log.Printf("[D3 Auth] #%d %q pid=%d kind=%s -> 200 code=700 auth_task=%s", n, id.Username, id.PID, id.Kind, authTask)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", authPort),
		Handler: h,
		// Le client est un curl embarque de 2018 : ne pas exiger TLS 1.2+.
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS10},
		ReadHeaderTimeout: 15 * time.Second,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	// Behind sni-router: the PROXY header carries the player's address (proxyproto.go).
	proxyProto := os.Getenv("NEXTENDO_PROXY_PROTOCOL") == "1"
	if proxyProto {
		ln = newProxyListener(ln)
	}
	log.Printf("[D3 Auth] listening HTTPS :%d (proxyProto=%v, cert=%s)", authPort, proxyProto, certFile)
	return srv.ServeTLS(ln, certFile, keyFile)
}

// clientIP rend l'adresse de l'appelant sans le port (127.0.0.1 derriere sni-router).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
