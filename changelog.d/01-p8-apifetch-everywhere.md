### Fixed

- An expired login session now shows an error everywhere in the web app instead of looking like success. Every browser request goes through `apiFetch`, so a login page answered in place of an API response is reported as "session expired" on the Users page (where deactivate and reactivate previously ignored failures entirely), the metadata revert and write-back, the plugin and Deluge settings, the iTunes download, and the version panels. An ESLint rule now keeps raw `fetch` out of `web/src` except inside `utils/apiFetch.ts` and tests.
