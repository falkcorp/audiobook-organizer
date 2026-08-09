<!-- Test-asset/CI fix: takes the three ~2KB visual-regression goldens out of
     Git LFS. The blanket `*.png filter=lfs` rule meant CI checked out LFS
     POINTER FILES, so Playwright reported "Could not decode expected image as
     PNG" -- the visual test could never pass on a runner, for either browser.

     No product behaviour changed, so this fragment is intentionally all
     comments and contributes nothing to CHANGELOG.md. -->
