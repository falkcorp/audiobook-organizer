// file: internal/operations/registry/scan_standdown.go
// version: 1.0.0
// guid: 8c1f2e6a-4b73-4d5e-9a12-7f0c3d94b6e1
// last-edited: 2026-09-06

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// scanStandDownDefID is the def whose runs a stand-down quiesces and blocks.
// The library scanner is the only op that walks and rewrites files under the
// library root while other work is in flight, so it is the one an apply op must
// stand down before touching those files.
const scanStandDownDefID = "library.scan"

// scanStandDownSettingKey is the SettingsStore key under which the persisted
// marker lives. A singleton: at most one marker exists at a time (the holders
// map may carry several holders, but they share one quiesced scan).
const scanStandDownSettingKey = "registry.scan_standdown"

// standDownPersister is the narrow slice of the settings store the stand-down
// marker needs. Kept local (rather than depending on the full
// database.SettingsStore, which also carries DeleteSetting) so the registry takes
// only the two methods it uses. *database.PebbleStore satisfies it in production.
type standDownPersister interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// defaultScanStandDownLease bounds how long a single acquisition holds the gate
// without a heartbeat renewal. It matches the op watchdog's ProgressTimeout so a
// holder that dies (its goroutine gone, no more heartbeats) cannot wedge the
// scanner paused: the lease lapses, the holder is reaped, and the scan resumes.
const defaultScanStandDownLease = 5 * time.Minute

// scanStandDown is the registry's in-memory coordination state for the scan
// stand-down control. Guarded by its own mutex, independent of Registry.mu, so
// acquiring the gate never contends with the dispatcher/worker hot path except
// where they call the cheap scanStandDownActive() check.
type scanStandDown struct {
	mu sync.Mutex
	// holders maps a holder opID to its lease expiry. A holder with an expired
	// lease is reaped (and treated as absent) the next time the map is scanned.
	holders map[string]time.Time
	// scanOpID is the opID of the library.scan run this stand-down quiesced, if
	// any. Cleared when the last holder releases; used to resume that exact run.
	scanOpID string
}

// scanStandDownMarker is the persisted (JSON) form of an active stand-down. Its
// presence at boot means a holder died mid-apply: the in-memory gate is gone but
// the scan must NOT be auto-resumed over the holder's half-applied work.
type scanStandDownMarker struct {
	HolderOpID     string `json:"holder_op_id"`
	ScanOpID       string `json:"scan_op_id"`
	LeaseExpiryUTC int64  `json:"lease_expiry_unix_nano"`
}

// AcquireScanStandDown makes the library scanner stand down for the caller.
//
// It (1) registers the caller as a gate holder with a lease and persists a
// marker, (2) cooperatively quiesces any running library.scan and BLOCKS until
// that scan's goroutine has actually parked (or ctx/lease deadline), and (3)
// returns a release closure. While any holder is registered, the dispatcher will
// not start a new library.scan (Gate 3.5) and a scan already claimed-but-not-yet
// -running is dropped as interrupted_quiesced at pickup.
//
// The returned release closure is idempotent. Call it (deferred) when the apply
// work is done: the last holder to release clears the marker and re-queues the
// quiesced scan from its checkpoint.
//
// Lease semantics (fail-safe): a holder must renew via RenewScanStandDown on its
// progress heartbeat and must treat ScanStandDownValid()==false as a HARD ABORT
// of its remaining writes — a lapsed lease resumes the scanner, so continuing to
// write past it means concurrent writers with no gate.
func (r *Registry) AcquireScanStandDown(ctx context.Context, holderOpID, reason string) (func(), error) {
	if holderOpID == "" {
		return nil, fmt.Errorf("scan stand-down: empty holder opID")
	}
	lease := r.leaseTTL()
	expiry := time.Now().Add(lease)

	// 1. Register the holder and persist the marker BEFORE quiescing, so a reboot
	//    during the quiesce is visible to the boot resume sweep.
	r.scanGate.mu.Lock()
	r.scanGate.holders[holderOpID] = expiry
	r.scanGate.mu.Unlock()
	r.persistScanStandDown(holderOpID, "", expiry)

	r.logger.Info("registry: scan stand-down acquired",
		"holder_op_id", holderOpID, "reason", reason, "lease", lease)

	// 2. Quiesce a running library.scan, if one is in flight.
	scanOpID, scanHandle := r.findRunningScan()
	if scanHandle != nil {
		scanHandle.quiescing.Store(true)
		scanHandle.cancelIfActive()

		r.scanGate.mu.Lock()
		r.scanGate.scanOpID = scanOpID
		r.scanGate.mu.Unlock()
		r.persistScanStandDown(holderOpID, scanOpID, expiry)

		r.logger.Info("registry: scan stand-down quiescing running scan; waiting for it to park",
			"holder_op_id", holderOpID, "scan_op_id", scanOpID)

		// 3. Wait for the scan goroutine to truly exit (parked chan closes at the
		//    real goroutine exit, incl. panic recovery). Bounded by ctx and lease.
		select {
		case <-scanHandle.parked:
			r.logger.Info("registry: scan parked for stand-down",
				"holder_op_id", holderOpID, "scan_op_id", scanOpID)
		case <-ctx.Done():
			r.releaseScanStandDown(holderOpID)
			return nil, fmt.Errorf("scan stand-down: canceled while waiting for scan to park: %w", ctx.Err())
		case <-time.After(lease):
			r.releaseScanStandDown(holderOpID)
			return nil, fmt.Errorf("scan stand-down: scan did not park within %s", lease)
		}
	}

	var once sync.Once
	return func() { once.Do(func() { r.releaseScanStandDown(holderOpID) }) }, nil
}

// leaseTTL returns the configured stand-down lease, or the default when unset.
func (r *Registry) leaseTTL() time.Duration {
	if r.scanStandDownLease > 0 {
		return r.scanStandDownLease
	}
	return defaultScanStandDownLease
}

// RenewScanStandDown extends the caller's lease. Callers renew on their progress
// heartbeat (the same signal the op watchdog reads). Returns false if the caller
// is no longer a registered holder (lease already reaped / released) — the caller
// must then abort its remaining writes.
func (r *Registry) RenewScanStandDown(holderOpID string) bool {
	r.scanGate.mu.Lock()
	defer r.scanGate.mu.Unlock()
	if _, ok := r.scanGate.holders[holderOpID]; !ok {
		return false
	}
	expiry := time.Now().Add(r.leaseTTL())
	r.scanGate.holders[holderOpID] = expiry
	// Refresh the persisted lease so a reboot after a renewal carries the newer
	// expiry (best-effort; marker persistence is a hardening, not the hot path).
	r.persistScanStandDown(holderOpID, r.scanGate.scanOpID, expiry)
	return true
}

// ScanStandDownValid reports whether the caller still holds a live (unexpired)
// gate. A holder MUST check this before each write batch and abort if false: a
// lapsed lease means the scanner has been (or is about to be) resumed.
func (r *Registry) ScanStandDownValid(holderOpID string) bool {
	r.scanGate.mu.Lock()
	defer r.scanGate.mu.Unlock()
	expiry, ok := r.scanGate.holders[holderOpID]
	return ok && time.Now().Before(expiry)
}

// releaseScanStandDown removes a holder and, when it was the last live holder,
// clears the persisted marker and re-queues the quiesced scan from its
// checkpoint. Idempotent for a given holder via the AcquireScanStandDown once.
func (r *Registry) releaseScanStandDown(holderOpID string) {
	r.scanGate.mu.Lock()
	delete(r.scanGate.holders, holderOpID)
	live := r.liveHoldersLocked()
	scanOpID := r.scanGate.scanOpID
	if live == 0 {
		r.scanGate.scanOpID = ""
	}
	r.scanGate.mu.Unlock()

	r.logger.Info("registry: scan stand-down released",
		"holder_op_id", holderOpID, "remaining_holders", live)

	if live > 0 {
		// Other holders remain; keep the scanner down.
		return
	}
	// Last holder out: clear the marker and resume the scan we quiesced.
	r.clearScanStandDown()
	if scanOpID != "" {
		r.resumeQuiescedOp(scanOpID)
	}
	r.pingDispatch()
}

// scanStandDownActive reports whether any holder currently holds the gate,
// reaping expired leases as a side effect. Cheap enough for the dispatcher to
// call per candidate (it is only taken for library.scan rows in practice).
func (r *Registry) scanStandDownActive() bool {
	r.scanGate.mu.Lock()
	defer r.scanGate.mu.Unlock()
	return r.liveHoldersLocked() > 0
}

// liveHoldersLocked returns the number of holders with unexpired leases, reaping
// expired ones. Caller must hold r.scanGate.mu.
func (r *Registry) liveHoldersLocked() int {
	now := time.Now()
	n := 0
	for holder, expiry := range r.scanGate.holders {
		if now.After(expiry) {
			delete(r.scanGate.holders, holder)
			r.logger.Warn("registry: scan stand-down lease expired; reaping holder",
				"holder_op_id", holder)
			continue
		}
		n++
	}
	return n
}

// findRunningScan returns the opID and handle of a currently-RUNNING library.scan
// (full handle with a live cancel), or ("", nil). Dispatcher stub handles (nil
// cancel) are skipped: a scan claimed-but-not-yet-running has no goroutine to
// wait on and is instead dropped at pickup by the executeRun gate check.
func (r *Registry) findRunningScan() (string, *runHandle) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for opID, h := range r.running {
		if h != nil && h.defID == scanStandDownDefID && h.cancel != nil {
			return opID, h
		}
	}
	return "", nil
}

// --- persistence (best-effort; nil store => in-memory only) ---

// persistScanStandDown, clearScanStandDown and readScanStandDownMarker read
// r.scanStandDownStore WITHOUT taking r.mu. This is deliberate and required: the
// dispatcher claim block calls scanStandDownActive() (which takes scanGate.mu)
// while holding r.mu, and RenewScanStandDown calls persistScanStandDown while
// holding scanGate.mu — taking r.mu here too would be an AB-BA inversion. The
// lockless read is safe because scanStandDownStore is set once via
// SetScanStandDownStore BEFORE Start() (same set-once-before-Start contract as
// depBookStore/runContextDecorator); Start()'s goroutine launch is the
// happens-before edge that publishes it.
func (r *Registry) persistScanStandDown(holderOpID, scanOpID string, expiry time.Time) {
	ss := r.scanStandDownStore
	if ss == nil {
		return
	}
	blob, err := json.Marshal(scanStandDownMarker{
		HolderOpID:     holderOpID,
		ScanOpID:       scanOpID,
		LeaseExpiryUTC: expiry.UnixNano(),
	})
	if err != nil {
		r.logger.Warn("registry: failed to marshal scan stand-down marker", "error", err)
		return
	}
	if err := ss.SetSetting(scanStandDownSettingKey, string(blob), "json", false); err != nil {
		r.logger.Warn("registry: failed to persist scan stand-down marker", "error", err)
	}
}

func (r *Registry) clearScanStandDown() {
	ss := r.scanStandDownStore // lockless: see persistScanStandDown's note
	if ss == nil {
		return
	}
	// Empty value is our cleared sentinel (readScanStandDownMarker treats a blank
	// or unparseable value as absent), avoiding a dependency on a DeleteSetting.
	if err := ss.SetSetting(scanStandDownSettingKey, "", "json", false); err != nil {
		r.logger.Warn("registry: failed to clear scan stand-down marker", "error", err)
	}
}

// readScanStandDownMarker returns the persisted marker and true if one is present
// and parseable with a non-empty ScanOpID. A blank/absent/garbage value reads as
// (zero, false).
func (r *Registry) readScanStandDownMarker() (scanStandDownMarker, bool) {
	ss := r.scanStandDownStore // lockless: see persistScanStandDown's note
	if ss == nil {
		return scanStandDownMarker{}, false
	}
	row, err := ss.GetSetting(scanStandDownSettingKey)
	if err != nil || row == nil || row.Value == "" {
		return scanStandDownMarker{}, false
	}
	var m scanStandDownMarker
	if err := json.Unmarshal([]byte(row.Value), &m); err != nil || m.ScanOpID == "" {
		return scanStandDownMarker{}, false
	}
	return m, true
}
