<!-- Docs-only: adds section 0b to the search design doc, covering whether a Go
     backend will be slow or memory-bloated for sorting. Records that memdb
     already holds the library resident (~2GB, stripped from ~10GB) and that the
     real cost is the non-title sort path materialising the full filtered set.

     No product behaviour changed, so this fragment is intentionally all
     comments and contributes nothing to CHANGELOG.md. -->
