### Security

- Web: raised the `minimatch` override from `>=10.2.1` to `^11.0.1`, so the lint toolchain (eslint, typescript-eslint) resolves minimatch 11.0.5 and no longer carries the globparts-amplification denial-of-service range (Dependabot alert 66). Dev-only dependency; not shipped in the built UI.
