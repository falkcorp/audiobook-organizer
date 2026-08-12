# Task 037: FE-10 — Add Vitest coverage thresholds

**Depends on:** none
**Estimated effort:** S
**Wave:** 10 (UI polish)
**Spec:** `docs/superpowers/bot-tasks/2026-04-30-fe-10-coverage.md`

## Goal

Add Vitest coverage thresholds to the frontend test config so CI fails if coverage drops
below the minimum. Read the spec for exact targets.

## Context

- Frontend tests: `web/` directory, run via `npm test` or `make test-frontend`
- Vitest config: `web/vite.config.ts` or `web/vitest.config.ts` — check which exists
- Current coverage: check by running `cd web && npm run coverage` or `npm test -- --coverage`
- Spec: `docs/superpowers/bot-tasks/2026-04-30-fe-10-coverage.md` — read it for exact thresholds

## Files to modify

- `web/vite.config.ts` or `web/vitest.config.ts` — add `coverage.thresholds`
- `package.json` (web/) — add `coverage` script if not present

## Instructions

### 1. Read the spec

```bash
cat docs/superpowers/bot-tasks/2026-04-30-fe-10-coverage.md
```

### 2. Run current coverage

```bash
cd web && npm test -- --coverage 2>/dev/null || npm run coverage
```

Note the current lines/branches/functions/statements percentages.

### 3. Add thresholds to Vitest config

In `vite.config.ts` or `vitest.config.ts`, find or add the `test.coverage` section:

```ts
test: {
    coverage: {
        provider: 'v8',
        reporter: ['text', 'lcov'],
        thresholds: {
            lines: <current_lines - 2>,    // Set 2% below current to start
            branches: <current_branches - 2>,
            functions: <current_functions - 2>,
            statements: <current_statements - 2>,
        }
    }
}
```

Set thresholds 2% below the current baseline so CI immediately enforces "don't regress"
without requiring new tests to be written now. The spec may specify different values —
follow the spec if it does.

### 4. Add coverage script to package.json

```json
"scripts": {
    "coverage": "vitest run --coverage"
}
```

### 5. Wire into CI

Check `.github/workflows/ci.yml` for the frontend test step. Add coverage flag:
```yaml
- name: Run frontend tests with coverage
  run: cd web && npm run coverage
```

## Test

```bash
cd web && npm run coverage   # must pass with thresholds
make ci
```

## Commit

```
chore(test): add Vitest coverage thresholds to frontend CI (FE-10)
```

## PR title

`chore(test): frontend coverage thresholds — FE-10`

## After merging

Mark `- [ ] **FE-10**` as `- [x]` in `TODO.md`.
