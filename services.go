package main

// Taches distantes du lobby (bdRemoteTask).
//
// Requete client (message chiffre, type interne 0x86) : un bdByteBuffer TYPE
// dont le premier octet — l'identifiant de service — est ecrit brut :
//
//	u8 service | 03 u8 tache | arguments types
//
// Reponse (type interne 0x01) :
//
//	0A u64 transaction | 08 u32 erreur | 03 u8 | 08 u32 nbResultats | 08 u32 total | resultats
//
// L'erreur 0 donne des resultats, 200 laisse la tache en attente, toute autre
// valeur la fait echouer proprement. Les taches sont servies dans l'ordre (la
// premiere en attente prend la reponse) : pas d'identifiant a apparier.
//
// Etiquettes de type :
// 01 bool, 03 u8, 08 u32, 0A u64, 10 chaine terminee par NUL, 13 blob (08 u32 + octets).

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	tagBool   = 0x01
	tagU8     = 0x03
	tagI32    = 0x07
	tagU32    = 0x08
	tagU64    = 0x0A
	tagString = 0x10
	tagBlob   = 0x13

	innerTaskReply = 0x01

	svcStorage        = 10
	svcTitleUtilities = 12

	taskGetPublisherFile = 21
	taskGetServerTime    = 6
)

// Erreur renvoyee pour une tache non geree : non nulle et differente de 200,
// la tache passe en echec au lieu d'expirer (une expiration coupe le lobby).
const errUnhandled = 1

var transactions atomic.Uint64

// bdWriter ecrit un bdByteBuffer type.
type bdWriter struct{ b []byte }

func (w *bdWriter) u8(v byte) { w.b = append(w.b, tagU8, v) }
func (w *bdWriter) u32(v uint32) {
	w.b = append(w.b, tagU32)
	w.b = binary.LittleEndian.AppendUint32(w.b, v)
}
func (w *bdWriter) u64(v uint64) {
	w.b = append(w.b, tagU64)
	w.b = binary.LittleEndian.AppendUint64(w.b, v)
}
func (w *bdWriter) blobv(p []byte) {
	w.b = append(w.b, tagBlob)
	w.u32(uint32(len(p)))
	w.b = append(w.b, p...)
}

// bdReader lit un bdByteBuffer type (arguments des requetes).
type bdReader struct {
	b   []byte
	off int
}

var errShort = errors.New("tampon trop court")

func (r *bdReader) tag(want byte) error {
	if r.off >= len(r.b) {
		return errShort
	}
	if r.b[r.off] != want {
		return errors.New("etiquette inattendue")
	}
	r.off++
	return nil
}

func (r *bdReader) u8() (byte, error) {
	if err := r.tag(tagU8); err != nil {
		return 0, err
	}
	if r.off >= len(r.b) {
		return 0, errShort
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}

func (r *bdReader) str() (string, error) {
	if err := r.tag(tagString); err != nil {
		return "", err
	}
	end := r.off
	for end < len(r.b) && r.b[end] != 0 {
		end++
	}
	if end >= len(r.b) {
		return "", errShort
	}
	s := string(r.b[r.off:end])
	r.off = end + 1
	return s, nil
}

// taskReply construit la charge d'une reponse de tache.
func taskReply(task byte, errCode uint32, results func(w *bdWriter) uint32) []byte {
	w := &bdWriter{}
	w.u64(transactions.Add(1))
	w.u32(errCode)
	if errCode != 0 {
		return w.b
	}
	w.u8(task)
	body := &bdWriter{}
	n := uint32(0)
	if results != nil {
		n = results(body)
	}
	// Le client ne lit le total que s'il y a au moins un resultat.
	w.u32(n)
	if n > 0 {
		w.u32(n)
		w.b = append(w.b, body.b...)
	}
	return w.b
}

// onTask traite une requete dechiffree. payload = octets apres le type interne.
func (l *lobbyConn) onTask(payload []byte) {
	if len(payload) < 3 {
		l.logf("tache: charge trop courte %X", payload)
		return
	}
	service := payload[0]
	r := &bdReader{b: payload, off: 1}
	task, err := r.u8()
	if err != nil {
		l.logf("tache: service %d sans id de tache (%v): %X", service, err, payload)
		return
	}
	noteTask(l, service, task)

	var reply []byte
	switch {
	case service == svcTitleUtilities && task == taskGetServerTime:
		now := uint32(time.Now().Unix())
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.u32(now); return 1 })
		l.logf("tache bdTitleUtilities.getServerTime -> %d", now)

	case service == svcStorage && task == taskGetPublisherFile:
		ctx, err1 := r.str()
		name, err2 := r.str()
		if err1 != nil || err2 != nil {
			l.logf("getPublisherFile: arguments illisibles: %X", payload)
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		data, path := l.publisherFile(name)
		if data == nil {
			// Aucun resultat plutot qu'une erreur : le jeu traite « fichier
			// absent » comme un cas normal.
			reply = taskReply(task, 0, nil)
			l.logf("tache bdStorage.getPublisherFile ctx=%q file=%q -> ABSENT", ctx, name)
			break
		}
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.blobv(data); return 1 })
		l.logf("tache bdStorage.getPublisherFile ctx=%q file=%q -> %s (%d octets)", ctx, name, path, len(data))

	case service == svcTitleUtilities && task == 9:
		reply = l.onGetUserNames(task, r)

	case service == 29 && (task == 1 || task == 4):
		if reply = l.onUserData(task, r); reply == nil {
			reply = taskReply(task, 0, nil)
		}

	case service == svcMatchMaking:
		if reply = l.onMatchMaking(task, r); reply == nil {
			reply = taskReply(task, 0, nil)
			l.logf("tache matchmaking NON GEREE tache=%d args:\n%s", task, hex.Dump(payload))
		}

	default:
		// Succes sans resultat : benin pour les requetes de liste, et une
		// erreur risque de marquer le service indisponible cote jeu.
		reply = taskReply(task, 0, nil)
		l.logf("tache NON GEREE service=%d tache=%d (succes vide) args:\n%s", service, task, hex.Dump(payload))
	}

	if err := l.sendEncrypted(innerTaskReply, reply); err != nil {
		l.logf("envoi reponse: %v", err)
	}
}

// publisherFile cherche le fichier dans le dossier des fichiers publisher, sans tenir
// compte de la casse (le jeu demande « Config.txt », le generateur ecrit
// « config.txt »).
func (l *lobbyConn) publisherFile(name string) ([]byte, string) {
	base := filepath.Base(name)
	entries, err := os.ReadDir(l.pubDir)
	if err != nil {
		return nil, ""
	}
	candidates := []string{base}
	if alias, ok := pubFileAliases[strings.ToLower(base)]; ok {
		candidates = append(candidates, alias)
	}
	for _, want := range candidates {
		for _, e := range entries {
			if strings.EqualFold(e.Name(), want) {
				p := filepath.Join(l.pubDir, e.Name())
				if b, err := os.ReadFile(p); err == nil {
					return b, p
				}
			}
		}
	}
	return nil, ""
}

// Noms demandes par le jeu -> noms des fichiers generes.
// Vu en direct : Config.txt, Seasons.txt, Blacklist.txt.
var pubFileAliases = map[string]string{
	"seasons.txt":   "seasons_config.txt",
	"blacklist.txt": "blacklist_config.txt",
}
