### Docs

- [ ] **Decide the fate of `docs/CODING_STANDARDS.md` — it is an unreferenced stale
      copy with a false "managed centrally" banner.** Four measured facts, then a
      recommendation; this needs an owner decision, not a silent cleanup.

  1. **Nothing operational references it.** Grepping `CODING_STANDARDS` across the
     whole worktree returns hits only in `CHANGELOG.md`, `changelog.d/`,
     `docs/archive/` and `docs/audits/` — all prose *about* the file. `CLAUDE.md`,
     `AGENTS.md` and `.github/copilot-instructions.md` do not mention it. The Quick
     Start points at `.github/copilot-instructions.md`, `.github/instructions/` and
     `AGENTS.md` instead.

  2. **The banner is false.** The file carries, three times, `DO NOT EDIT: This file
     is managed centrally in ghcommon repository`. No assembler exists: grepping
     `CODING_STANDARDS` across every file type in `falkcorp/github-common` returns
     zero hits, and this repo's workflows reference ghcommon only for reusable
     workflows (`uses: falkcorp/github-common/.github/workflows/...`), never docs.
     Its 7-commit history is entirely hand-edits. It is a one-time manual paste.

  3. **39% of it is duplicated.** Lines 1918-3168 are a verbatim second copy of
     649-1899 (`typescript.instructions.md` included twice); only 18 lines differ,
     being the second copy's own header. ~1,251 wasted lines.

  4. **It is not purely dead — deleting it would silently drop one real rule.**
     Lines 599-605 (TOOL-5, commit `761f5c1b`, 2026-06-23) say *prefer a narrow
     hand-written fake over a generated mock* for small new interfaces. That
     **contradicts** the org rule in `.standards/instructions/go.md` (*"do not
     hand-write mocks"*). `docs/audits/2026-08-16-manual-mock-inventory.md:426`
     already scheduled retiring TOOL-5 as "phase 2 only" — so the conflict is known
     and deliberately unresolved, not an oversight.

  **Recommendation:** archive the file rather than maintain it. Its Go half is now
  superseded upstream (falkcorp/.github#4, merged — `instructions/go.md` v1.1.0 has
  the 1.26 idioms, synctest, the globals rule and `omitzero`), and `.standards/` is
  what CLAUDE.md actually names canonical. Before archiving, TOOL-5 needs somewhere
  to live — either move those 7 lines into the audit's phase-2 track, or raise the
  upstream proposal the audit says is required. **Do not just delete it**; that
  drops a deliberate local exemption with no record.

  Cost if instead kept and repaired: delete 1,251 duplicated lines, re-sync the Go
  half against the new upstream, and correct the banner to say what is true.
