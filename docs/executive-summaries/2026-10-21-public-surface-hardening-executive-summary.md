<!-- file: docs/executive-summaries/2026-10-21-public-surface-hardening-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: f48321db-42a2-44f8-a617-4c75ee6ae1ff -->
<!-- last-edited: 2026-10-21 -->

# The public-facing server is harder to abuse

Branch `fix/public-surface-hardening`, PR not yet opened. No separate spec. Drawn from an internal review of everything an outsider can reach through the Cloudflare tunnel; the Cloudflare-side findings from that review are handled separately and are not part of this change.

## Executive Summary

- **Pages no longer redirect to a made-up address.** The server used to answer a request for `/path` with a redirect to `/path/` (and similar), building the new address from headers a caller could set. A crafted request could send a visitor to a lookalike location. The server now never redirects for those cases.
- **Direct local-network callers share the same overall limit as everyone.** The first-time setup page and the invitation pages had a rate limit with a hole: callers on the local network were exempt from the overall ceiling, so a flood from inside the network could not be stopped by it. The exemption is gone.
- **One person's failed logins no longer lock out everybody.** Behind the tunnel every visitor appeared to come from one of three relay machines, so the failed-login counter was shared. Now it counts each real visitor separately (and groups an IPv6 visitor by their /64 block so rotating addresses does not reset it).
- **The server can refuse strangers outright.** A new setting lists the networks allowed to connect directly. Anyone else gets "forbidden" before the server looks at what they asked for. Left empty, nothing changes and the server logs a warning that it accepts anyone.
- **The web app now has a browser-enforced script policy.** Browsers are told to run only the app's own scripts, plus one small theme script allowed by its fingerprint. Injected script would be refused. A second header turns off camera, microphone, location, payment and USB access.
- **Checked against the real thing.** The web app was loaded in a real browser against the finished program, and no policy violations appeared on the main pages.

## Redirects

**What it was.** The web framework's default is to "fix" a missing or extra trailing slash by redirecting, using a prefix header the caller can supply.

**Why it mattered.** An outsider could craft a link on the real site that bounced the visitor somewhere else, a standard phishing step.

**The fix.** Both automatic redirects are switched off; no real page depended on them.

## Overall limit on local callers

**What it was.** The setup and invitation limiters counted each caller and also kept an overall ceiling, but skipped the ceiling for callers connecting directly from the local network.

**Why it mattered.** Anything running on that network, including a compromised device, could hammer those endpoints without ever tripping the overall limit.

**The fix.** Every caller now counts toward the overall ceiling. One consequence to know: the setup limiter lives in memory, so restarting the service clears a ceiling that has been used up.

## Failed-login counting

**What it was.** The failed-login counter was keyed on the machine that handed the request to the server, which for tunnel traffic is one of three relays.

**Why it mattered.** A single attacker could use up the budget and block every real user behind that relay, and a user could not be told apart from an attacker.

**The fix.** The counter uses the visitor's real address, believed only when it comes from a listed relay or a verified Cloudflare sign-in. A request from anywhere else is counted by its own address, so a forged header cannot pick a counter.

## Allowed-peer list

**What it was.** The server listens on every network interface and answered anyone who could reach it.

**Why it mattered.** One mistaken port forward or firewall rule would have exposed the whole interface to the internet.

**The fix.** A new setting names the allowed networks. This computer itself and the tunnel relays are always allowed. Anything else is refused before routing, on every listener, including the secure, HTTP/3 and port-80 ones. The HTTP/3 secure handshake still completes before the refusal, since the refusal is an answer to a request.

## Browser script policy

**What it was.** The web app was served with no instruction telling the browser which scripts it may run.

**Why it mattered.** If a bug ever let an attacker slip a script into a page, the browser would have run it with the user's login.

**The fix.** The server sends a content security policy. The one inline script in the page is allowed by a fingerprint worked out from the exact page being served, so editing it cannot leave the policy out of date. Styles still need to be allowed inline because the interface library creates them while running, and images may come from any secure address because metadata search shows cover pictures from outside providers.
