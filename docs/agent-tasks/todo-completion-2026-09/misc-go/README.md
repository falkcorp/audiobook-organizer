<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/README.md -->
<!-- version: 1.6.0 -->
<!-- guid: 8338487b-0b0a-4176-a527-6ac7a23fe139 -->
<!-- last-edited: 2026-09-10 -->

# Workstream — misc-go (todo-completion-2026-09)

16 tasks: 4 carried forward from the 2026-08-21 package (ids kept), 12 new (TASK-300+). Projected from `../state/merged.json` by `../state/tools/gen_new_package.py` — regenerate, never hand-edit.

| Task | Kind | Risk | Priority | Effort | Title | Evidence |
|---|---|---|---|---|---|---|
| [TASK-083](TASK-083-fix-or-verify-the-4-still-open-go-path-injection.md) | carried | security | P1 | M | Fix or verify the 4 still-open go/path-injection findings | PR #2781 confirmed MERGED, but PR #2781's own body states the outcome explicitly: 'this re |
| [TASK-086](TASK-086-collapse-internal-whitespace-in-util-normalizeau.md) | carried | correctness | P2 | S | Collapse internal whitespace in util.NormalizeAuthor so double-spaced names dedu | internal/util/normalize.go:26 func NormalizeAuthor(s string) string { return strings.ToLow |
| [TASK-186](TASK-186-measure-the-real-double-primary-rate-library-wid.md) | carried | correctness | P2 | M | Measure the real double-primary rate library-wide, then build the demote-extras  | grep -rln 'primaries > 1/MultiPrimary/DoublePrimary/double_primary' --include='*.go' inter |
| [TASK-197](TASK-197-audit-every-registry-runitems-caller-s-custom-la.md) | carried | correctness | P2 | L | Audit every registry.RunItems caller's custom Label closure for the post-fn re-r | grep -rl 'Label:\s*func' internal/plugins / wc -l -> 35 (was 31 at brief-authoring time, 3 |
| [TASK-335](TASK-335-reauth-passkey-reverify-gate-future.md) | new-todo | security | P1 | M | Reauth / passkey reverify gate (future) | TODO.md lines 1045 |
| [TASK-344](TASK-344-dedup-mergebooks-hard-delete-path-has-no-audio-r.md) | new-todo | data-loss | P1 | M | `dedup.MergeBooks` hard-delete path has no audio-route guard | TODO.md lines 2304 |
| [TASK-349](TASK-349-decide-whether-a-backup-that-lands-on-the-same-f.md) | new-todo | data-loss | P1 | S | Decide whether a backup that lands on the same filesystem should warn at startup | TODO.md lines 3395 |
| [TASK-350](TASK-350-repair-the-12-525-existing-books-with-no-book-fi.md) | new-todo | data-loss | P1 | M | Repair the 12,525 existing books with no `book_file` rows, and the ~1,710 track- | TODO.md lines 3589 |
| [TASK-352](TASK-352-apply-it-repointing-rather-than-deleting.md) | new-todo | data-loss | P1 | S | Apply it, REPOINTING rather than deleting | TODO.md lines 3966 |
| [TASK-355](TASK-355-decide-the-repair-shape-with-the-user-before-wri.md) | new-todo | data-loss | P1 | M | Decide the repair shape with the user before writing it | TODO.md lines 4388 |
| [TASK-357](TASK-357-sec-backup-abspath-decide-whether-the-backup-res.md) | new-todo | security | P1 | S | SEC-BACKUP-ABSPATH — Decide whether the backup restore path should *reject* abso | TODO.md lines 4726 |
| [TASK-369](TASK-369-todo-sso-edge-neither-native-app-auth-mode-is-ac.md) | new-todo | security | P1 | M | TODO-SSO-EDGE — Neither native-app auth mode is actually configured at the Cloud | TODO.md lines 16927 |
| [TASK-370](TASK-370-todo-sec-bind-the-service-binds-every-interface.md) | new-todo | security | P1 | M | TODO-SEC-BIND — The service binds every interface (`ExecStart=… serve --host 0.0 | TODO.md lines 16949 |
| [TASK-371](TASK-371-todo-sec-jwt-rotate-abs-jwt-secret.md) | new-todo | security | P1 | S | TODO-SEC-JWT — Rotate `ABS_JWT_SECRET` | TODO.md lines 16960 |
| [TASK-372](TASK-372-todo-sec-systemd-the-unit-has-user-audiobook-non.md) | new-todo | security | P1 | M | TODO-SEC-SYSTEMD — The unit has `User=audiobook`, `NoNewPrivileges`, `ProtectKer | TODO.md lines 16966 |
| [TASK-374](TASK-374-itunes-2-way-sync-continuation-p3-redefine-rever.md) | new-todo | data-loss | P1 | L | iTunes 2-way-sync — continuation (P3 redefine + reverse sync + footgun audit) | TODO.md lines 17307 |

## Ground rules

- Worktree per task (the ⛔ START HERE block in each brief). Never edit `main`.
- **Verify every file:line anchor with `grep` before editing** — line numbers are a starting point, not a guarantee.
- Gate per brief (**How to test** section). Never `make ci` — red on `main` from pre-existing staticcheck findings.
- Coordinator owns git: workers commit in their worktree and STOP. See [`../ORCHESTRATION.md`](../ORCHESTRATION.md).
