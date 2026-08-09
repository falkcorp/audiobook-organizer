<!-- Test-asset change: adds the missing linux visual golden
     (scan-button-loading-chromium-linux.png) so the one visual-regression test
     can pass on a CI runner. Only -darwin goldens existed, and Playwright
     fails rather than writes a missing snapshot when CI=true.

     No product behaviour changed, so this fragment is intentionally all
     comments and contributes nothing to CHANGELOG.md. -->
