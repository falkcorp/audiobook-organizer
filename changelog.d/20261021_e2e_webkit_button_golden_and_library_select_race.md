Fixed the nightly E2E run that has failed on `[webkit] Button loading states
visual check` and a rare chromium failure in `scan-import-organize.spec.ts`.
The webkit golden (`scan-button-loading-webkit-linux.png`) still showed the
uppercase "STARTING SCAN..." label from before the theme dropped
`textTransform`, so it was never going to match; the darwin goldens had the
same staleness and are regenerated too. The Library E2E mocks now answer the
Browse-by-Tag facets request instead of letting it fall through to the real
server, and the specs that click Select All or a card checkbox right after load
wait for the tag panel to settle, so the panel mounting can no longer move the
checkbox out from under the click. Test-only; no application behavior changes.
