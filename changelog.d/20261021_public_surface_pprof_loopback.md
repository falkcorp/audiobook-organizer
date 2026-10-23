### Security

#### The pprof debug listener refuses any address that is not loopback

In a `pprof` build, `ABK_PPROF_ADDR` used to accept any address and only logged a warning for an all-interfaces bind, although the profiling endpoints (goroutine dumps, heap profiles, CPU profiles, source paths) have no authentication. The listener now starts only on a loopback address (`127.0.0.1`, `::1`, or `localhost`); a bare port is still pinned to `127.0.0.1`. Any other address, including `0.0.0.0`, `::` and host names other than `localhost`, is refused at startup with an error log saying why, and no listener is started. Use SSH port-forwarding to reach it from another machine. Default builds are unaffected: the listener only exists in a build with the `pprof` tag.
