### Changed

- CI: one web dependency entry point (`make web-deps`, which runs `scripts/ci/build_web.sh deps`) at every build site, so web builds keep working when `web/` starts depending on the shared `core/web` packages. The two release workflows no longer run the reusable workflow's separate frontend job, whose output was discarded: the binary embeds the UI built by the pre-build script.
