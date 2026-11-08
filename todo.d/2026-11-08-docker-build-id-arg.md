- [ ] **PLATFORM-CORE-R1-19-DOCKER-BUILD-ID-ARG** Add the optional `ARG BUILD_ID`
      to the Docker frontend stage (07 §7.3.2, §7.6.3 item 1), passed to the
      `npm run build` step, so a release image can carry a chosen UI build ID. R1-19
      left `Dockerfile` alone (it is not in the row's file inventory): with no
      `.git` in the Docker context and no `BUILD_ID`, `vite build` stamps the
      timestamp-plus-random ID of rule 3, which already satisfies the image probe.
      An empty `--build-arg BUILD_ID=` must fall through to that rule, which the
      Vite config already does.
