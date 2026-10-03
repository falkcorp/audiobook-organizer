### Fixed

- **Junk-title repair: a misheard transcription no longer blocks a title that
  the file's own name and a metadata provider agree on.** Books such as
  "20 - Hilldiggers" were held for manual review because the speech-to-text
  reading of the intro said "Hildiggers". When a provider recorded exactly the
  title in the file's name, the transcription is now overruled: the row is
  proposed at review risk and the reason states what was overruled. The hold
  stays when the provider only matches as "title plus subtitle" (a
  first-book-of-the-series hit), when a search candidate agrees with the
  transcription, or when the author already has a book under the transcribed
  title.
