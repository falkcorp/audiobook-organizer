// file: core/audit/actions.go
// version: 1.1.0
// guid: d49c3aba-d467-42d9-9205-66ce8b49640f
// last-edited: 2026-11-10

package audit

import "strings"

// Visibility says who may see a record of an action in the audit query API
// (05-rbac §7.6).
type Visibility uint8

const (
	// VisibilityUnknown is the zero value and the answer for an action the
	// table does not list (a module's own, or one added without a
	// classification): it is withheld from everyone who is not a user manager.
	VisibilityUnknown Visibility = iota
	// VisibleToAll is an action whose record every holder of core:audit.view
	// may see: it names no credential, identity or security decision.
	VisibleToAll
	// UserScoped is an action about identities, credentials, sessions or
	// security decisions: only a holder of core:users.manage sees it.
	UserScoped
)

// actionRow is one row of the table: an action and who may see its records.
type actionRow struct {
	name string
	vis  Visibility
}

// coreActionTable is the normative table of 05-rbac §7.1 with each action
// classified. It is built per call, so a caller cannot change it. Sorted by
// name.
func coreActionTable() []actionRow {
	return []actionRow{
		{"core.apikey.create", UserScoped},
		{"core.apikey.require_confirmation", UserScoped},
		{"core.apikey.revoke", UserScoped},
		{"core.apikey.set_status", UserScoped},
		{"core.audit.export", VisibleToAll},
		{"core.audit.prune", VisibleToAll},
		{"core.auth.login", UserScoped},
		{"core.auth.logout", UserScoped},
		{"core.auth.setup", UserScoped},
		{"core.backup.create", VisibleToAll},
		{"core.backup.download", VisibleToAll},
		{"core.backup.import", VisibleToAll},
		{"core.bootstrap.redeem", UserScoped},
		{"core.diagnostics.download", VisibleToAll},
		{"core.identity.displace", UserScoped},
		{"core.identity.link", UserScoped},
		{"core.identity.unlink", UserScoped},
		{"core.integration.change_url", UserScoped},
		{"core.integration.create", VisibleToAll},
		{"core.integration.delete", VisibleToAll},
		{"core.integration.local_migrated", VisibleToAll},
		{"core.integration.test", VisibleToAll},
		{"core.integration.update", VisibleToAll},
		{"core.integration.write_secret", UserScoped},
		{"core.invite.accept", UserScoped},
		{"core.invite.create", UserScoped},
		{"core.oidc_provider.create", UserScoped},
		{"core.oidc_provider.delete", UserScoped},
		{"core.oidc_provider.update", UserScoped},
		{"core.outbox.backlog_purge", VisibleToAll},
		{"core.outbox.dead_discard", VisibleToAll},
		{"core.outbox.dead_retry", VisibleToAll},
		{"core.permission.deny", UserScoped},
		{"core.restore.apply", VisibleToAll},
		{"core.restore.revoke", UserScoped},
		{"core.restore.stage", VisibleToAll},
		{"core.role.accept_seed", VisibleToAll},
		{"core.role.create", VisibleToAll},
		{"core.role.delete", VisibleToAll},
		{"core.role.review_admin", VisibleToAll},
		{"core.role.update", VisibleToAll},
		{"core.role.update_seed", VisibleToAll},
		{"core.secret.rotate", UserScoped},
		{"core.session.revoke", UserScoped},
		{"core.settings.update", VisibleToAll},
		{"core.storage.checkpoint_deleted", VisibleToAll},
		{"core.storage.cutover_blocker_delete", VisibleToAll},
		{"core.storage.cutover_blocker_fix", VisibleToAll},
		{"core.storage.cutover_step", VisibleToAll},
		{"core.storage.quarantine_purge", VisibleToAll},
		{"core.system.factory_reset", VisibleToAll},
		{"core.task.approve", VisibleToAll},
		{"core.task.cancel", VisibleToAll},
		{"core.task.reject", VisibleToAll},
		{"core.templogin.consume", UserScoped},
		{"core.templogin.mint", UserScoped},
		{"core.update.apply", VisibleToAll},
		{"core.update.rollback", VisibleToAll},
		{"core.user.create", UserScoped},
		{"core.user.delete", UserScoped},
		{"core.user.disable", UserScoped},
		{"core.user.enable", UserScoped},
		{"core.user.reset_password", UserScoped},
		{"core.user.set_roles", UserScoped},
	}
}

// CoreActions returns every action core itself records, sorted: the
// normative table of 05-rbac §7.1, one string per row cell (the deferred-build
// members of §3.6 are not in it). Each is "<ns>.<noun>.<verb>" in the present
// tense (03 §3.6); success and failure of one action share its name and
// differ in Event.Outcome.
//
// A module's own actions ("books.user.merge") are not listed here: they are
// module-defined, recorded through Logger, and checked only against ValidAction.
// The slice is built per call, so a caller cannot change the table.
func CoreActions() []string {
	rows := coreActionTable()
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.name
	}
	return out
}

// VisibilityOf classifies action. An action that is not in the table is
// VisibilityUnknown: default-deny, so a new action cannot be shown to every
// audit viewer by omission (TestCoreActions_EveryActionIsClassified).
func VisibilityOf(action string) Visibility {
	for _, r := range coreActionTable() {
		if r.name == action {
			return r.vis
		}
	}
	return VisibilityUnknown
}

// ValidAction reports whether s matches
// ^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$ (03 §3.6, COR2-19):
// exactly three segments. Record, RecordIn and Emit refuse an action that
// fails it.
func ValidAction(s string) bool {
	segs := 0
	for part := range strings.SplitSeq(s, ".") {
		segs++
		if !validSegment(part) {
			return false
		}
	}
	return segs == 3
}

// validSegment is [a-z][a-z0-9_]*.
func validSegment(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// namespaceOf is the first segment of a valid action: the label of
// audit_records_total.
func namespaceOf(action string) string {
	ns, _, _ := strings.Cut(action, ".")
	return ns
}
