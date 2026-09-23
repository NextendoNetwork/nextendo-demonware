package main

// Pit Stop, the in-game shop, is bdMarketplace (service 80).
//
// CTR reads its catalog locally; the server validates purchases against the same
// catalog and stores balances and granted inventory items.
//
//	245 getBalancesV3        struct: bdGetPlayerBalancesRequest -> bdGetPlayerBalancesResponse
//	246 switchReconciliation struct: carries the Nintendo eShop receipt (a JWT)
//	165 getInventoryPaginated classic task, an array of bdMarketplaceInventory
//
// Request seen on the wire (task 245):
//
//	50 03 f5 17 08 11 00 00 00 | 0a 0d "octane_switch" 10 03
//	service task   struct len   | 1: context           2: maxNumResults
//
// bdGetPlayerBalancesResponse::deserialize loops readObject(1, ...) into a
// 0x30-byte element capped at 16 entries, so the reply is:
//
//	bdGetPlayerBalancesResponse { repeated bdMarketplaceCurrencyAmount balances = 1 }
//	bdMarketplaceCurrencyAmount { uint64 currency_id = 1; int64 amount = 2; uint64 kind = 3 }
//
// CMarketplaceGetBalancesTask::finish reads getCurrencyID/getAmount off each entry and
// looks the id up in the currency table the game loaded from its own data. An id the
// game does not know is dropped. The catalog defines currency IDs 10 through 12.

import (
	"encoding/binary"
	"os"
	"strconv"
)

const (
	svcMarketplace            = 80
	taskGetInventoryPaginated = 165
	taskPurchaseSkus          = 154
	taskGetBalancesV3         = 245
	taskSwitchReconciliation  = 246
)

// Wumpa Coins, Nitro Boost, Free Nitro Boost, in the original catalog order.
func currencyIDs() []uint64 { return []uint64{10, 11, 12} }

func startingBalance() int64 {
	if n, err := strconv.ParseInt(envOr("CTR_START_BALANCE", "0"), 10, 64); err == nil && n >= 0 {
		return n
	}
	return 0
}

func loadWallets() {
	economy.dir = envOr("CTR_WALLETS", "wallets")
	if err := os.MkdirAll(economy.dir, 0755); err != nil {
		panic(err)
	}
}

// ---- protobuf -------------------------------------------------------------

func pbVarintField(b []byte, field byte, v uint64) []byte {
	b = append(b, field<<3)
	return appendVarint(b, v)
}

// zigzag encodes a signed amount the way bdStructBufferSerializer::writeInt64 does.
// bdMarketplaceCurrencyAmount's amount is an int64, and a plain varint is read back
// halved: 10000 sent raw arrives as 10000>>1 = 5000, which is what the game displayed.
func zigzag(v int64) uint64 {
	return uint64(v<<1) ^ uint64(v>>63)
}

func pbBytesField(b []byte, field byte, p []byte) []byte {
	b = append(b, field<<3|2)
	b = appendVarint(b, uint64(len(p)))
	return append(b, p...)
}

// getPlayerBalancesResponse builds the reply for task 245.
func getPlayerBalancesResponse(w map[uint64]int64, max int) []byte {
	ids := currencyIDs()
	if max <= 0 || max > len(ids) {
		max = len(ids)
	}
	out := []byte{}
	for _, id := range ids[:max] {
		e := pbVarintField(nil, 1, id)
		e = pbVarintField(e, 2, zigzag(w[id]))
		e = pbVarintField(e, 3, 0)
		out = pbBytesField(out, 1, e)
	}
	return out
}

// structTaskReply mirrors httpProxyReply: bdStructBufferTask::deserializeTaskReply reads
// the struct immediately after the task byte, so no result-count fields may precede it.
func structTaskReply(task byte, sb []byte) []byte {
	w := &bdWriter{}
	w.u64(transactions.Add(1))
	w.u32(0)
	w.u8(task)
	w.structv(sb)
	return w.b
}

// ---- dispatch -------------------------------------------------------------

// onMarketplace handles Pit Stop requests; nil means an unsupported task.
func (l *lobbyConn) onMarketplace(task byte, payload []byte) []byte {
	switch task {
	case taskGetBalancesV3:
		ctx, max := parseBalancesRequest(payload)
		a, err := economy.snapshot(l.pid())
		if err != nil {
			l.logf("CTR economy read failed: %v", err)
			return taskReply(task, errUnhandled, nil)
		}
		w := a.Balances
		sb := getPlayerBalancesResponse(w, max)
		l.logf("CTR pitstop balances ctx=%q max=%d -> %d currencies", ctx, max, len(currencyIDs()))
		return structTaskReply(task, sb)

	case taskSwitchReconciliation:
		// The request carries an eShop receipt. Nothing was bought with real money on a
		// private server, so acknowledge with an empty response rather than failing the
		// task, which would stall Pit Stop's startup.
		l.logf("CTR pitstop switchReconciliation -> ack")
		return structTaskReply(task, nil)

	case taskGetInventoryPaginated:
		r := &bdReader{b: payload, off: 3}
		ctx, err := r.str()
		if err != nil || ctx != "octane_switch" {
			return taskReply(task, errUnhandled, nil)
		}
		page, err := r.u32()
		if err != nil {
			return taskReply(task, errUnhandled, nil)
		}
		size, err := r.u32()
		if err != nil || size == 0 || size > 4096 {
			return taskReply(task, errUnhandled, nil)
		}
		a, err := economy.snapshot(l.pid())
		if err != nil {
			l.logf("CTR economy inventory failed: %v", err)
			return taskReply(task, errUnhandled, nil)
		}
		items := inventoryPage(a, page, size)
		l.logf("CTR pitstop inventory page=%d size=%d -> %d items", page, size, len(items))
		return taskReply(task, 0, func(w *bdWriter) uint32 {
			for _, item := range items {
				writeInventoryItem(w, l.pid(), item)
			}
			return uint32(len(items))
		})
	case taskPurchaseSkus:
		skus, ok := parsePurchaseSkus(payload)
		if !ok {
			return taskReply(task, errUnhandled, nil)
		}
		debit, err := economy.purchase(l.pid(), skus)
		if err != nil {
			l.logf("CTR pitstop purchase rejected: %v", err)
			return taskReply(task, errUnhandled, nil)
		}
		l.logf("CTR pitstop purchase skus=%v debit=%d currency=10 persisted", skus, debit)
		l.afterReply = l.pushCurrentBalance
		return taskReply(task, 0, nil)

	}
	return nil
}

// parseBalancesRequest reads the bdGetPlayerBalancesRequest struct buffer that follows the
// service and task bytes: field 1 context string, field 2 maxNumResults.
func parseBalancesRequest(payload []byte) (string, int) {
	sb := structPayload(payload)
	ctx, max := "", 0
	for i := 0; i < len(sb); {
		field := sb[i] >> 3
		wire := sb[i] & 7
		i++
		switch wire {
		case 0:
			v, next, err := readVarint(sb, i)
			if err != nil {
				return ctx, max
			}
			i = next
			if field == 2 {
				max = int(v)
			}
		case 2:
			ln, next, err := readVarint(sb, i)
			if err != nil || next+int(ln) > len(sb) {
				return ctx, max
			}
			i = next
			if field == 1 {
				ctx = string(sb[i : i+int(ln)])
			}
			i += int(ln)
		default:
			return ctx, max
		}
	}
	return ctx, max
}

// structPayload returns the struct buffer inside a task argument buffer. onTask passes the
// WHOLE typed buffer, so the first three bytes are the service, the u8 tag and the task:
//
//	50 | 03 f5 | 17 | 08 11 00 00 00 | 0a 0d "octane_switch" 10 03
//	svc  tag/task  struct  u32 length   the struct itself
func structPayload(payload []byte) []byte {
	const hdr = 3 // service + tagU8 + task
	if len(payload) < hdr+6 || payload[hdr] != tagStruct || payload[hdr+1] != tagU32 {
		return nil
	}
	n := int(payload[hdr+2]) | int(payload[hdr+3])<<8 | int(payload[hdr+4])<<16 | int(payload[hdr+5])<<24
	if n < 0 || hdr+6+n > len(payload) {
		return nil
	}
	return payload[hdr+6 : hdr+6+n]
}

// ---- purchases and inventory ----------------------------------------------

// readUInt16 expects 0x06; readInt64 expects 0x09. 0x14 is a special
// INT64_MAX sentinel in writeInt64, not the tag for an eight-byte integer.
const (
	tagU16 = 0x06
	tagI64 = 0x09
)

func (w *bdWriter) u16(v uint16) {
	w.b = append(w.b, tagU16)
	w.b = binary.LittleEndian.AppendUint16(w.b, v)
}

func (w *bdWriter) i64(v int64) {
	w.b = append(w.b, tagI64)
	w.b = binary.LittleEndian.AppendUint64(w.b, uint64(v))
}

// writeInventoryItem writes one bdMarketplaceInventory. Field order and the struct offsets
// each value comes from were read from bdMarketplaceInventory::serialize; field 3 (+0x48)
// is the item id CMarketplaceInventoryItem::populateItem matches against the game's own
// catalog, so it carries the catalog ITEM id granted by the SKU.
func writeInventoryItem(w *bdWriter, owner uint64, item uint32) {
	w.u64(owner)       // bdUserAccountID owner
	w.strv("nintendo") // bdUserAccountID account type
	w.u32(item)        // catalog item ID, not SKU
	w.u32(1)           // +0x4c quantity
	w.u32(0)           // +0x50
	w.blobv(nil)       // +0x54 item data
	w.u32(0)           // +0x98
	w.i64(0)           // +0xa0 expiry, 0 = never
	w.u16(0)           // +0xa8
	w.u32(0)           // +0xac
}

// purchaseSkus encodes arrays as count followed by typed elements.
// CTR cosmetics are non-consumable: quantities other than one are rejected.
func parsePurchaseSkus(payload []byte) ([]uint32, bool) {
	r := &bdReader{b: payload, off: 3}
	ctx, err := r.str()
	if err != nil || ctx != "octane_switch" {
		return nil, false
	}
	n, err := r.u32()
	if err != nil || n == 0 || n > 255 {
		return nil, false
	}
	skus := make([]uint32, n)
	for i := range skus {
		skus[i], err = r.u32()
		if err != nil {
			return nil, false
		}
	}
	qn, err := r.u32()
	if err != nil || qn != n {
		return nil, false
	}
	for range skus {
		q, e := r.u32()
		if e != nil || q != 1 {
			return nil, false
		}
	}
	return skus, true
}
func parsePurchaseSku(payload []byte) (uint32, bool) {
	ids, ok := parsePurchaseSkus(payload)
	if !ok || len(ids) != 1 {
		return 0, false
	}
	return ids[0], true
}
