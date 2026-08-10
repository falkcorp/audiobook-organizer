<!-- Docs-only: records a measured production defect -- the search index worker
     silently drops updates when its 1024-deep queue overflows (56,537 dropped
     in seven days), with no retry or reconciliation. Marks index
     reconciliation as a blocking prerequisite for pushing filters/sort into
     Bleve, and closes the "is the index complete" open question with "no".

     No code changed yet, so this fragment is intentionally all comments and
     contributes nothing to CHANGELOG.md. -->
