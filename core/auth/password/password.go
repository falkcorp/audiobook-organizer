// file: core/auth/password/password.go
// version: 2.1.0
// guid: 7b26c8f1-3e40-49da-95b7-0c48f2a6d371
// last-edited: 2026-11-10

// Package password is core's one password-hashing policy (05-authn §4.5),
// moved from ABO's internal/server/absauth/password.go (04 §8.1 F4): argon2id
// m=64 MiB, t=3, p=2, 16-byte salt, PHC string; bcrypt verify-only with a
// transparent rehash; an "oauth" or empty hash never authenticates.
//
// Every argon2id derivation, hashing and verifying, runs under the Hasher's
// bounded semaphore, because each one allocates the hash's m= cost: an
// unbounded login burst would exhaust memory before any throttle tripped
// (TestPasswordHash_BoundedConcurrency). HEAD held the semaphore, the cost
// and the dummy hash in package variables; core forbids package state (03
// R5), so they are fields of a Hasher, and an app builds one per process.
//
// Layer L4 with auth; it imports only the standard library and x/crypto.
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Password-hash algorithm identifiers (HEAD's database.User.PasswordHashAlgo
// values).
const (
	// AlgoArgon2id is what every new or rehashed password uses.
	AlgoArgon2id = "argon2id"
	// AlgoBcrypt is what HEAD's older users have. Verification keeps working
	// and a successful login triggers a transparent rehash.
	AlgoBcrypt = "bcrypt"
	// AlgoOAuth marks a user with no password credential (an OAuth or
	// Cloudflare-Access-provisioned account). It never authenticates by
	// password.
	AlgoOAuth = "oauth"
)

// The production argon2id parameters (OWASP baseline; HEAD's values).
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
	argonVersion        = argon2.Version
	// argonMaxMemory is the largest m= (KiB) a stored hash may ask for: room to
	// raise argonMemory later while one hostile row cannot allocate gigabytes
	// on every verify.
	argonMaxMemory = 4 * argonMemory
)

// ErrBusy is returned by the bounded calls when the semaphore was not free
// within the wait (05-authn §6: 503 busy).
var ErrBusy = errors.New("password: hashing busy")

// ErrEmpty refuses to hash an empty password.
var ErrEmpty = errors.New("password: refusing to hash an empty password")

// Options configures a Hasher. The zero value is production.
type Options struct {
	// Permits is how many argon2id derivations may run at once; 0 is
	// GOMAXPROCS (HEAD's sizing: it follows a container's CPU quota, which
	// NumCPU does not, and never exceeds NumCPU, 05-authn §4.5).
	Permits int
	// Time and MemoryKiB lower the cost Hash writes, for test binaries only
	// (New panics on them anywhere else, and archtest.NoLoweredHashCost
	// refuses them in a non-test file): a hash written at a lowered cost
	// verifies exactly as before, because verification reads the parameters
	// stored in the hash. Zero means the production value.
	Time      uint32
	MemoryKiB uint32
	// Compute replaces argon2.IDKey (the concurrency test observes how many
	// derivations overlap), for test binaries only like Time and MemoryKiB.
	// Nil is argon2.IDKey.
	Compute func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte
}

// Hasher hashes and verifies passwords under one bounded semaphore. Safe for
// concurrent use; build one per process, with New (the zero Hasher is not
// usable).
type Hasher struct {
	sem     chan struct{}
	time    uint32
	memory  uint32
	compute func(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte

	derivations, cost atomic.Uint64

	// dummy is the hash a login with no usable credential verifies against
	// (newDummy). It is set by New and never changes.
	dummy string
}

// New returns a Hasher. It panics when o sets Time, MemoryKiB or Compute
// outside a test binary (testing.Testing is false there), so an Options
// literal that reached a production binary, from any file the static guard
// could miss, still cannot lower a real password's cost.
func New(o Options) *Hasher { return newHasher(o, testing.Testing) }

// newHasher is New with the test-binary probe passed in, so a test can stand
// in for a production binary without a package-level variable.
func newHasher(o Options, isTestBinary func() bool) *Hasher {
	if (o.Time != 0 || o.MemoryKiB != 0 || o.Compute != nil) && !isTestBinary() {
		panic("password: Options.Time, MemoryKiB and Compute lower the hash cost; they are for test binaries only")
	}
	permits := o.Permits
	if permits <= 0 {
		permits = max(1, runtime.GOMAXPROCS(0))
	}
	h := &Hasher{sem: make(chan struct{}, permits), time: o.Time, memory: o.MemoryKiB, compute: o.Compute}
	if h.time == 0 {
		h.time = argonTime
	}
	if h.memory == 0 {
		h.memory = argonMemory
	}
	if h.compute == nil {
		h.compute = argon2.IDKey
	}
	h.dummy = newDummy(h.time, h.memory)
	return h
}

// Permits is the semaphore's size.
func (h *Hasher) Permits() int { return cap(h.sem) }

// Derivations reports how many argon2id derivations this Hasher has run and
// their summed cost in KiB-passes (time × memory). Both only grow: a test
// reads them around the code it measures (a failed login for an unknown name
// pays the same derivation a wrong password does).
func (h *Hasher) Derivations() (count, kibPasses uint64) {
	return h.derivations.Load(), h.cost.Load()
}

// acquire takes a permit. wait < 0 blocks until one is free or ctx ends;
// wait >= 0 fails ErrBusy after wait.
func (h *Hasher) acquire(ctx context.Context, wait time.Duration) error {
	if wait < 0 {
		select {
		case h.sem <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case h.sem <- struct{}{}:
		return nil
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-t.C:
		return ErrBusy
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

// derive runs one argon2id derivation; the caller holds a permit.
func (h *Hasher) derive(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	h.derivations.Add(1)
	h.cost.Add(uint64(time) * uint64(memory))
	return h.compute(password, salt, time, memory, threads, keyLen)
}

// Hash hashes a new password with argon2id and returns the algorithm
// identifier with the PHC-encoded hash. It blocks for a permit.
func (h *Hasher) Hash(plain string) (algo, hash string, err error) {
	return h.HashWithin(context.Background(), -1, plain)
}

// HashWithin is Hash with a bounded wait for a permit (wait < 0: unbounded).
func (h *Hasher) HashWithin(ctx context.Context, wait time.Duration, plain string) (algo, hash string, err error) {
	if plain == "" {
		return "", "", ErrEmpty
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", "", fmt.Errorf("password: read salt: %w", err)
	}
	if err := h.acquire(ctx, wait); err != nil {
		return "", "", err
	}
	key := h.derive([]byte(plain), salt, h.time, h.memory, argonThreads, argonKeyLen)
	h.release()
	return AlgoArgon2id, encodeArgon2id(h.time, h.memory, salt, key), nil
}

// encodeArgon2id is the PHC string Hash writes.
func encodeArgon2id(time, memory uint32, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argonVersion, memory, time, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}

// AlgoOf derives the algorithm of a stored hash from its format: a PHC
// argon2id string, a bcrypt string ($2a$, $2b$, $2y$), or "" for an empty
// hash. Anything else is reported as "unknown", which never verifies. Core's
// store keeps only the hash (05-authn §3.4 ImportUser), so it derives the
// algorithm here.
func AlgoOf(hash string) string {
	h := strings.TrimSpace(hash)
	switch {
	case h == "":
		return ""
	case strings.HasPrefix(h, "$argon2id$"):
		return AlgoArgon2id
	case strings.HasPrefix(h, "$2a$"), strings.HasPrefix(h, "$2b$"), strings.HasPrefix(h, "$2y$"):
		return AlgoBcrypt
	}
	return "unknown"
}

// Verify checks plain against a stored hash. ok reports a match; needsRehash
// is true after a successful bcrypt verify (the caller re-stores with Hash).
// An empty hash never authenticates, whatever the algorithm; "oauth" and an
// unknown algorithm never authenticate (fail closed). It blocks for a permit.
func (h *Hasher) Verify(algo, hash, plain string) (ok, needsRehash bool) {
	ok, needsRehash, err := h.VerifyWithin(context.Background(), -1, algo, hash, plain)
	if err != nil {
		// Unreachable (an unbounded wait on a background context cannot end
		// early), and fail closed regardless.
		return false, false
	}
	return ok, needsRehash
}

// VerifyWithin is Verify with a bounded wait for a permit; err is ErrBusy or
// the context's error, and ok is false with it.
func (h *Hasher) VerifyWithin(ctx context.Context, wait time.Duration, algo, hash, plain string) (ok, needsRehash bool, err error) {
	if strings.TrimSpace(hash) == "" {
		return false, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case AlgoOAuth:
		return false, false, nil
	case AlgoArgon2id:
		ok, err := h.verifyArgon2id(ctx, wait, hash, plain)
		return ok, false, err
	case AlgoBcrypt, "":
		// An empty algo predates HEAD's column being populated; those rows
		// are bcrypt.
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) != nil {
			return false, false, nil
		}
		return true, true, nil
	default:
		return false, false, nil
	}
}

// verifyArgon2id parses a PHC argon2id hash and compares in constant time.
// Any malformed field is a failure, never a panic and never a pass.
func (h *Hasher) verifyArgon2id(ctx context.Context, wait time.Duration, encoded, plain string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[0] != "" || parts[1] != AlgoArgon2id {
		return false, nil
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argonVersion {
		return false, nil
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, nil
	}
	if memory == 0 || time == 0 || threads == 0 || memory > argonMaxMemory {
		return false, nil
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, nil
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, nil
	}
	if err := h.acquire(ctx, wait); err != nil {
		return false, err
	}
	got := h.derive([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	h.release()
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// newDummy returns the hash a login for a name with no usable password
// verifies against, discarding the answer, so that case costs what a wrong
// password for a real account costs (equal timing, 05-authn §3.2). It carries
// this Hasher's write cost, a random salt and a random key, so verifying
// against it runs one full derivation at that cost, exactly as a wrong
// password does, and no input matches it (the key is 256 random bits that no
// derivation produced). Building it derives nothing, so it is made in New:
// there is no first login that pays for it, nothing to warm, and no build
// that could wait for a permit outside a bounded call's wait (§6).
func newDummy(time, memory uint32) string {
	salt := make([]byte, argonSaltLen)
	key := make([]byte, argonKeyLen)
	_, _ = rand.Read(salt) // crypto/rand.Read never fails (Go 1.24+)
	_, _ = rand.Read(key)
	return encodeArgon2id(time, memory, salt, key)
}

// SpendVerification does one verification's work against the dummy hash and
// discards the result. It blocks for a permit, so the work is always spent.
func (h *Hasher) SpendVerification(plain string) {
	if err := h.spendWithin(context.Background(), -1, plain); err != nil {
		// verifyArgon2id fails only when the permit wait ends, and an
		// unbounded wait on a background context never does: a broken
		// invariant, not a refusal to report.
		panic(fmt.Sprintf("password: an unbounded permit wait ended: %v", err))
	}
}

// spendWithin verifies plain against the dummy hash within wait; the only
// error is the permit wait's.
func (h *Hasher) spendWithin(ctx context.Context, wait time.Duration, plain string) error {
	_, err := h.verifyArgon2id(ctx, wait, h.dummy, plain)
	return err
}

// HasCredential reports whether a stored (algo, hash) pair is one Verify does
// real work for.
func HasCredential(algo, hash string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case AlgoArgon2id, AlgoBcrypt, "":
		return true
	}
	return false
}

// VerifyLogin is Verify for the login paths: when the account has no password
// credential (no hash, an OAuth user, an unknown algorithm) it spends the
// same work on the dummy hash before answering false.
func (h *Hasher) VerifyLogin(algo, hash, plain string) (ok, needsRehash bool) {
	ok, needsRehash, err := h.VerifyLoginWithin(context.Background(), -1, algo, hash, plain)
	if err != nil {
		// Unreachable, as in Verify; fail closed regardless.
		return false, false
	}
	return ok, needsRehash
}

// VerifyLoginWithin is VerifyLogin with a bounded wait for a permit.
func (h *Hasher) VerifyLoginWithin(ctx context.Context, wait time.Duration, algo, hash, plain string) (ok, needsRehash bool, err error) {
	if !HasCredential(algo, hash) {
		return false, false, h.spendWithin(ctx, wait, plain)
	}
	return h.VerifyWithin(ctx, wait, algo, hash, plain)
}
