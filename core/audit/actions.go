// file: core/audit/actions.go
// version: 1.0.0
// guid: d49c3aba-d467-42d9-9205-66ce8b49640f
// last-edited: 2026-11-10

package audit

import "strings"

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
	return []string{
		"core.apikey.create",
		"core.apikey.require_confirmation",
		"core.apikey.revoke",
		"core.apikey.set_status",
		"core.audit.export",
		"core.audit.prune",
		"core.auth.login",
		"core.auth.logout",
		"core.auth.setup",
		"core.backup.create",
		"core.backup.download",
		"core.backup.import",
		"core.bootstrap.redeem",
		"core.diagnostics.download",
		"core.identity.displace",
		"core.identity.link",
		"core.identity.unlink",
		"core.integration.change_url",
		"core.integration.create",
		"core.integration.delete",
		"core.integration.local_migrated",
		"core.integration.test",
		"core.integration.update",
		"core.integration.write_secret",
		"core.invite.accept",
		"core.invite.create",
		"core.oidc_provider.create",
		"core.oidc_provider.delete",
		"core.oidc_provider.update",
		"core.outbox.backlog_purge",
		"core.outbox.dead_discard",
		"core.outbox.dead_retry",
		"core.permission.deny",
		"core.restore.apply",
		"core.restore.revoke",
		"core.restore.stage",
		"core.role.accept_seed",
		"core.role.create",
		"core.role.delete",
		"core.role.review_admin",
		"core.role.update",
		"core.role.update_seed",
		"core.secret.rotate",
		"core.session.revoke",
		"core.settings.update",
		"core.storage.checkpoint_deleted",
		"core.storage.cutover_blocker_delete",
		"core.storage.cutover_blocker_fix",
		"core.storage.cutover_step",
		"core.storage.quarantine_purge",
		"core.system.factory_reset",
		"core.task.approve",
		"core.task.cancel",
		"core.task.reject",
		"core.templogin.consume",
		"core.templogin.mint",
		"core.update.apply",
		"core.update.rollback",
		"core.user.create",
		"core.user.delete",
		"core.user.disable",
		"core.user.enable",
		"core.user.reset_password",
		"core.user.set_roles",
	}
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
