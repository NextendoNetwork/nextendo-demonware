package main

// Poignee de main du lobby Demonware.
//
// Connexion : le client tire 8 octets aleatoires et envoie 28 octets BRUTS,
// hors trame :
//
//	u32 200 | u32 200 | u32 210 | u32 220 | u32 maxTrame | nonce[8]
//
// Ensuite tout est trame :
//
//	u32 L | u8 drapeau | corps[L-1]        L=0 : keepalive
//
// le premier octet du corps etant le type. Etats du client :
//
//	1  attend 0x81  u32 version 210..220 | u64 identifiant | 8 octets
//	   -> le client repond 0x82 et derive les cles
//	2  attend 0x83  u64 == CLIENTCHAL[8:16]              | 0x84 = erreur u32
//	3  attend 0x85  u32 seq | iv[16] | AES-CBC | tag[8]  | 0x84 = erreur u32
//
// Transcript signe par les deux cotes :
//
//	u32 210 | u32 220 | u32 maxTrame | nonce[8]
//	| u32 (len81+2) | 0xAB | 0x81 | charge81
//	| trame 0x82 complete sans ses 8 derniers octets

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	frameFlag      = 0xAB
	lobbyVersion   = 220 // le plus haut accepte ; le client en propose 210 et 220
	helloLen       = 28
	msgServerHi    = 0x81
	msgClientAuth  = 0x82
	msgServerOK    = 0x83
	msgError       = 0x84
	msgEncrypted   = 0x85
	msgMigrateInit = 0x87
	msgMigrateAck  = 0x88
	msgMigrateGo   = 0x89
)

var serverIDs atomic.Uint64

func u32le(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// frame emballe un corps (type inclus) avec longueur et drapeau.
func frame(body []byte) []byte {
	out := make([]byte, 0, 5+len(body))
	out = append(out, u32le(uint32(1+len(body)))...)
	out = append(out, frameFlag)
	return append(out, body...)
}

type lobbyConn struct {
	c       net.Conn
	n       uint64
	dumpDir string
	sessDir string
	pubDir  string

	maxLen   uint32
	nonce    []byte
	payload1 []byte // charge du 0x81 envoye, pour le transcript

	keys    *sessionKeys
	player  *playerID
	s2cSeq  uint32
	sendMu  sync.Mutex
	msgSeen int

	// Dernier document d'hote recu en 145/2, rendu tel quel en 145/3.
	lobbyDoc string

	// Emis apres la reponse de tache en cours (messages pousses).
	afterReply func()
}

func (l *lobbyConn) logf(format string, a ...any) {
	log.Printf("#%d "+format, append([]any{l.n}, a...)...)
}

func (l *lobbyConn) readFull(n int) ([]byte, error) {
	b := make([]byte, n)
	// Long : une connexion gardee muette doit durer tant que le jeu la garde.
	_ = l.c.SetReadDeadline(time.Now().Add(30 * time.Minute))
	_, err := io.ReadFull(l.c, b)
	return b, err
}

// readFrame rend la trame BRUTE (longueur comprise), nil pour un keepalive.
func (l *lobbyConn) readFrame() ([]byte, error) {
	hdr, err := l.readFull(4)
	if err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr)
	if n == 0 {
		return nil, nil
	}
	if n > l.maxLen || n > 1<<21 {
		return nil, fmt.Errorf("trame de %d octets refusee", n)
	}
	rest, err := l.readFull(int(n))
	if err != nil {
		return nil, err
	}
	return append(hdr, rest...), nil
}

func (l *lobbyConn) send(b []byte) error {
	_ = l.c.SetWriteDeadline(time.Now().Add(15 * time.Second))
	_, err := l.c.Write(b)
	return err
}

func (l *lobbyConn) dump(tag string, b []byte) {
	if l.dumpDir == "" {
		return
	}
	name := fmt.Sprintf("lobby_%03d_%s.bin", l.n, tag)
	_ = os.WriteFile(filepath.Join(l.dumpDir, name), b, 0o644)
}

func (l *lobbyConn) run() {
	defer l.c.Close()
	defer dropSessionsOf(l.n)
	defer ctrMM.leave(l, 0)
	defer dropOnline(l.n)
	l.logf("==== CONNECT from=%s", l.c.RemoteAddr())

	hello, err := l.readFull(helloLen)
	if err != nil {
		l.logf("hello: %v", err)
		return
	}
	l.dump("hello", hello)
	v := func(i int) uint32 { return binary.LittleEndian.Uint32(hello[i:]) }
	if v(0) != 200 || v(4) != 200 || v(8) != 210 || v(12) != 220 {
		l.logf("hello inattendu: %X", hello)
		return
	}
	l.maxLen = v(16)
	l.nonce = append([]byte{}, hello[20:28]...)
	l.logf("hello ok: versions 210..220 maxTrame=0x%X nonce=%X", l.maxLen, l.nonce)

	// 0x81 : version | identifiant serveur (remonte au jeu dans
	// l'evenement « connecte ») | 8 octets que le client ne fait que sauter.
	id := 0x0D3000000000 + serverIDs.Add(1)
	pad := make([]byte, 8)
	_, _ = rand.Read(pad)
	l.payload1 = make([]byte, 0, 20)
	l.payload1 = append(l.payload1, u32le(lobbyVersion)...)
	l.payload1 = binary.LittleEndian.AppendUint64(l.payload1, id)
	l.payload1 = append(l.payload1, pad...)
	if err := l.send(frame(append([]byte{msgServerHi}, l.payload1...))); err != nil {
		l.logf("envoi 0x81: %v", err)
		return
	}
	l.logf("-> 0x81 version=%d id=0x%X", lobbyVersion, id)

	for {
		raw, err := l.readFrame()
		if err != nil {
			if err != io.EOF {
				l.logf("fin: %v", err)
			} else {
				l.logf("fin: le client a ferme")
			}
			return
		}
		if raw == nil {
			// Keepalive : on renvoie le meme, c'est ce que le lecteur accepte.
			_ = l.send(make([]byte, 4))
			continue
		}
		body := raw[5:]
		if len(body) == 0 {
			continue
		}
		l.msgSeen++
		l.dump(fmt.Sprintf("in%02d_%02x", l.msgSeen, body[0]), raw)

		switch {
		case l.keys == nil && body[0] == msgClientAuth:
			if !l.onClientAuth(raw) {
				return
			}
		case l.keys == nil:
			l.logf("type 0x%02X avant la poignee de main:\n%s", body[0], hex.Dump(raw))
		default:
			l.onEncrypted(raw)
		}
	}
}

// onClientAuth verifie le tag du 0x82 contre chaque cle candidate, garde celle
// qui correspond et repond 0x83.
func (l *lobbyConn) onClientAuth(raw []byte) bool {
	if len(raw) < 5+1+8 {
		l.logf("0x82 trop court")
		return false
	}
	tag := raw[len(raw)-8:]

	var t bytes.Buffer
	t.Write(u32le(210))
	t.Write(u32le(220))
	t.Write(u32le(l.maxLen))
	t.Write(l.nonce)
	t.Write(u32le(uint32(len(l.payload1) + 2)))
	t.WriteByte(frameFlag)
	t.WriteByte(msgServerHi)
	t.Write(l.payload1)
	t.Write(raw[:len(raw)-8])
	transcript := t.Bytes()

	for _, cand := range l.candidates(raw) {
		for _, wrapped := range []bool{false, true} {
			k24 := cand.key24
			if wrapped {
				k24 = wrapKey24(k24)
			}
			k := deriveKeys(transcript, k24)
			if bytes.Equal(k.clientTag[:], tag) {
				l.keys = &k
				l.logf("0x82 AUTHENTIFIE — cle=%s rsa=%v", cand.name, wrapped)
				if tk := findTicket(raw); tk != nil {
					l.loadIdentity(tk)
				}
				if err := l.send(frame(append([]byte{msgServerOK}, k.serverChk[:]...))); err != nil {
					l.logf("envoi 0x83: %v", err)
					return false
				}
				l.logf("-> 0x83 chk=%X ; session chiffree etablie", k.serverChk)
				// Le 0x83 met deja [+0x258]=3, seule condition de getStatus()==2 : le 0x87 n'apporte rien
				// et fait attendre au jeu une migration (fermeture au bout de ~12s). Garde sous CTR_MIGRATE.
				if migrateEnabled {
					if err := l.sendEncrypted(msgMigrateInit, []byte{1}); err != nil {
						l.logf("envoi 0x87: %v", err)
						return false
					}
					l.logf("-> 0x87 migrate-init")
				}
				return true
			}
		}
	}

	// Echec : on NE ferme PAS. Une connexion lobby muette laisse le jeu en
	// ligne « amis seulement » (invitations P2P) ; une fermeture ou une trame
	// invalide le fait basculer hors ligne apres quelques essais.
	l.logf("0x82: aucune cle ne reproduit le tag %X (%d candidates) — connexion gardee muette\n%s", tag, len(l.candidates(raw)), hex.Dump(raw))
	return true
}

type keyCand struct {
	name  string
	key24 []byte
}

// candidates rassemble les cles de 24 octets plausibles : ticket extrait du
// 0x82 lui-meme, tickets emis par auth.go, et zero (tickets anterieurs, dont
// les octets 97..120 etaient nuls).
func (l *lobbyConn) candidates(raw []byte) []keyCand {
	var out []keyCand
	add := func(name string, t []byte) {
		out = append(out,
			keyCand{name + "[97:121]", append([]byte{}, t[97:121]...)},
			keyCand{name + "[33:57]", append([]byte{}, t[33:57]...)},
		)
	}
	if t := findTicket(raw); t != nil {
		add("ticket-0x82", t)
	}
	if entries, err := os.ReadDir(l.sessDir); err == nil {
		cutoff := time.Now().Add(-72 * time.Hour)
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".tkt") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(l.sessDir, e.Name()))
			if err == nil && len(b) == 128 {
				add(e.Name(), b)
			}
		}
	}
	out = append(out, keyCand{"zero", make([]byte, 24)})
	return out
}

// findTicket cherche le magic DE AD BD EF dans le bdBitBuffer du 0x82, a tout
// decalage de bit (les champs precedents ne sont pas alignes sur l'octet), en
// ordre LSB puis MSB, et rend les 128 octets qui suivent.
func findTicket(raw []byte) []byte {
	magic := []byte{0xDE, 0xAD, 0xBD, 0xEF}
	bit := func(i int, msb bool) byte {
		b := raw[i/8]
		if msb {
			return (b >> (7 - uint(i%8))) & 1
		}
		return (b >> uint(i%8)) & 1
	}
	total := len(raw) * 8
	for _, msb := range []bool{false, true} {
		for off := 0; off+128*8 <= total; off++ {
			get := func(k int) byte {
				var v byte
				for j := 0; j < 8; j++ {
					if msb {
						v |= bit(off+k*8+j, true) << uint(7-j)
					} else {
						v |= bit(off+k*8+j, false) << uint(j)
					}
				}
				return v
			}
			ok := true
			for k := 0; k < 4 && ok; k++ {
				ok = get(k) == magic[k]
			}
			if !ok {
				continue
			}
			t := make([]byte, 128)
			for k := range t {
				t[k] = get(k)
			}
			return t
		}
	}
	return nil
}

// onEncrypted decode un message client apres la poignee de main. Le format
// client->serveur n'a pas encore ete lu dans le binaire : on suppose le miroir
// du 0x85 serveur et on journalise tout, verifie ou non.
func (l *lobbyConn) onEncrypted(raw []byte) {
	body := raw[5:]
	typ := body[0]
	if len(body) < 1+4+16+16+8 {
		l.logf("msg 0x%02X court (%d):\n%s", typ, len(raw), hex.Dump(raw))
		return
	}
	seq := binary.LittleEndian.Uint32(body[1:5])
	iv := body[5:21]
	ct := body[21 : len(body)-8]
	tag := raw[len(raw)-8:]
	macOK := bytes.Equal(hmacSHA1(l.keys.c2sMAC, raw[:len(raw)-8])[:8], tag)

	pt, err := aesCBCDecrypt(l.keys.c2sAES, iv, ct)
	if err != nil {
		l.logf("msg 0x%02X seq=%d mac=%v dechiffrement: %v\n%s", typ, seq, macOK, err, hex.Dump(raw))
		return
	}
	l.dump(fmt.Sprintf("in%02d_plain", l.msgSeen), pt)
	if len(pt) < 5 {
		l.logf("msg 0x%02X seq=%d mac=%v clair trop court: %X", typ, seq, macOK, pt)
		return
	}
	n := binary.LittleEndian.Uint32(pt[0:4])
	inner := pt[4]
	if verbose {
		l.logf("msg 0x%02X seq=%d mac=%v inner_len=%d inner_type=0x%02X clair:\n%s", typ, seq, macOK, n, inner, hex.Dump(pt))
	} else if !macOK {
		l.logf("msg 0x%02X seq=%d MAC invalide, ignore", typ, seq)
	}
	if !macOK || int(n) > len(pt)-5 {
		return
	}
	if inner == 0x86 {
		l.onTask(pt[5 : 5+n])
		return
	}
	if inner == msgMigrateAck {
		// Le 0x88 clot le 0x87 : rien n'est attendu ensuite. Un 0x89 declencherait une vraie migration.
		if !migrateEnabled {
			l.logf("0x88 migrate-ack — login LSG complet")
			return
		}
		if err := l.sendEncrypted(msgMigrateGo, migrateGoPayload()); err != nil {
			l.logf("envoi 0x89: %v", err)
			return
		}
		l.logf("-> 0x89 migrate-go %s:%d", migrateIP(), migratePort)
		return
	}
	l.logf("message interne 0x%02X ignore (%d octets)", inner, n)
}

// Le jeu reprend la poignee de main complete sur la connexion migree, donc on
// le renvoie sur le port d'ecoute habituel.
const migratePort uint16 = 3074

var migrateEnabled = os.Getenv("CTR_MIGRATE") == "1"

func migrateIP() net.IP {
	ip := net.ParseIP(nextendoHost).To4()
	if ip == nil {
		ip = net.IPv4(127, 0, 0, 1).To4()
	}
	return ip
}

// migrateGoPayload : 01 | type 2 (adresse fournie) | taille de l'adresse | bdAddr | u16 taille du
// jeton | jeton | u32. parse220MigrateAck exige ce dernier u32 (millisecondes, *0.001 et plafonne
// a 30s dans +0x764) : sans lui elle rend false et process220MigrateAck ferme la connexion.
const migrateAddrLen = 6
const migrateDelayMS uint32 = 1000

func migrateGoPayload() []byte {
	p := []byte{1, 2, migrateAddrLen}
	p = append(p, migrateIP()...)
	p = append(p, byte(migratePort&0xFF), byte(migratePort>>8))
	p = append(p, 0, 0)
	d := migrateDelayMS
	return append(p, byte(d), byte(d>>8), byte(d>>16), byte(d>>24))
}

// sendEncrypted emet un 0x85 : u32 seq | iv | AES-CBC(u32 N | type | charge | bourrage) | tag.
func (l *lobbyConn) sendEncrypted(innerType byte, payload []byte) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	pt := make([]byte, 0, 5+len(payload)+16)
	pt = append(pt, u32le(uint32(len(payload)))...)
	pt = append(pt, innerType)
	pt = append(pt, payload...)
	for len(pt)%16 != 0 {
		pt = append(pt, 0)
	}
	iv := make([]byte, 16)
	_, _ = rand.Read(iv)
	ct, err := aesCBCEncrypt(l.keys.s2cAES, iv, pt)
	if err != nil {
		return err
	}
	l.s2cSeq++
	body := []byte{msgEncrypted}
	body = append(body, u32le(l.s2cSeq)...)
	body = append(body, iv...)
	body = append(body, ct...)

	out := u32le(uint32(1 + len(body) + 8))
	out = append(out, frameFlag)
	out = append(out, body...)
	out = append(out, hmacSHA1(l.keys.s2cMAC, out)[:8]...)
	return l.send(out)
}
