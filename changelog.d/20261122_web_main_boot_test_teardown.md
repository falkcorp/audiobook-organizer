### Fixed

- `web/src/main.boot.test.tsx` now unmounts every React root `main.tsx` creates (recorded through a `react-dom/client` mock, unmounted when each booted page is replaced and after each test). The roots were left mounted, so the real AuthProvider's three one-second status retries and the bell's MUI transition timers could fire after the file finished and jsdom was gone, which failed the run with `ReferenceError: window is not defined` although every test had passed.
