### Security

#### Origin refuses connections from peers outside an allow-list (ALLOWED_PEER_CIDRS)

The service binds every interface, so an exposed port answered anyone who could reach it. A new environment-only setting, `ALLOWED_PEER_CIDRS` (comma-separated CIDRs or bare IPs), names the peers allowed to talk to the origin directly. A request from any other peer gets 403 before routing, on the HTTPS, HTTP/3, plain HTTP and port-80 redirect listeners alike. Loopback and every `TUNNEL_CONNECTOR_ADDRS` address are always allowed, the peer is the socket's address (never a forwarded header), and a list with no valid entry still enforces. Left empty the behaviour is unchanged and a startup warning says the origin accepts any peer. List this host's own non-loopback addresses and any LAN scraper or client that calls it directly. HTTP/3 refuses the request, not the QUIC handshake.
