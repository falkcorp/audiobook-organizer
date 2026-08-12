<!-- file: docs/archive/2026-08-consolidation-wave2/research/2026-06-15-sonarr-radarr-advanced-settings.md -->
<!-- version: 1.0.1 -->
<!-- guid: b2c3d4e5-f6a7-8901-bcde-f12345678901 -->
<!-- last-edited: 2026-08-12 -->

# Sonarr/Radarr "Show Advanced" Settings Toggle — Research

> Captured 2026-06-15. Input for `<ToolsPanel>` and broader Settings page UX.
> Sources: github.com/Sonarr/Sonarr, github.com/Radarr/Radarr

---

## State Management & Persistence

- **Mechanism:** Redux with Redux-Actions
- **State path:** `state.settings.advancedSettings` (boolean)
- **Toggle action:** `toggleAdvancedSettings()` — inverts the boolean
- **Persistence:** The boolean is in the Redux persistence list → survives page refresh and browser restart (localStorage)
- **Button component:** `AdvancedSettingsButton` is a pure presentational component; parent wires Redux dispatch and selector

---

## Component Pattern

Two props on a `FormGroup` wrapper:

```tsx
<FormGroup isAdvanced advancedSettings={showAdvanced}>
  {/* advanced field content */}
</FormGroup>
```

- `isAdvanced` — static declaration at author time: "this field is advanced"
- `advancedSettings` — runtime boolean from Redux: "user has toggled advanced on"
- `FormGroup` renders `null` when `isAdvanced && !advancedSettings`

This separates **what is advanced** (declaration, co-located with the field) from **show advanced?** (runtime state, global). No central registry of advanced fields needed.

Works at any granularity: wrap an entire section or a single field.

---

## Key Design Decisions

1. **Co-located declaration** — `isAdvanced` lives next to the field, not in a separate config. Easy to add, easy to audit, easy to grep.
2. **Single persisted boolean** — only the toggle state is persisted, not individual field values.
3. **Stateless button** — `AdvancedSettingsButton` doesn't manage state; the parent Redux setup does. The button is reusable anywhere.
4. **Cascading via cloneElement** — `FormGroup` clones children and injects `isAdvanced` prop, so nested components can know their context without prop-drilling.

---

## Recommended Pattern for audiobook-organizer

Since the project uses React + MUI (not Redux), adapt as follows:

```tsx
// Global context (or per-settings-tab useState + localStorage)
const { showAdvanced, toggleAdvanced } = useAdvancedSettings();

// Wrapper component
function AdvancedField({ children }: { children: React.ReactNode }) {
  const { showAdvanced } = useAdvancedSettings();
  if (!showAdvanced) return null;
  return <>{children}</>;
}

// Usage in any settings form
<AdvancedField>
  <TextField label="Managed tools directory" ... />
</AdvancedField>

// Toggle button (anywhere in the settings header)
<Button onClick={toggleAdvanced} size="small" variant="outlined">
  {showAdvanced ? 'Hide Advanced' : 'Show Advanced'}
</Button>
```

**Persistence:** `localStorage.setItem('settings.showAdvanced', ...)` on toggle, read at init.

**Scope:** one global toggle is simpler and matches Sonarr/Radarr. Per-tab toggles add complexity for marginal benefit — start global, reconsider only if specific tabs need different defaults.

**Wizard:** advanced fields in the wizard should always show (the wizard is already the guided path; hiding fields there defeats the purpose of the "Let me choose" branch).
