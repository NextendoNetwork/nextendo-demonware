package main

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// The exact bytes Crash Team Racing sent for bdMarketplace::getBalancesV3 (service 80,
// task 245), captured from production on 2026-09-21.
const realBalancesRequest = "5003f51708110000000a0d6f6374616e655f7377697463681003"

func TestParseBalancesRequestFromRealCapture(t *testing.T) {
	payload, err := hex.DecodeString(realBalancesRequest)
	if err != nil {
		t.Fatal(err)
	}
	ctx, max := parseBalancesRequest(payload)
	if ctx != "octane_switch" {
		t.Fatalf("context = %q, want %q", ctx, "octane_switch")
	}
	if max != 3 {
		t.Fatalf("maxNumResults = %d, want 3", max)
	}
}

// A reply the game cannot deserialize leaves Pit Stop with no currency, so check the
// wire shape: repeated field 1, each holding currency id, amount and kind.
func TestBalancesResponseShape(t *testing.T) {
	w := map[uint64]int64{10: 1234, 11: 0, 12: 7}
	sb := getPlayerBalancesResponse(w, 3)

	var got []struct {
		id     uint64
		amount uint64
	}
	for i := 0; i < len(sb); {
		if sb[i] != 0x0A { // field 1, length delimited
			t.Fatalf("byte %d: tag %#x, want 0x0A", i, sb[i])
		}
		i++
		ln, next, err := readVarint(sb, i)
		if err != nil {
			t.Fatal(err)
		}
		i = next
		e := sb[i : i+int(ln)]
		i += int(ln)

		var id, amount uint64
		for j := 0; j < len(e); {
			field := e[j] >> 3
			j++
			v, n, err := readVarint(e, j)
			if err != nil {
				t.Fatal(err)
			}
			j = n
			switch field {
			case 1:
				id = v
			case 2:
				amount = v
			}
		}
		got = append(got, struct {
			id     uint64
			amount uint64
		}{id, amount})
	}

	if len(got) != 3 {
		t.Fatalf("%d balances, want 3", len(got))
	}
	// Amounts are zigzag encoded; a raw varint arrives halved (10000 showed as 5000).
	if got[0].id != 10 || got[0].amount != zigzag(1234) {
		t.Fatalf("first balance = %+v, want id 1 amount zigzag(1234)=%d", got[0], zigzag(1234))
	}
	if got[2].id != 12 || got[2].amount != zigzag(7) {
		t.Fatalf("third balance = %+v, want id 3 amount zigzag(7)=%d", got[2], zigzag(7))
	}
}

// bdStructBufferTask::deserializeTaskReply reads the struct straight after the task byte:
// a result count in between makes the task fail with error 4.
func TestStructTaskReplyLayout(t *testing.T) {
	reply := structTaskReply(taskGetBalancesV3, []byte{0x0A, 0x02, 0x08, 0x01})

	if reply[0] != tagU64 {
		t.Fatalf("reply[0] = %#x, want transaction tag %#x", reply[0], tagU64)
	}
	// u64 tag + 8 bytes, u32 tag + 4 bytes, u8 tag + 1 byte.
	const taskOff = 1 + 8 + 1 + 4
	if reply[taskOff] != tagU8 || reply[taskOff+1] != taskGetBalancesV3 {
		t.Fatalf("task byte not at %d: % x", taskOff, reply[:taskOff+2])
	}
	if reply[taskOff+2] != tagStruct {
		t.Fatalf("struct must follow the task byte, got %#x", reply[taskOff+2])
	}
}

// The response is capped at 16 entries: bdGetPlayerBalancesResponse::deserialize stops
// reading past the sixteenth and the rest would be silently dropped.
func TestBalancesResponseCap(t *testing.T) {
	t.Setenv("CTR_CURRENCY_IDS", "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18")
	sb := getPlayerBalancesResponse(map[uint64]int64{}, 0)
	n := 0
	for i := 0; i < len(sb); {
		i++
		ln, next, err := readVarint(sb, i)
		if err != nil {
			t.Fatal(err)
		}
		i = next + int(ln)
		n++
	}
	if n != 3 {
		t.Fatalf("%d entries, want the three catalog currencies", n)
	}
}

func TestStructPayloadRejectsJunk(t *testing.T) {
	if got := structPayload([]byte{0x50, 0x03, 0xf5}); got != nil {
		t.Fatalf("short buffer returned % x", got)
	}
	if got := structPayload(bytes.Repeat([]byte{0xff}, 32)); got != nil {
		t.Fatalf("junk returned % x", got)
	}
}

// The game read a raw 10000 as 5000, so the amount must survive a zigzag round trip.
func TestAmountSurvivesZigzagRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, 7, 1234, 5000, 10000} {
		enc := zigzag(v)
		dec := int64(enc>>1) ^ -int64(enc&1)
		if dec != v {
			t.Fatalf("zigzag(%d) decoded to %d", v, dec)
		}
	}
	if zigzag(10000) != 20000 {
		t.Fatalf("zigzag(10000) = %d, want 20000 so the game shows 10000", zigzag(10000))
	}
}

// Both achievements responses end on a string read and return its result, so a missing
// string field fails the whole deserialize. That is what broke the Pit Stop refresh.
func TestAchievementsRepliesCarryTheirStringField(t *testing.T) {
	l := &lobbyConn{}

	states := l.onAchievements(taskGetAchievementStates, nil)
	if !hasField(structBodyOf(t, states), 2) {
		t.Fatal("getAchievementStates reply lacks field 2, deserialize would return false")
	}

	user := l.onAchievements(taskGetUserState, nil)
	if !hasField(structBodyOf(t, user), 1) {
		t.Fatal("getUserState reply lacks field 1, CAEUserStateRefreshed never fires")
	}
}

// structBodyOf pulls the struct buffer back out of a task reply.
func structBodyOf(t *testing.T, reply []byte) []byte {
	t.Helper()
	const off = 1 + 8 + 1 + 4 // u64 transaction, u32 error
	if reply[off] != tagU8 || reply[off+2] != tagStruct {
		t.Fatalf("not a struct reply: % x", reply[:off+3])
	}
	return reply[off+3+5:]
}

func hasField(sb []byte, want byte) bool {
	for i := 0; i < len(sb); {
		field, wire := sb[i]>>3, sb[i]&7
		i++
		if wire != 2 {
			return false
		}
		ln, next, err := readVarint(sb, i)
		if err != nil {
			return false
		}
		if field == want {
			return true
		}
		i = next + int(ln)
	}
	return false
}

// A real purchaseSkus (task 154) request captured from production on 2026-09-21. The sku
// is the SECOND u32: the first is the count.
const realPurchase = "50039a106f6374616e655f73776974636800080100000008950f030008010000000801000000010008010000000a00000000000000001000080000000008000000000300080000000000"

func TestParsePurchaseSkuFromRealCapture(t *testing.T) {
	payload, err := hex.DecodeString(realPurchase)
	if err != nil {
		t.Fatal(err)
	}
	sku, ok := parsePurchaseSku(payload)
	if !ok {
		t.Fatal("could not read the sku out of a real purchase request")
	}
	if sku != 0x00030f95 {
		t.Fatalf("sku = %d (%#x), want 200085 (0x30f95)", sku, sku)
	}
}
