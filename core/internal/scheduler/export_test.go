// file: core/internal/scheduler/export_test.go
// version: 1.0.0
// guid: e5523b33-8f1d-47fc-b839-737f6028af6e
// last-edited: 2026-11-11

package scheduler

import (
	"encoding/binary"
	"testing"
	"time"
)

// GothamZone is a fake zone with US-style DST rules and no tzdata behind
// it: GST (UTC-5) and GDT (UTC-4), spring forward at 02:00 on the second
// Sunday of March, fall back at 02:00 on the first Sunday of November. It is
// built from a TZif v2 file with no transitions and only the POSIX TZ footer,
// so every instant takes the footer's rule and the zone never depends on the
// host's zone database.
func GothamZone(t testing.TB) *time.Location {
	t.Helper()
	const footer = "GST5GDT,M3.2.0,M11.1.0"
	header := func(version byte) []byte {
		h := make([]byte, 44)
		copy(h, "TZif")
		h[4] = version
		// isutcnt, isstdcnt, leapcnt, timecnt, typecnt, charcnt
		for i, n := range []uint32{0, 0, 0, 0, 1, 4} {
			binary.BigEndian.PutUint32(h[20+4*i:], n)
		}
		return h
	}
	body := func() []byte {
		b := make([]byte, 6)
		off := int32(-5 * 3600)
		binary.BigEndian.PutUint32(b, uint32(off))
		return append(b, "GST\x00"...)
	}
	var data []byte
	data = append(data, header('2')...)
	data = append(data, body()...)
	data = append(data, header('2')...)
	data = append(data, body()...)
	data = append(data, "\n"+footer+"\n"...)
	loc, err := time.LoadLocationFromTZData("Gotham", data)
	if err != nil {
		t.Fatalf("GothamZone: %v", err)
	}
	return loc
}
