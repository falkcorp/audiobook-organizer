<!-- Test-infrastructure change: adds web/tests/e2e/global-setup.ts, which fails
     a local e2e run when :8484 is already serving a bundle older than the code
     under test. No product behaviour changed, so this fragment is
     intentionally all comments and contributes nothing to CHANGELOG.md.

     Also adds a todo.d fragment recording the three linux-only e2e failures
     that block making the CI gate blocking, measured by dispatching the
     workflow against main rather than inferred from a stale nightly. -->
