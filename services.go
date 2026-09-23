package main

// Lobby remote tasks (bdRemoteTask), read from the NSO.
//
// Client request (encrypted message, inner type 0x86): a TYPED bdByteBuffer
// whose first byte — the service identifier — is written raw:
//
//	u8 service | 03 u8 task | typed arguments
//
// Reply (inner type 0x01, pump 0xBE96E0 -> 0xBE3FE0 -> 0xC00370):
//
//	0A u64 transaction | 08 u32 error | 03 u8 | 08 u32 numResults | 08 u32 total | results
//
// Error 0 yields results, 200 leaves the task pending, any other value makes
// it fail cleanly. Tasks are served in order (the first pending one takes the
// reply): there is no identifier to match.
//
// Type tags (readers 0xBD90E0/0xBD9170/0xBDA100/0xBDA220/0xBDA4C0):
// 01 bool, 03 u8, 08 u32, 0A u64, 10 NUL-terminated string, 13 blob (08 u32 + bytes).

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

	// Numbers AND result types read from the bdTaskParams of each bdAsyncMatchMaking
	// function. bdUInt64Result::deserialize = readUInt64, bdStringResult = readString,
	// bdBoolResult = readBool, bdLobbyDocuments = two strings.
	taskSetPlayerInfo      = 2  // no result
	taskGetPlayerToken     = 3  // bdUInt64Result
	taskQoSHostsReply      = 4  // no result
	taskGetMMStatus        = 5  // bdStringResult
	taskInitMatchMaking    = 6  // bdStringResult
	taskStartMatchMaking   = 7  // bdStringResult
	taskLobbyDisbanded     = 10 // no result
	taskGetLobbyDocuments  = 13 // bdLobbyDocuments
	taskAckExpectGame      = 14 // no result
	taskSyncLobbyDocuments = 15 // bdBoolResult
	taskInitiateDCQoS      = 17 // bdStringResult
	taskStartSearch        = 18 // bdStringResult

	taskGetPublisherFile = 21
	taskGetServerTime    = 6
)

// Error returned for an unhandled task: nonzero and different from 200, so
// the task fails instead of timing out (a timeout drops the lobby).
const errUnhandled = 1

var transactions atomic.Uint64

// bdWriter writes a typed bdByteBuffer.
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

// raw64 writes 8 bytes with NO tag (bdByteBuffer::read, not readUInt64).
func (w *bdWriter) raw64(v uint64) {
	w.b = binary.LittleEndian.AppendUint64(w.b, v)
}
func (w *bdWriter) blobv(p []byte) {
	w.b = append(w.b, tagBlob)
	w.u32(uint32(len(p)))
	w.b = append(w.b, p...)
}

// structv writes a bdStructBuffer (protobuf), as the client sends it.
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

// httpProxyReply returns a bdHTTPProxyResponse: { 1: HTTP code, 2: HTTP code, 3: body }.
// The code is written TWICE because two readers coexist:
// bdHTTPProxyResponse::deserializeStatusCode reads field 2, but the REST path taken by
// bdABTesting (bdRESTInternalResponse::deserialize -> bdRESTResponseMessage::initFromBuffer ->
// bdRESTLSGResponseMessageDeserializer::deserialize) reads field 1. An unknown field is ignored,
// so both can coexist; without field 1 initFromBuffer fails and sets error 4.
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

// bdReader reads a typed bdByteBuffer (request arguments).
type bdReader struct {
	b   []byte
	off int
}

var errShort = errors.New("buffer too short")

func (r *bdReader) tag(want byte) error {
	if r.off >= len(r.b) {
		return errShort
	}
	if r.b[r.off] != want {
		return errors.New("unexpected tag")
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

// sendPush sends a pushed message (inner type 0x02). bdLobbyService::handlePushMessage reads a
// tagged u32: the bdEventType, resolved in the table of registered handlers.
func (l *lobbyConn) sendPush(event uint32, build func(w *bdWriter)) error {
	w := &bdWriter{}
	w.u32(event)
	if build != nil {
		build(w)
	}
	return l.sendEncrypted(innerPush, w.b)
}

// taskReply builds the payload of a task reply.
//
// bdRemoteTaskManager::handleTaskReply reads this header RAW (bdByteBuffer::read(dst, 8)), but
// handleLSGTaskReply, the path our tasks take, reads no id: it simply takes the first pending
// task. The 0A tag is therefore kept (original form, verified).
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
	// 0xC005B0 only reads the total if there is at least one result.
	w.u32(n)
	if n > 0 {
		w.u32(n)
		w.b = append(w.b, body.b...)
	}
	return w.b
}

// onTask handles a decrypted request. payload = bytes after the inner type.
func (l *lobbyConn) onTask(payload []byte) {
	if len(payload) < 3 {
		l.logf("task: payload too short %X", payload)
		return
	}
	service := payload[0]
	r := &bdReader{b: payload, off: 1}
	task, err := r.u8()
	if err != nil {
		// Never leave a task unanswered: the game waits for it until the timeout, which
		// fails the whole LSG login sequence and restarts a full reconnection.
		if len(payload) >= 3 {
			task = payload[2]
		}
		l.logf("task: service %d task id unreadable (%v), empty success task=%d: %X",
			service, err, task, payload)
		if err := l.sendEncrypted(innerTaskReply, taskReply(task, 0, nil)); err != nil {
			l.logf("send reply: %v", err)
		}
		return
	}
	noteTask(l, service, task)

	var reply []byte
	switch {
	case service == svcTitleUtilities && task == taskGetServerTime:
		now := uint32(time.Now().Unix())
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.u32(now); return 1 })
		l.logf("task bdTitleUtilities.getServerTime -> %d", now)

	case service == svcStorage && task == taskGetPublisherFile:
		ctx, err1 := r.str()
		name, err2 := r.str()
		if err1 != nil || err2 != nil {
			l.logf("getPublisherFile: unreadable arguments: %X", payload)
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		data, path := l.publisherFile(name)
		if data == nil {
			// No result rather than an error: the game treats a "missing
			// file" as a normal case.
			reply = taskReply(task, 0, nil)
			l.logf("task bdStorage.getPublisherFile ctx=%q file=%q -> MISSING", ctx, name)
			break
		}
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.blobv(data); return 1 })
		l.logf("task bdStorage.getPublisherFile ctx=%q file=%q -> %s (%d bytes)", ctx, name, path, len(data))

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
		// bdAsyncMatchMaking::setPlayerInfo(const char*, u32): the game uploads its
		// listen_server{local_address,security_id,security_key} here. The function has no
		// result, so an empty success is the right answer. It is the FIRST task of the
		// matchmaking registration: failing it brings down everything after it.
		if doc, err := r.str(); err == nil && doc != "" {
			l.lobbyDoc = doc
		}
		reply = taskReply(task, 0, nil)
		l.logf("task bdAsyncMatchMaking.setPlayerInfo -> success (%d bytes)", len(l.lobbyDoc))

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
		// getMatchMakingPlayerToken -> bdUInt64Result (readUInt64). This is the
		// requestPlayerToken step of CNetworkTaskAsyncMatchMakingRegistration: without a token the
		// registration state machine cannot advance.
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.u64(l.pid()); return 1 })
		l.logf("task bdAsyncMatchMaking.getMatchMakingPlayerToken -> %d", l.pid())

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
			l.logf("CTR search id=%d registered; seats=%d filter=%s notifications=%d",
				search.id, search.seats, search.filterID, len(notices))
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
		// All of these return a bdStringResult (readString).
		id := strconv.FormatUint(l.pid(), 10)
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.strv(id); return 1 })
		l.logf("task bdAsyncMatchMaking task=%d -> string %q", task, id)

	case service == svcAsyncMatchMaking &&
		(task == taskQoSHostsReply || task == taskAckExpectGame):
		// No result.
		reply = taskReply(task, 0, nil)
		l.logf("task bdAsyncMatchMaking task=%d -> empty success", task)

	case service == svcAsyncMatchMaking && task == taskGetLobbyDocuments:
		id, err := r.u64()
		h, b, ok := ctrMM.documents(l, id)
		if err != nil || !ok {
			reply = taskReply(task, errUnhandled, nil)
			break
		}
		reply = taskReply(task, 0, func(w *bdWriter) uint32 { w.strv(h); w.strv(b); return 1 })

	case service == svcHTTPProxy && task == 1:
		// bdABTesting::enroll. The body is parsed as JSON: expiresIn, ABToken,
		// enrollments. No experiment is active, but the reply must be well
		// formed, or the task fails on every connection.
		// ABToken NON-EMPTY: bdABTestingEnrollResponse reads it with getString, and an empty string
		// fails deserialization -> bdRemoteTask error 4 -> the login sequence fails.
		body := []byte(`{"expiresIn":3600,"ABToken":"nextendo","enrollments":[]}`)
		reply = httpProxyReply(task, 200, body)
		l.logf("task bdABTesting.enroll -> 200 (%d bytes)", len(body))

	case service == svcAchievements:
		reply = l.onAchievements(task, payload)

	case service == svcMarketplace:
		if reply = l.onMarketplace(task, payload); reply == nil {
			reply = taskReply(task, 0, nil)
			l.logf("task marketplace UNHANDLED task=%d args:\n%s", task, hex.Dump(payload))
		}

	case service == svcMatchMaking:
		if reply = l.onMatchMaking(task, r); reply == nil {
			reply = taskReply(task, 0, nil)
			l.logf("task matchmaking UNHANDLED task=%d args:\n%s", task, hex.Dump(payload))
		}

	default:
		// Success with no result: harmless for list requests, whereas an
		// error may mark the service unavailable on the game side.
		reply = taskReply(task, 0, nil)
		l.logf("task UNHANDLED service=%d task=%d (empty success) args:\n%s", service, task, hex.Dump(payload))
	}

	if pending := l.afterReply; pending != nil {
		l.afterReply = nil
		defer pending()
	}
	if err := l.sendEncrypted(innerTaskReply, reply); err != nil {
		l.logf("send reply: %v", err)
	}
}

// publisherFile looks the file up in the pubfiles folder, ignoring case (the
// game asks for "Config.txt", the generator writes "config.txt").
func (l *lobbyConn) publisherFile(name string) ([]byte, string) {
	if data, path, ok := riftFile(name, time.Now()); ok {
		return data, path
	}
	if data, path, ok := generatedPubfile(name, time.Now()); ok {
		return data, path
	}
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

// Real names requested by the game -> names written by the generator (taken
// from d3hack's cache). Seen live: Config.txt, Seasons.txt, Blacklist.txt.
var pubFileAliases = map[string]string{
	"seasons.txt":   "seasons_config.txt",
	"blacklist.txt": "blacklist_config.txt",
}
