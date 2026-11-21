### Changed

- The web app's build can now resolve the shared shell and UI kit packages (`@mediacore/shell`, `@mediacore/ui-kit`) with one copy of React, React Router, MUI and emotion. Nothing in the app imports them yet, so the interface looks and behaves the same; this is groundwork for moving the app's layout into the shell.
- The web dependency scripts (`scripts/ci/build_web.sh`, `scripts/ci/ensure_core_web.sh`) now build the API client, the UI kit and the shell, in that order, instead of only the API client.
