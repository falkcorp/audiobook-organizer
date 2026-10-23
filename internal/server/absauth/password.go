// file: internal/server/absauth/password.go
// version: 1.6.0
// guid: 7b26c8f1-3e40-49da-95b7-0c48f2a6d371
// last-edited: 2026-10-23

package absauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Password-hash algorithm identifiers as stored on database.User.PasswordHashAlgo.
const (
	// AlgoArgon2id is what every NEW or rehashed password uses (spec §3.5).
	AlgoArgon2id = "argon2id"
	// AlgoBcrypt is what existing users have. Verification keeps working and a
	// successful login triggers a transparent rehash, so the migration needs no
	// flag day and no password reset.
	AlgoBcrypt = "bcrypt"
	// AlgoOAuth marks a user with NO password credential — created by the OAuth
	// login or by ABS Cloudflare-Access JIT provisioning. Such a user must never
	// authenticate by password.
	AlgoOAuth = "oauth"
)

// argon2id parameters. m=64 MiB / t=3 / p=2 is the OWASP-recommended baseline and
// costs a few tens of milliseconds per login, which is acceptable because logins are
// rare (access tokens last 30 days).
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
	argonVersion        = argon2.Version
)

// argonSem caps how many argon2id computations run at once, process-wide. Each one
// allocates the m= cost of the hash it derives (argonMemory, 64 MiB, for every hash
// this server writes; verifyArgon2id refuses anything above argonMaxMemory), so
// unbounded concurrent logins would allocate that many times over: 100 parallel
// sign-ins by one rehashed user is about 6.4 GB. It is sized to GOMAXPROCS, not
// NumCPU, because GOMAXPROCS follows a container's CPU quota while NumCPU counts the
// host's cores. The acquire is a plain blocking one: neither HashPassword nor
// VerifyPassword takes a context.
var argonSem = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))

// argonMaxMemory is the largest m= (KiB) a stored hash may ask for. It leaves room to
// raise argonMemory later while keeping one malformed or hostile row from
// allocating gigabytes on every verify.
const argonMaxMemory = 4 * argonMemory

// argonCompute is the raw argon2id derivation. It is a variable only so the
// concurrency test can observe how many computations overlap inside argonIDKey.
var argonCompute = argon2.IDKey

// argonIDKey is argon2.IDKey run under argonSem. Every argon2id derivation in this
// package, hashing and verifying, goes through it.
func argonIDKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	argonSem <- struct{}{}
	defer func() { <-argonSem }()
	return argonCompute(password, salt, time, memory, threads, keyLen)
}

// HashPassword hashes a new password with argon2id and returns the algorithm
// identifier alongside the PHC-encoded hash.
//
// It is the ONE hashing policy: every writer of a password (web setup and change,
// invite acceptance, the generated bootstrap admin, the ABS rehash) stores the
// algo and hash it returns. TestNoDirectBcrypt bans bcrypt.GenerateFromPassword
// elsewhere.
func HashPassword(plain string) (algo string, hash string, err error) {
	if plain == "" {
		return "", "", fmt.Errorf("absauth: refusing to hash an empty password")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", "", fmt.Errorf("absauth: read password salt: %w", err)
	}
	key := argonIDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argonVersion, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return AlgoArgon2id, encoded, nil
}

// VerifyPassword checks plain against a stored hash.
//
// This is the ONE password verifier: the ABS sign-in, the web sign-in and the web
// password change all call it. The ABS sign-in rehashes bcrypt users to argon2id, so
// a caller that compares with bcrypt directly (as the web handlers once did) locks
// that user out; TestNoDirectBcrypt keeps new callers from doing so.
//
// ok reports whether the password is correct. needsRehash reports whether the caller
// should re-store the password with HashPassword after a successful login — true for
// every legacy bcrypt hash, which is how spec §3.5's "rehash-on-successful-login"
// migration to argon2id happens without a flag day.
//
// SECURITY: a user with an EMPTY stored hash never authenticates, whatever the
// algorithm or supplied password. That is the case for OAuth and
// Cloudflare-Access-JIT users, who have no password credential at all — treating an
// empty hash as "matches empty password" would make every such account trivially
// loginable. Likewise an explicit "oauth" algorithm never authenticates by password.
func VerifyPassword(algo, hash, plain string) (ok bool, needsRehash bool) {
	if strings.TrimSpace(hash) == "" {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case AlgoOAuth:
		return false, false
	case AlgoArgon2id:
		return verifyArgon2id(hash, plain), false
	case AlgoBcrypt, "":
		// An empty algo predates the column being populated; those rows are bcrypt.
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
			return false, false
		}
		return true, true
	default:
		// An unknown algorithm is a fail-closed condition, not a fallback.
		return false, false
	}
}

// verifyArgon2id parses a PHC-encoded argon2id hash and compares in constant time.
// Any malformed field is a verification failure, never a panic and never a pass.
func verifyArgon2id(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[0] != "" || parts[1] != AlgoArgon2id {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argonVersion {
		return false
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	if memory == 0 || time == 0 || threads == 0 || memory > argonMaxMemory {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argonIDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// The work a login does for a name that has no usable password.
//
// A login for a username that does not exist (or whose account has no password
// credential: an OAuth user, an empty hash) would otherwise skip the argon2id
// derivation a real account costs, and the difference in response time, tens of
// milliseconds against microseconds, tells a caller which names are real however
// carefully the status codes and the throttle charges are kept identical. So such a
// login verifies the submitted password against a fixed dummy argon2id hash built
// once with the production parameters (HashPassword), under the same argonSem as
// every other derivation, and discards the answer.
//
// The dummy is derived from a random secret nobody holds, so the verification
// cannot succeed. Legacy bcrypt rows (which still cost a bcrypt compare, not an
// argon2id derivation, until their first successful login rehashes them) are the
// one place a timing difference could remain.
var (
	dummyOnce  sync.Once
	dummyHash  string
	dummyBuilt atomic.Bool // set once dummyHash holds a usable hash; read by LoginDummyReadyForTest
)

func dummyArgon2idHash() string {
	dummyOnce.Do(func() {
		secret := make([]byte, 32)
		// crypto/rand.Read never fails (Go 1.24: it crashes the program if the system
		// source is unavailable), so there is no error path to fall back from.
		_, _ = rand.Read(secret)
		_, h, err := HashPassword(base64.RawStdEncoding.EncodeToString(secret))
		if err != nil {
			return
		}
		dummyHash = h
		dummyBuilt.Store(true)
	})
	return dummyHash
}

// WarmLoginDummy builds the dummy hash now, so the first login for an unknown
// name does not pay for building it as well. Constructors of the login handlers
// call it; it is idempotent.
func WarmLoginDummy() { _ = dummyArgon2idHash() }

// LoginDummyReadyForTest reports whether the dummy hash has been built. It is a
// test seam (the name carries ForTest, which the dead-code ratchet exempts): the
// constructor-warm tests live in other packages, so an export_test.go here cannot
// serve them. It exposes nothing but that fact.
func LoginDummyReadyForTest() bool { return dummyBuilt.Load() }

// SpendVerification does the work of one password verification against the dummy
// hash and discards the result. A login for a name that does not exist calls it.
func SpendVerification(plain string) {
	if h := dummyArgon2idHash(); h != "" {
		_ = verifyArgon2id(h, plain)
	}
}

// hasPasswordCredential reports whether a stored (algo, hash) pair is one
// VerifyPassword will do real work for.
func hasPasswordCredential(algo, hash string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case AlgoArgon2id, AlgoBcrypt, "":
		return true
	}
	return false
}

// VerifyLoginPassword is VerifyPassword for the login handlers: when the account
// has no password credential at all (no hash, an OAuth user, an unknown algorithm)
// it spends the same verification work on the dummy hash before answering false,
// so that case costs what a wrong password for a real account costs.
func VerifyLoginPassword(algo, hash, plain string) (ok bool, needsRehash bool) {
	if !hasPasswordCredential(algo, hash) {
		SpendVerification(plain)
		return false, false
	}
	return VerifyPassword(algo, hash, plain)
}
