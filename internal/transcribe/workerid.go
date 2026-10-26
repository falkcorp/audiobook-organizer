// file: internal/transcribe/workerid.go
// version: 1.1.0
// guid: 41850903-354a-4f35-819b-d4acdeea1adb
// last-edited: 2026-10-26

package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// Worker identity.
//
// A file's decoder-timeout result turns permanent only when two DIFFERENT
// workers fail on it, so one sick machine can never condemn a file. "Worker"
// means machine, not endpoint: production reaches its workers through SSH
// tunnels, so every endpoint is a 127.0.0.1 port, and two of those ports can be
// two server processes on the same box. A URL says nothing about which machine
// answers it.
//
// The worker reports machine_id in /health (an opaque hash it computes from a
// stable machine id, never a hostname). The identity stored on a file row and
// returned in API responses is built from that, or, for a worker too old to
// report it or one with no stable machine id yet, from a hash of the URL. It is never the URL itself: the row is
// served to API clients, and endpoint addresses are internal.

// WorkerSeparator joins the identities of several workers in one value: a
// dispatcher condemnation names every machine the file was stuck on alone.
// The database splits on the same string (database.IntroTimeoutWorkerSeparator).
const WorkerSeparator = " + "

// shortHash is the first 12 hex digits of sha256(s).
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// cleanMachineID keeps a reported machine_id to a short run of [0-9a-z]. The
// worker sends a hex hash; anything else is hashed, so a misbehaving worker
// cannot put arbitrary text (or the separator) into a stored row.
func cleanMachineID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ""
	}
	if len(id) <= 32 && !strings.ContainsFunc(id, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'z')
	}) {
		return id
	}
	return shortHash(id)
}

// machineKey is the identity the dispatcher uses to tell workers apart inside
// one dispatch. Always non-empty. An endpoint whose /health could not be read
// gets a key of its own that is never stored (see workerID), so two such
// endpoints are not assumed to be one machine, and neither is assumed to be a
// new one.
func (g gatedEndpoint) machineKey() string {
	if id := g.workerID(); id != "" {
		return id
	}
	return "?" + shortHash(g.Endpoint.URL)
}

// workerID is the identity stored with a strike: "m:" + the reported
// machine_id, or "u:" + a hash of the URL for a worker too old to report one.
// Empty when /health could not be read: the store then treats a later strike as
// coming from the same worker as the previous one, which is the safe direction
// (an unknown worker never earns a file its second strike). A URL fallback here
// would give one machine a new identity on any night its /health was flaky.
func (g gatedEndpoint) workerID() string {
	if !g.Probed {
		return ""
	}
	if id := cleanMachineID(g.Health.MachineID); id != "" {
		return "m:" + id
	}
	return "u:" + shortHash(g.Endpoint.URL)
}

// splitWorkers is the set of identities in a stored worker value.
func splitWorkers(v string) map[string]bool {
	out := make(map[string]bool)
	for _, id := range strings.Split(v, WorkerSeparator) {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// joinWorkers is the stored form of a set of machine keys: the sorted,
// distinct, storable ones (an unprobed endpoint's key is dropped), joined.
func joinWorkers(keys []string) string {
	var ids []string
	for _, k := range keys {
		if k != "" && !strings.HasPrefix(k, "?") && !slices.Contains(ids, k) {
			ids = append(ids, k)
		}
	}
	slices.Sort(ids)
	return strings.Join(ids, WorkerSeparator)
}
