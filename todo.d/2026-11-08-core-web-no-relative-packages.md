- [ ] **PLATFORM-CORE-R1-30-NO-RELATIVE-PACKAGES** Add `import/no-relative-packages`
      to `core/web/eslint.config.mjs` (07 §7.3.1 names it beside
      `no-restricted-imports`). R1-30 enforces the import boundary with
      `no-restricted-imports` patterns only (a specifier climbing out of
      `core/web`, `@abo/*`, ABO's `web/src`), because the rule that rejects a
      sibling package reached by a relative path needs `eslint-plugin-import-x`,
      which is not in 13's version table and has one package to guard until
      `ui-kit` and `shell` arrive. Pin the plugin in 13 §13.4 and add the rule in
      the shell-packages PR (F9-01).
