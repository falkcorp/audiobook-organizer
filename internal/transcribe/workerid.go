// file: internal/transcribe/workerid.go
// version: 1.2.5
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

// reprobed is the fresh probe g of an endpoint the dispatch froze as key (a
// weak or strong identity, resolved by the gate at the start) and capable (its
// worker could report a machine id then), judged against what g reports now. A
// re-probe replaces an endpoint's health (the canary and the work should use
// what the worker says now) but never gives the dispatch a second identity for
// it, and never credits a worker's answers to a machine it is not.
//
// It returns the probe to keep, and false when the port changed hands while the
// dispatch waited, in which case the port is to be dropped from the dispatch
// like a refusal: the next dispatch's gate re-keys it (B-1 of review 6 of #4201).
//
// Only the lone endpoint (dispatchSingle) keeps what is described as kept below
// for a frozen "?" or "", a probe that read nothing, and a worker that gained a
// machine id. A pool (dispatcher regate) compares g.identityKey() with the frozen
// key first and uses reprobed only for the kind of worker: it drops all three,
// since a port is used after a wait only if its identity then equals its frozen
// one and is read (D60, D62).
//
//   - Same identity (a weak "u:" also needs the same kind of worker: a worker
//     that cannot report an id answers as "u:" + the address of the URL, the very
//     string an id-capable worker that missed its wait answers as): nothing
//     changed, the probe stands.
//   - Frozen weak, fresh strong, from an id-capable worker both times: the worker
//     gained its machine id. Whether that is the machine that was behind the URL
//     or another one that took it over cannot be told from the probe. The frozen
//     "u:" stays the key because the rest of the dispatch (benching, the machine
//     count behind the condemnation limit, steering, the canary memory) has
//     grouped the endpoint under it since the start and must not see two
//     identities for one endpoint. The machine it reports now travels with it
//     (learned), and the stored value names both ("u:" and "m:", answerWorker):
//     the store counts the "m:" as a machine like any other (a strike by it can
//     be the second strike of a file, database independentStrike) and learns it
//     when the row knew no machine (database learnsMachine), while the "u:"
//     still matches a row an earlier strike of that address wrote. The
//     machine it reports is therefore credited as itself. Only the lone-endpoint
//     path keeps such a worker (dispatchSingle): it resolves the canary memory at
//     call time, after noteCanaryProbes moved the port to the new identity, so it
//     has no frozen group to disagree with. A pool dispatch drops it
//     (regate, on any identity different from the frozen one): it cannot tell the machine that was
//     behind the URL from another that took it over, and a kept port would be
//     counted, benched and struck under two identities.
//   - A frozen key standing for no machine ("" or "?", an endpoint whose /health
//     could not be read at the start), or a probe that read nothing now: there
//     is nothing to compare, and what the dispatch froze carries (a fresh probe
//     that answered is the better identity for a frozen "?" and takes over).
//   - Anything else (one strong identity for another, a strong one for a weak one
//     after a refusal ended the URL-to-machine link, a worker that cannot report
//     an id where one could, or the reverse): another worker is behind the URL.
func (g gatedEndpoint) reprobed(key string, capable bool) (gatedEndpoint, bool) {
	if key == "" || strings.HasPrefix(key, "?") {
		return g, true
	}
	if !g.Probed {
		g.resolved = key
		return g, true
	}
	fresh := g.identityKey()
	switch {
	case fresh == key && (isStrongKey(key) || g.idCapable() == capable):
		g.resolved = key
		return g, true
	case !isStrongKey(key) && isStrongKey(fresh) && capable && g.idCapable():
		g.resolved = key
		g.learned = fresh
		return g, true
	}
	return g, false
}

// storedWorker is workerID resolved by address, the identity stored with a
// strike or a condemnation: empty when g was not probed (see workerID).
func (g gatedEndpoint) storedWorker() string {
	if !g.Probed {
		return ""
	}
	return g.identityKey()
}

// answerWorker is the identity stored with the answers a probed worker gives:
// storedWorker, joined with the machine a worker that gained its id during the
// dispatch reports now (learned, see reprobed). Empty when g was not probed.
// Only the lone-endpoint path (dispatchSingle) can hold such a worker; a pool
// drops it at the re-probe and stores storedWorker.
func (g gatedEndpoint) answerWorker() string {
	w := g.storedWorker()
	if w == "" || g.learned == "" {
		return w
	}
	return joinWorkers([]string{w, g.learned})
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
