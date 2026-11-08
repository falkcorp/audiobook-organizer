- [ ] **PLATFORM-CORE-R1-30-MEMORY-LEAK-SCAN** Give `scripts/check-memory-leaks.py` a
      second source tree and add the `core/web` leg and path trigger to
      `.github/workflows/memory-leak-scan.yml`. The workflow's header says the PR
      that creates `core/web` adds it; R1-30 did not, because the script takes no
      directory argument (it finds `web/src` by walking up) and the scanner's
      patterns are React ones, while `@mediacore/api-client` holds no React code
      (its one `addEventListener`, the deadline's abort forward in `client.ts`, is
      removed in a `finally`). Do it with the first core/web package that has
      components (ui-kit, F9-01).
