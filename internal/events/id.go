package events

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"
)

// idCounter disambiguates two IDs minted in the same nanosecond when crypto/rand
// is unavailable, keeping IDs unique without a hard dependency on the RNG.
var idCounter atomic.Uint64

// randomID returns a time-ordered, unique event ID: a big-endian
// nanosecond-timestamp prefix (so IDs sort by creation time) followed by random
// bytes. It never returns an empty string; if crypto/rand fails it falls back
// to a process-local monotonic counter so uniqueness is still guaranteed.
func randomID() string {
	ts := time.Now().UTC().UnixNano()
	prefix := strconv.FormatInt(ts, 16)

	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: monotonic counter (still unique within the process).
		return "ev_" + prefix + "_" + strconv.FormatUint(idCounter.Add(1), 16)
	}
	return "ev_" + prefix + "_" + hex.EncodeToString(b[:])
}
