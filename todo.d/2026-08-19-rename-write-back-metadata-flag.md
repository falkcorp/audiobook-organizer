## Config

- [ ] **Rename `write_back_metadata` → `auto_write_tags_on_fetch`.** The current name
      reads like a global "do we ever write tags to files" switch. It is not. It
      gates exactly one call site — `mfs.writeBackMetadata(updatedBook, meta)` at
      `internal/metafetch/service_fetch.go:309`, on the **auto-fetch** path only.

      Tag writing on **apply** is a completely separate flag,
      `auto_write_tags_on_apply` (`internal/metafetch/service_writeback.go:604`),
      which is **on** in prod. So the two live side by side in the config with one
      named for what it does and the other named for the whole subsystem, and
      reading `write_back_metadata: false` naturally leads to "we're not writing
      tags to files at all" — which is wrong. That misreading already happened.

      Renaming makes the pair symmetric and self-documenting:
      `auto_write_tags_on_fetch` / `auto_write_tags_on_apply`.

      **Touch points:** `internal/config/config.go:531` (struct field + json tag),
      `:1445` (viper binding), `internal/config/persistence.go:1075` (snapshot
      load), `internal/metafetch/service_fetch.go:309` (the only read site), and
      `internal/config/config_unit_test.go:654`.

      **Migration matters — do not do a bare rename.** Live config is a persisted
      snapshot, not defaults (the stored value overrides `config.go`'s default).
      Prod's snapshot has the OLD key, so the loader must keep honouring
      `write_back_metadata` as a deprecated alias, or the setting silently reverts
      to its default on the next load and the fetch path changes behaviour without
      anyone asking for it. Read the old key when the new one is absent, and log
      once at WARN when the alias is used.

- [ ] **Related asymmetry found while tracing the above: auto-fetch embeds cover
      art into audio files even when tag write-back is off.**
      `mfs.embedCoverInBookFiles(updatedBook, coverPath)` sits *outside* the
      `if config.AppConfig.WriteBackMetadata` block in `service_fetch.go` (~:301 vs
      :309). So with `write_back_metadata: false`, auto-fetch still modifies files
      on disk — artwork only, no text tags. That may well be intended, but it is
      not what either flag's name suggests, and it means "off" does not mean "does
      not touch my files." Decide whether cover embedding belongs under the same
      gate, and say so in a comment either way.
