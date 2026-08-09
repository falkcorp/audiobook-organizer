- [ ] **4 remaining failures in `metadata-provenance.spec.ts`** (down from 12).
      Diagnosed but not fixed 2026-08-09; stopped deliberately rather than
      keep iterating.

      Fixed in that pass (8 of 12): the spec mocks by patching `window.fetch`
      rather than using `page.route`, so it gets none of the shared handlers
      and needed its own `/auth/status` (without it the app rendered the LOGIN
      screen), its own `{ data: ... }` envelope, and explicit handlers for the
      book sub-resources — the generic "URL contains the book id" branch was
      swallowing `/files`, `/versions`, `/tags`, `/segments` and handing each
      of them the BOOK object, so the page crashed on `.length` of undefined.

      **Still failing, with what is known:**

      1. `dialog opens with all fields populated` — the Author textbox is
         empty. The dialog reads `formData.author`, and the detail page renders
         "Unknown Author", so the payload shape is wrong somewhere between the
         fixture and `Audiobook`. Adding `author_name`/`series_name` alongside
         the short names did NOT fix it, so the mapping is elsewhere — trace
         how `formData` is initialised from the API response rather than
         guessing again.
      2. `locked fields show orange lock icon` — walks the DOM relative to
         `getByLabel('Title *')` (`'..'` → `'..'` → `button`) to reach the lock
         icon. Fragile by construction: it depends on the exact wrapper depth
         MUI renders. Better fixed by giving the lock button a stable
         `data-testid` than by counting parents.
      3. `editing a field automatically locks it` and 4. `year field shows
         error for non-numeric input` — both start from the same field
         locators; likely fall out with (1) and (2).

      **Locator rule established in this file, worth keeping:** to READ or FILL
      a field use `getByRole('textbox', { name, exact: true })` —
      `getByLabel('Title *')` is a strict-mode violation because each field has
      an adjacent lock button labelled "Lock Title *" and getByLabel
      substring-matches. The lock tests still use `getByLabel` on purpose,
      because they traverse relative to it. A blanket sweep converting all of
      them broke passing tests; the note in the spec says so.
