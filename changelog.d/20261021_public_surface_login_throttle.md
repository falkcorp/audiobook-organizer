### Security

#### Login failure throttle now counts each visitor behind the tunnel separately

The web login and the Audiobookshelf-compatible `/login` and `/auth/refresh` throttled failed attempts per peer address. Behind the Cloudflare tunnel the peer is always the connector, so one visitor failing 15 times locked every other visitor out, and a visitor could not be told apart from another. The throttle now keys on the visitor address (`CF-Connecting-IP`, believed only from a configured tunnel connector or a verified Access assertion; a public IPv6 visitor counts per /64). A request from any other peer is still keyed on its own address, so a spoofed header cannot pick a bucket. Session and audit records are unchanged.
