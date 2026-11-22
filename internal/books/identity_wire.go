// file: internal/books/identity_wire.go
// version: 0.1.0
// guid: 4c8e1a93-6d27-4f05-b9a1-2e7c5d03f846
// last-edited: 2026-11-21

package books

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/oidc"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// What books hands core for the identity boot, and what it builds from what core
// hands back. Everything here is reached only when identityOn() (identity_flip.go);
// with it off none of it runs and Options and Register are as they were.

const (
	// bootstrapTokenName is the bootstrap token file's name under the state
	// directory. It is HEAD's and the production runbook's: the server-bootstrap
	// skill and the sudoers rule that lets it read the token name exactly
	// "<state dir>/.bootstrap-token", so a rename is a lockout of the tooling that
	// recovers a lost admin. The directory half is bootOptions' StateDir,
	// config.DefaultSecureStateDir, which is a constant since 2026-09-09 and
	// deliberately not "beside the database".
	bootstrapTokenName = ".bootstrap-token"

	// sessionCookieName is core.auth.session.cookie_name: HEAD's cookie, so a
	// browser holding a session keeps it across the flip. Core's own default is
	// "<AppID>_session". The __Host- prefix core adds to a Secure cookie is kept
	// (core.auth.session.cookie_host_prefix stays true): a plain-HTTP LAN request
	// gets the unprefixed name, an HTTPS one the prefixed, as core decides per
	// request.
	sessionCookieName = "session_id"

	// cookieNameKey is the config path the cookie name is pinned on.
	cookieNameKey coreconfig.Path = "core.auth.session.cookie_name"
	// ownerRequiredKey is core.auth.owner.required.
	ownerRequiredKey coreconfig.Path = "core.auth.owner.required"
)

// identityAuthOptions is app.Options.Auth under the flip: the one auth value
// books sets, the bootstrap token's file name.
//
// Not set here, on purpose: ImportedKeyPrefixes ("abk_"), DefaultDisabledAllowRemote,
// OIDCCallbackBase and Options.ArchivePrefix are in the spec's list for ABO but are
// not in this slice's scope, and two of them (DefaultDisabledAllowRemote true, an
// imported key prefix that makes more keys verify) change who is admitted, so they
// are the lead's to add with their own tests, not a default carried in here.
func identityAuthOptions() coreauth.Options {
	return coreauth.Options{BootstrapTokenFile: bootstrapTokenName}
}

// identityConfigFlags is flags with the identity pins added, on a copy. The flag
// layer is the highest operator layer, so the pin holds against a stored value
// and an environment one; it also locks the key, which is the point of a pin.
func identityConfigFlags(flags map[coreconfig.Path]string) map[coreconfig.Path]string {
	out := maps.Clone(flags)
	if out == nil {
		out = map[coreconfig.Path]string{}
	}
	out[cookieNameKey] = sessionCookieName
	return out
}

// identityWiring is what books builds before core boots and finishes after the
// settings boot: the Access extension and the handles it holds. The extension is
// part of app.Options.Authenticators, which is read at app.New, before the
// settings that name the Access team and the allow-list exist; so each of them is
// a late handle that refuses until Register binds it (an unbound handle admits
// nobody, never everybody).
type identityWiring struct {
	decl *identityDecl
	// verifier is the Access verifier as a handle (cfaccess.LateVerifier),
	// shared by the extension and the throttle's visitor bucket.
	verifier *cfaccess.LateVerifier
	// resolver is core's account resolver handle (app.Options.AccountResolver),
	// which core binds at P5.
	resolver *oidc.LateResolver
	// policy is the Access admission policy, bound by Register once the
	// allow-list is read.
	policy *policyResolver
	// ext is the Access extension, the one entry of Options.Authenticators.
	ext coreauth.Extension
}

// newIdentityWiring builds the declaration check and the extension.
func newIdentityWiring(ownerEmail func() string) (*identityWiring, error) {
	decl, err := buildIdentityDecl()
	if err != nil {
		return nil, err
	}
	w := &identityWiring{decl: decl, verifier: cfaccess.NewLateVerifier(), resolver: oidc.NewLateResolver()}
	w.policy = newPolicyResolver(w.resolver)
	ext, err := cfaccess.Extension(cfaccess.ExtensionConfig{
		Verifier: w.verifier,
		Resolver: w.policy,
		// The provider is all the extension reads of this value (it never calls
		// the resolver with it: policyResolver substitutes the bound one).
		Policy:     oidc.Policy{Provider: cfaccessProvider},
		OwnerEmail: ownerEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("books: the Access extension: %w", err)
	}
	w.ext = ext
	return w, nil
}

// configOwnerEmail is the owner e-mail as HEAD read it on every request.
func configOwnerEmail() string { return config.Snapshot().OwnerEmail }

// ownerSet is whether an owner e-mail is configured. core.auth.owner.required is
// true exactly then (ruling b): the extension's OwnerSet and the bridge's
// owner-required reader both come from this one rule, so core's stage 13 and
// the handlers that ask the bridge cannot disagree.
func ownerSet(email func() string) bool {
	return strings.TrimSpace(coreauth.NormalizeEmail(email())) != ""
}

// bindAccess binds what the Access sign-in needs and Register reads from the
// settings: the admission policy, and the verifier when the team domain and the
// AUD are both configured, as HEAD built its own only then. A verifier that
// cannot be built is logged and skipped, as at HEAD: the handle then refuses
// every assertion (cfaccess.ErrNotConfigured) and no one is admitted by it.
func (w *identityWiring) bindAccess(ctx context.Context, teamDomain, aud string, pol oidc.Policy, log *slog.Logger) {
	w.policy.bind(pol)
	if strings.TrimSpace(teamDomain) == "" || strings.TrimSpace(aud) == "" {
		return
	}
	v, err := cfaccess.NewCFAccessVerifier(ctx, teamDomain, aud)
	if err != nil {
		log.Error("books: the Cloudflare Access verifier could not be built; Access sign-in is off", "err", err)
		return
	}
	if err := w.verifier.Bind(v); err != nil {
		log.Error("books: the Cloudflare Access verifier could not be bound; Access sign-in is off", "err", err)
	}
}

// atomResolver is the atom table as the two shapes that consume it: stage 13's
// httpapi.AtomResolver (resolve) and the bridge's legacyauth.Resolver. The route
// requirement is the table's Route (AllOf every scope, XFILE-5); an atom no route
// enforces (playlists.create) has a zero Route and resolves to nothing, so
// neither a route nor an in-handler check can be satisfied by it, the refusal side
// of "when in doubt".
type atomResolver struct{ rows []Atom }

func newAtomResolver() atomResolver { return atomResolver{rows: AtomTable()} }

func (t atomResolver) resolve(atom string, readSide bool) (rbac.Requirement, bool) {
	for _, a := range t.rows {
		if a.Name != atom {
			continue
		}
		req := a.Route
		if readSide && !a.ReadSide.IsZero() {
			req = a.ReadSide
		}
		return req, !req.IsZero()
	}
	return rbac.Requirement{}, false
}

// Requirement implements legacyauth.Resolver.
func (t atomResolver) Requirement(atom string, readSide bool) (rbac.Requirement, bool) {
	return t.resolve(atom, readSide)
}

// Atoms implements legacyauth.Resolver.
func (t atomResolver) Atoms() []string {
	out := make([]string, len(t.rows))
	for i, a := range t.rows {
		out[i] = a.Name
	}
	return out
}

// atomsForStage13 is Options.ResolveAtom.
func atomsForStage13() httpapi.AtomResolver { return newAtomResolver().resolve }

// errIdentityDeps is Register's refusal when the predicate is on and core did not
// hand the handles the server runs under.
var errIdentityDeps = errors.New("books: the identity flip is on but core handed no identity (Deps.Auth, AuthCompat and AuthRefresh are all required): " +
	"the boot was not given Options.IdentityBoot, so serving would leave HEAD's tables, which M3 drained, judging every request")

// errIdentityDepsWithoutFlip is the opposite refusal: core handed identity
// handles but books was built to run HEAD's own authentication.
var errIdentityDepsWithoutFlip = errors.New("books: core booted the identity flip but this build's identity predicate is off: " +
	"the mount would keep LegacyAuthGin over tables M3 drained")

// legacyUser is HEAD's user record of a core user, for the handlers that read it
// from the request context (the bridge's UserLookup). It carries what a handler
// shows and checks (identity, status, roles); the hash, the epoch and the
// version are core's and never leave it.
func legacyUser(ids coreauth.Identities) legacyauth.UserLookup {
	return func(ctx context.Context, id coreauth.UserID) (*database.User, error) {
		u, err := ids.User(ctx, id)
		if err != nil {
			return nil, err
		}
		roles := make([]string, len(u.Roles))
		for i, r := range u.Roles {
			roles[i] = string(r)
		}
		return &database.User{
			ID: string(u.ID), Username: u.Username, Email: u.Email, Roles: roles,
			Status: string(u.Status), CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
		}, nil
	}
}

// newLegacyIdentity is the wiring server.WithIdentity takes, over the handles
// core gave books. ownerRequired is core.auth.owner.required as books reads it:
// true exactly when an owner e-mail is configured (ownerSet).
func newLegacyIdentity(d module.Deps, ownerRequired func() bool) (*legacyauth.Identity, error) {
	if d.Auth == nil || d.AuthCompat == nil || d.AuthRefresh == nil {
		return nil, errIdentityDeps
	}
	b, err := legacyauth.New(newAtomResolver(), legacyUser(d.Auth), ownerRequired)
	if err != nil {
		return nil, fmt.Errorf("books: the legacy-auth bridge: %w", err)
	}
	id := &legacyauth.Identity{Bridge: b, Compat: d.AuthCompat, Client: httpapi.ClientInfoFrom, Refresh: d.AuthRefresh}
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("books: %w", err)
	}
	return id, nil
}

// refuseOwnerRequiredOff is Register's check of ruling b: an operator who set
// core.auth.owner.required to false has switched the owner gate off on an
// install whose owner is set, which HEAD never allowed (HEAD gated on "owner
// set"), so the boot refuses rather than serve with the gate off. It is a refusal
// and not an override because the key is restart-class and admin-protected, and
// silently ignoring an operator's value is worse than telling them: the fix is to
// remove the key.
func refuseOwnerRequiredOff(acc module.ConfigReads) error {
	if acc == nil {
		return nil
	}
	ks, ok := acc.Lookup(ownerRequiredKey)
	if !ok || ks.Source == coreconfig.SourceDefault {
		return nil
	}
	if strings.TrimSpace(string(ks.Effective)) == "false" {
		return fmt.Errorf("books: %s is set to false (%s); the owner gate is on whenever an owner e-mail is configured, as before the flip. Remove the key", ownerRequiredKey, ks.Source)
	}
	return nil
}

// identityFor is the identity Register builds the server under, or nil when the
// flip is off. It is where the four ways the flip can come apart are refused:
//
//   - the predicate is on and core handed no identity (errIdentityDeps, from
//     newLegacyIdentity): the boot did not get Options.IdentityBoot;
//   - core handed identity and the predicate is off (errIdentityDepsWithoutFlip):
//     the mount would keep LegacyAuthGin over tables M3 drained;
//   - an operator turned core.auth.owner.required off (refuseOwnerRequiredOff);
//   - (identityTies, at the mount) the server and the mount disagree, and (Attach,
//     identityStoreReady) the store is not at BooksIdentityLevel.
//
// With the predicate on it also binds what the Access sign-in needs from the
// settings the boot just loaded.
func (m *booksModule) identityFor(d module.Deps) (*legacyauth.Identity, error) {
	if !identityOn() {
		if d.Auth != nil || d.AuthAdmin != nil || d.AuthRefresh != nil {
			return nil, errIdentityDepsWithoutFlip
		}
		return nil, nil
	}
	if m.idw == nil {
		return nil, errors.New("books: the identity flip is on but Options built no identity wiring (Register ran over a module that never went through Options)")
	}
	if err := refuseOwnerRequiredOff(d.Config); err != nil {
		return nil, err
	}
	id, err := newLegacyIdentity(d, func() bool { return ownerSet(configOwnerEmail) })
	if err != nil {
		return nil, err
	}
	snap := config.Snapshot()
	m.idw.bindAccess(context.Background(), snap.CFAccessTeamDomain, snap.CFAccessAUD, identityPolicyFrom(snap), d.Log)
	return id, nil
}

// errIdentityTie is the refusal of a server, mount and predicate that disagree.
var errIdentityTie = errors.New("books: the identity predicate, the server's identity and the mount's authentication do not agree")

// identityTies is the one place the predicate, whether the server was built under
// core identity (server.WithIdentity) and the mount's authentication must agree,
// and Register calls it before it mounts. Each way they can come apart is a way to
// serve with the wrong authority: core identity with a server still on HEAD's
// middleware (nothing then judges against core), a server under core identity
// behind a mount that says LegacyAuthGin (the kernel runs no stage 12 for it), and
// a flipped mount over a server that does not read core's principal.
func identityTies(predicate, serverUnderIdentity bool, mount httpapi.LegacyAuth) error {
	switch {
	case predicate != serverUnderIdentity:
		return fmt.Errorf("%w: predicate=%v, server under core identity=%v", errIdentityTie, predicate, serverUnderIdentity)
	case predicate != (mount == httpapi.LegacyAuthCore):
		return fmt.Errorf("%w: predicate=%v, mount auth=%v", errIdentityTie, predicate, mount)
	}
	return nil
}

// errStoreNotAtIdentityLevel is Attach's refusal of a server built under core
// identity over a store whose identity step has not run.
var errStoreNotAtIdentityLevel = errors.New("books: the server runs under core identity but the store's identity cutover (books.level-2 and M3) is not done")

// identityStoreReady is Attach's check, before the App runs, that the server the
// predicate asked for was built under core identity and that the store it is built
// over is at BooksIdentityLevel: M3 is done (it runs behind books.level-2, so done
// implies the level). The kernel runs the cutover before Register, so on a real
// boot this holds; the check is for the boot where it does not (a manifest that
// lost M3, a list the kernel skipped), where serving would put core's empty user
// table in front of every HEAD user. The level is read here and not in Register
// because the storage admin that answers it exists only once app.New has returned.
func identityStoreReady(ctx context.Context, underIdentity bool, admin migrationReader) error {
	if !underIdentity {
		return errors.New("books: the identity predicate is on but the server was not built under core identity")
	}
	if admin == nil {
		return errors.New("books: no storage admin to read the store's level from")
	}
	done, err := admin.DataMigrationDone(ctx, cutover.IdentityCutoverID)
	if err != nil {
		return fmt.Errorf("books: read the identity cutover's state: %w", err)
	}
	if !done {
		return errStoreNotAtIdentityLevel
	}
	return nil
}

// migrationReader is the one read of core's storage admin the check needs.
type migrationReader interface {
	DataMigrationDone(ctx context.Context, id string) (bool, error)
}
