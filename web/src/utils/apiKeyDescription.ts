// file: web/src/utils/apiKeyDescription.ts
// version: 0.1.0
// guid: 84cc6509-a3be-4cf7-a7ba-c1748b7aad7f
// last-edited: 2026-11-22

/**
 * The longest API key description core stores, in characters
 * (auth.MaxKeyDescriptionRunes, the same bound as a role description). Core
 * refuses a longer one, and one with a line break or other control character,
 * so the create dialog's description box is a single line capped here.
 */
export const API_KEY_DESCRIPTION_MAX_CHARS = 1024;
