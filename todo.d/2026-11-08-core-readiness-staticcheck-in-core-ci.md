- [ ] **CORE-READINESS-STATICCHECK-IN-CORE-CI** `core/tools/staticcheck` is pinned
      and compared with the root's by `check_module_skew.py`, and `make -C core
      tools` installs it, but no core CI target runs it. Wire it into `make -C
      core ci` (or record why core stays on golangci-lint only).
