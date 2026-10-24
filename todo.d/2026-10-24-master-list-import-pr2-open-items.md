- [ ] **TODO-MLIST-REQUIREADMIN-AUTHOFF** `servermiddleware.RequireAdmin` itself
      still answers 401 with `EnableAuth=false` for its other users (retired wipe
      stub, cache introspection, admin debug API, `/system` wipe routes). Decide
      per route whether auth-off should open it, then give each the same
      pass-through the master-list routes now have through `Server.adminRole()`.
      Do not change `RequireAdmin` globally: it also guards destructive routes.
- [ ] **TODO-MLIST-AUDIBLE-CUTOVER** Cut over `maintenance.audible-read-status`
      to the `master-list-import` fixer (its Audible-specific input becomes a
      list upload) and retire the old op.
- [ ] **TODO-MLIST-PLANROWS-4148** Plan rows of a Repairs fixer are readable by
      any `library.view` holder, including the master-list plan rows (list titles
      and authors, the target user's read status). PR #4148 gates that read and
      must merge BEFORE the master-list apply PR (`feat/master-list-import-apply-r2`,
      which supersedes #4147). Once #4148 is on main, rewrite the "reads are not
      gated yet" comments in `master_list_import_fixer.go` and
      `wire_master_list_routes.go`, the changelog's last line and the executive
      summary's "Not covered yet" paragraph, then tick this task.
