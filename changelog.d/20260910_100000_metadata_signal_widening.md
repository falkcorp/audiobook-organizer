### Added

- **Metadata fetches now keep several provider fields that were previously
  decoded and thrown away**, so more of what a provider knows about a book is
  stored and available as a matching signal:
  - **Abridged / unabridged** (from Audible's `format_type`).
  - **Runtime** from Hardcover (`audio_seconds`) and Audnexus (`runtimeLengthMin`),
    which previously contributed no duration at all.
  - **Both ISBN-10 and ISBN-13** when a provider returns them (Google Books,
    Open Library, Hardcover previously collapsed the two into one, losing one
    identifier).
  - **Secondary series** membership (from Audnexus `seriesSecondary`).
  - **Raw series position**, preserving a decimal like "1.5" that the whole-number
    series field cannot hold.
  - **Subtitle** and **page count** where the provider reports them.

  These are stored as identification/matching signals; they do not change the
  book fields shown to clients beyond filling previously-empty identifiers.
