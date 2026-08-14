- [ ] **124 files remain 0600 after the fix-file-modes repair (1,547 repaired),
      and they expose a stale-path defect.** The repair enumerates
      `GetAllBookFilesCore()`, but the residue files are on-disk paths the
      canary write-back REALLY wrote (mtime in the canary window) that do not
      appear in that enumeration. Worse: the sampled book's `/files` API row
      points at a path that does NOT exist on disk (`.../The Seven Deadly
      Demons 3 - Dungeon of Pride/Dungeon of Pride.m4b` → ENOENT) while the
      real file lives at `.../The Seven Deadly Demons/Dungeon of Pride/...`.
      So (a) some books' file rows carry stale paths, (b) the write-back
      resolves the REAL file anyway (different row? path fallback?), and
      (c) the repair job can't see those paths. Investigate the row-vs-disk
      divergence (organize moved files without updating rows? duplicate
      rows?), then either extend fix-file-modes with a disk-walk mode or
      repair the residue by hand:
      `sudo find <organizer-root> -type f -user <service-user> -perm 600 -exec chmod 664 {} +`
