- [ ] **`scan-import-organize.spec.ts` (7 failures) — Settings tab deep-link
      fixed, but that was NOT the blocker. Count unchanged at 7.**
      Investigated 2026-08-09.

      **Applied and kept (correct, but insufficient):** the tests navigated to
      `/settings` and immediately clicked "Add Import Path". Settings is tabbed
      now and defaults to **Library**; the button is rendered by
      `components/settings/PathsSettingsTab.tsx:229`, mounted from
      `pages/Settings.tsx:832`, i.e. only when the **Paths** tab is active.
      `tabFromHash()` (`Settings.tsx:96`) maps a URL hash to a tab index via
      `TAB_KEYS`, so `'/settings#paths'` is the app's own supported deep link.
      All four navigations now use it.

      **It did not help — still 7 failures**, all still timing out on
      `getByRole('button', { name: 'Add Import Path' })`. So the Paths tab is
      not rendering, or the Settings page is not reaching a usable state at
      all. The change is kept because it is verifiably more correct than
      `/settings`, not because it fixed anything.

      **Next step, and do this before writing any code:** capture the DOM for
      one of these failures specifically. `test-results/` was dominated by
      other tests' directories, so the Settings page snapshot was never
      actually read — which, given that reading the snapshot has found every
      real cause in this effort, is the obvious gap. Run just this spec and
      open `test-results/<dir>/error-context.md` for the workflow test.

      Candidates worth checking once the snapshot is in hand: whether Settings
      renders an error boundary from an unmocked endpoint, whether it redirects
      (auth), and whether the tab panel is lazily mounted such that the
      hash-selected index is applied after the click is attempted.
