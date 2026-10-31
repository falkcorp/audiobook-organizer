<!-- file: docs/decisions/2026-10-30-masksecret-length-independent.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b4e6d12-0c7a-4e3f-9a15-d2f7c3b60a94 -->
<!-- last-edited: 2026-10-30 -->

# Length-independent MaskSecret

## Decision log

Chosen: under 16 characters masks to exactly `********`; 16 or more shows
`********` plus the last 2 characters. Lengths are counted in runes so a
multi-byte suffix is never split. No prefix is ever shown.

Rejected: a flat `****` for every length (loses the "which key is this" hint for
long API keys); keeping a prefix (prefixes of provider keys identify the
provider and shrink the search space); raising only the 3+4 threshold (still
leaks a fixed fraction of a short secret).

Callers: every mask comparison already derives the expected value from
`MaskSecret(stored)` (`acceptSecretUpdate`, `restoreMaskedCredentials`,
`restoreNestedSecret`), so no literal pattern needed changing. A stored secret
equal to the literal mask `********` is still treated as unchanged when echoed,
as before. The web client only checks `includes('***')`, which still holds.

Consequences to review: all short secrets now mask identically, so restores must
come from the field's own prior value (covered by a test). `MaskSecret` is no
longer idempotent for secrets of 16+ characters. `unmaskAIEndpointAuthRefs`
matches `auth_ref` names by their mask; with one known name
(`openai_api_key`) this is still unambiguous, but adding a second name under 16
characters would make it ambiguous. Not changed here (out of scope).
