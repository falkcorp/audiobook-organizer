- [ ] **SC8-WORKSPACE-LIST-GROWS** `split-readiness.yml` packs every `core/web`
      workspace, and `web_build_against_tarballs.py` rewrites every `@mediacore/*`
      dependency of `web/`. Only `@mediacore/api-client` exists today. When
      `@mediacore/shell` and `@mediacore/ui-kit` land, check that SC8 packs them
      all and still fails (not passes) when `web/` depends on a package that has
      no tarball.
