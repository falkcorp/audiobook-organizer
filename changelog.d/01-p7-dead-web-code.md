### Removed

- Removed unused frontend files, unused API client functions and a dead test configuration. Twelve web files that nothing imported (plus the tests of three of them), twenty-nine unused functions and the types only they used in the API client, the unused position and status-list helpers in the reading client, the unused playlist reorder helper, and the never-read `test:` block in `web/vite.config.ts` are gone. The real test configuration in `web/vitest.config.ts` is unchanged. No behaviour changes.
