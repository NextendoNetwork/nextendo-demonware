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
	"strconv"
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
	tagStruct = 0x17

	innerTaskReply = 0x01
	innerPush      = 0x02

	svcStorage          = 10
	svcTitleUtilities   = 12
	svcAsyncMatchMaking = 145 // bdAsyncMatchMaking
	svcHTTPProxy        = 194 // bdABTesting, via bdHTTPProxyRequest/Response

	// Numeros ET types de resultat lus dans le bdTaskParams de chaque fonction de
	// bdAsyncMatchMaking. bdUInt64Result::deserialize = readUInt64, bdStringResult = readString,
	// bdBoolResult = readBool, bdLobbyDocuments = deux chaines.
	taskSetPlayerInfo      = 2  // aucun resultat
	taskGetPlayerToken     = 3  // bdUInt64Result
	taskQoSHostsReply      = 4  // aucun resultat
	taskGetMMStatus        = 5  // bdStringResult
	taskInitMatchMaking    = 6  // bdStringResult
	taskStartMatchMaking   = 7  // bdStringResult
	taskLobbyDisbanded     = 10 // aucun resultat
	taskGetLobbyDocuments  = 13 // bdLobbyDocuments
	taskAckExpectGame      = 14 // aucun resultat
	taskSyncLobbyDocuments = 15 // bdBoolResult
	taskInitiateDCQoS      = 17 // bdStringResult
	taskStartSearch        = 18 // bdStringResult

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
func (w *bdWriter) strv(s string) {
	w.b = append(w.b, tagString)
	w.b = append(w.b, s...)
	w.b = append(w.b, 0)
}
func (w *bdWriter) boolv(v bool) {
	b := byte(0)
	if v {
		b = 1
	}
	w.b = append(w.b, tagBool, b)
}
func (w *bdWriter) u32(v uint32) {
	w.b = append(w.b, tagU32)
	w.b = binary.LittleEndian.AppendUint32(w.b, v)
}
func (w *bdWriter) u64(v uint64) {
	w.b = append(w.b, tagU64)
	w.b = binary.LittleEndian.AppendUint64(w.b, v)
}

// raw64 ecrit 8 octets SANS etiquette (bdByteBuffer::read, pas readUInt64).
func (w *bdWriter) raw64(v uint64) {
	w.b = binary.LittleEndian.AppendUint64(w.b, v)
}
func (w *bdWriter) blobv(p []byte) {
	w.b = append(w.b, tagBlob)
	w.u32(uint32(len(p)))
	w.b = append(w.b, p...)
}

// structv ecrit un bdStructBuffer (protobuf), tel que le client l'envoie.
func (w *bdWriter) structv(p []byte) {
	w.b = append(w.b, tagStruct)
	w.u32(uint32(len(p)))
	w.b = append(w.b, p...)
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// httpProxyReply rend un bdHTTPProxyResponse : { 1: code HTTP, 2: code HTTP, 3: corps }.
// Le code est ecrit DEUX fois car deux lecteurs coexistent :
// bdHTTPProxyResponse::deserializeStatusCode lit le champ 2, mais la voie REST empruntee par
// bdABTesting (bdRESTInternalResponse::deserialize -> bdRESTResponseMessage::initFromBuffer ->
// bdRESTLSGResponseMessageDeserializer::deserialize) lit le champ 1. Un champ inconnu est ignore,
// donc les deux peuvent coexister ; sans le champ 1 initFromBuffer echoue et pose l'erreur 4.
func httpProxyReply(task byte, status uint32, body []byte) []byte {
	sb := appendVarint([]byte{0x08}, uint64(status))
	sb = appendVarint(append(sb, 0x10), uint64(status))
	sb = append(sb, 0x1A)
	sb = appendVarint(sb, uint64(len(body)))
	sb = append(sb, body...)
	// bdRemoteTask::handleTaskReply consumes transaction, error and task.
	// bdStructBufferTask::deserializeTaskReply then reads StructStart directly;
	// ordinary result-count fields make it fail with BD_HANDLE_TASK_FAILED (4).
	w := &bdWriter{}
	w.u64(transactions.Add(1))
	w.u32(0)
	w.u8(task)
	w.structv(sb)
	return w.b
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

// sendPush emet un message pousse (type interne 0x02). bdLobbyService::handlePushMessage lit un
// u32 etiquete : c'est le bdEventType, resolu dans la table des gestionnaires enregistres.
func (l *lobbyConn) sendPush(event uint32, build func(w *bdWriter)) error {
	w := &bdWriter{}
	w.u32(event)
	if build != nil {
		build(w)
	}
	return l.sendEncrypted(innerPush, w.b)
}

// taskReply construit la charge d'une reponse de tache.
//
// bdRemoteTaskManager::handleTaskReply lit cet en-tete en BRUT (bdByteBuffer::read(dst, 8)), mais
// handleLSGTaskReply — la voie de nos taches — ne lit aucun identifiant : il prend simplement la
// premiere tache en attente. L'etiquette 0A est donc conservee (forme d'origine, verifiee).
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
		// Ne jamais laisser une tache sans reponse : le jeu l'attend jusqu'au timeout, ce qui
		// fait echouer toute la sequence de login LSG et relance une reconnexion complete.
		if len(payload) >= 3 {
			task = payload[2]
		}
		l.logf("tache: service %d id de tache illisible (%v), succes vide tache=%d: %X",
			service, err, task, payload)
		if err := l.sendEncrypted(innerTaskReply, taskReply(task, 0, nil)); err != nil {
			l.logf("envoi reponse: %v", err)
		}
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

	case service == 138:
		ctx, err := r.str()
		if err != nil || ctx == "" {
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		if task == mmCreateSession || task == mmUpdateSession || task == mmDeleteSession || task == mmUpdatePlayers || task == mmRequestSessionID || task == mmInitializeSession {
			reply = l.onMatchMakingContext(task, r, "ctr:"+ctx)
		} else if task == 14 {
			reply = l.onFriendSessions(task, r, "ctr:"+ctx)
		} else {
			l.logf("CTR matchmaking unimplemented task=%d context=%q", task, ctx)
			reply = taskReply(task, errUnhandled, nil)
		}
	case service == svcRichPresence:
		reply = l.onRichPresence(task, r)

	case service == svcAsyncMatchMaking && task == taskSetPlayerInfo:
		// bdAsyncMatchMaking::setPlayerInfo(const char*, u32) : le jeu televerse ici son
		// listen_server{local_address,security_id,security_key}. La fonction ne porte aucun
		// resultat, donc un succes vide est la bonne reponse. C'est la PREMIERE tache de
		// l'enregistrement matchmaking : la faire echouer fait tomber tout le reste.
		if doc, err := r.str(); err == nil && doc != "" {
			l.lobbyDoc = doc
		}
		reply = taskReply(task, 0, nil)
		l.logf("tache bdAsyncMatchMaking.setPlayerInfo -> succes (%d octets)", len(l.lobbyDoc))

	case service == svcAsyncMatchMaking && task == taskSyncLobbyDocuments:
		id, e1 := r.u64()
		version, e2 := r.u64()
		raw, e3 := r.str()
		ok := false
		if e1 == nil && e2 == nil && e3 == nil {
			var notices []ctrNotice
			ok, notices = ctrMM.syncDoc(l, id, version, raw)
			l.afterReply = func() { sendCTRNotices(notices) }
		}
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.boolv(ok); return 1 })
		l.logf("CTR sync lobby=%d version=%d accepted=%v", id, version, ok)

	case service == svcAsyncMatchMaking && task == taskGetPlayerToken:
		// getMatchMakingPlayerToken -> bdUInt64Result (readUInt64). C'est l'etape
		// requestPlayerToken de CNetworkTaskAsyncMatchMakingRegistration : sans jeton la
		// machine a etats de l'enregistrement ne peut pas avancer.
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.u64(l.pid()); return 1 })
		l.logf("tache bdAsyncMatchMaking.getMatchMakingPlayerToken -> %d", l.pid())

	case service == svcAsyncMatchMaking && task == taskStartSearch:
		raw, err := r.str()
		if err != nil {
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		search, err := parseCTRSearch(l, raw)
		if err != nil {
			l.logf("CTR search rejected: %v", err)
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		doc := "{\"mm_id\":" + strconv.FormatUint(search.id, 10) + "}"
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.strv(doc); return 1 })
		l.afterReply = func() {
			notices := ctrMM.enqueue(search)
			l.logf("CTR search id=%d registered; notifications=%d", search.id, len(notices))
			sendCTRNotices(notices)
		}

	case service == svcAsyncMatchMaking && task == 8:
		id, err := r.u64()
		ok := err == nil && ctrMM.cancel(l, id)
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.boolv(ok); return 1 })
	case service == svcAsyncMatchMaking && task == taskLobbyDisbanded:
		id, err := r.u64()
		if err == nil {
			ctrMM.leave(l, id)
		}
		reply = taskReply(task, 0, nil)

	case service == svcAsyncMatchMaking &&
		(task == taskInitMatchMaking || task == taskStartMatchMaking ||
			task == taskGetMMStatus || task == taskInitiateDCQoS):
		// Toutes ces fonctions rendent un bdStringResult (readString).
		id := strconv.FormatUint(l.pid(), 10)
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.strv(id); return 1 })
		l.logf("tache bdAsyncMatchMaking tache=%d -> chaine %q", task, id)

	case service == svcAsyncMatchMaking &&
		(task == taskQoSHostsReply || task == taskAckExpectGame):
		// Sans resultat.
		reply = taskReply(task, 0, nil)
		l.logf("tache bdAsyncMatchMaking tache=%d -> succes vide", task)

	case service == svcAsyncMatchMaking && task == taskGetLobbyDocuments:
		id, err := r.u64()
		h, b, ok := ctrMM.documents(l, id)
		if err != nil || !ok {
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.strv(h); w.strv(b); return 1 })

	case service == svcHTTPProxy && task == 1:
		// bdABTesting::enroll. Le corps est relu en JSON : expiresIn, ABToken,
		// enrollments. Aucune experience active, mais la reponse doit etre
		// formee, sinon la tache echoue a chaque connexion.
		// ABToken NON VIDE : bdABTestingEnrollResponse le lit avec getString, et une chaine vide
		// fait echouer la deserialisation -> bdRemoteTask erreur 4 -> la sequence de login echoue.
		body := []byte(`{"expiresIn":3600,"ABToken":"nextendo","enrollments":[]}`)
		reply = httpProxyReply(task, 200, body)
		l.logf("tache bdABTesting.enroll -> 200 (%d octets)", len(body))

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

	if pending := l.afterReply; pending != nil {
		l.afterReply = nil
		defer pending()
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
