### Fixed

- **Chapter sets held only because of junk author or series fields now
  merge.** An author field that is a placeholder, or that is the folder's own
  name and cannot be a person's ("Jennsen, GS_ 08 Rubicon (Amaranthe 08)"),
  counts as missing. So does a series field that is a file name ("01.Intro",
  "read by narrator"). A real author folder ("Jane Author/" with files by Jane
  Author) and two real series are still held.
- **Chapter sets whose files are all still unorganized now have a survivor.**
  When no member is organized, the lowest-id primary member takes the others'
  files; the merged book is organized later like any other imported book.
  These sets used to be held as "no survivor".
