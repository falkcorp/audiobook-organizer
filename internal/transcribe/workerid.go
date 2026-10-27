// file: internal/transcribe/workerid.go
// version: 1.2.1
// guid: 41850903-354a-4f35-819b-d4acdeea1adb
// last-edited: 2026-10-27

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

// backendMLX is the /health backend of scripts/whisper_mlx_server.py, the one
// worker server that reports machine_id (scripts/whisper_server.py reports
// "faster-whisper" and never a machine_id). It is what makes a worker able to
// have a stable machine identity at all: only such a worker that answers with
// a weak identity ("u:") has merely missed its MACHINE_ID_WAIT_S and is still
// the machine this process last saw behind its URL (see resolveMachine).
const backendMLX = "mlx"

// idCapable reports whether g is a probed worker that is able to report a
// machine_id, whether or not it did this time.
func (g gatedEndpoint) idCapable() bool { return g.Probed && g.Health.Backend == backendMLX }

// identityKey is machineKey resolved by address: the key every part of a
// dispatch groups g under (D55). It is the machine identity the gate resolved
// for g (gateEndpoints: resolveMachine), or machineKey for a value the gate never
// saw. A worker that missed its machine id this time keeps the machine its URL
// last reported, so one machine is one key whatever its worker answers as.
func (g gatedEndpoint) identityKey() string {
	if g.resolved != "" {
		return g.resolved
	}
	return g.machineKey()
}

// withIdentity is g carrying key as its identity, for a re-probe that replaces
// an endpoint's health but must not give the dispatch a second identity for it
// (see gatedEndpoint.resolved): identityKey, and so storedWorker, then answer
// with the identity the dispatch began with. A key that stands for no machine
// ("" or a "?" key, an endpoint whose /health could not be read) is not carried:
// the endpoint answered this time, and what it reports is the better identity.
func (g gatedEndpoint) withIdentity(key string) gatedEndpoint {
	if key != "" && !strings.HasPrefix(key, "?") {
		g.resolved = key
	}
	return g
}

// storedWorker is workerID resolved by address, the identity stored with a
// strike or a condemnation: empty when g was not probed (see workerID).
func (g gatedEndpoint) storedWorker() string {
	if !g.Probed {
		return ""
	}
	return g.identityKey()
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

// anyIn reports whether any of ids is in set.
func anyIn(set map[string]bool, ids []string) bool {
	return slices.ContainsFunc(ids, func(id string) bool { return set[id] })
}
