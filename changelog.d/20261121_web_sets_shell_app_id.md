### Fixed

- The running web app now sets the shell app ID (`abo`, the books app ID) before its first render. Nothing in `web/` set it, so the operations bell's section collapse state could not be saved: `storageKey()` threw and the bell swallowed the throw. The bell now reports a missing app ID once with `console.error` instead of failing silently. `@mediacore/shell` exports `setShellAppId` for apps that have not adopted `createShell` yet, and `@mediacore/shell/testing` exports `setTestShellAppId` for tests.
