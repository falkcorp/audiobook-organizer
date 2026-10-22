<!-- file: docs/executive-summaries/2026-10-21-public-surface-hardening-executive-summary.md -->
<!-- version: 1.3.0 -->
<!-- guid: f48321db-42a2-44f8-a617-4c75ee6ae1ff -->
<!-- last-edited: 2026-10-21 -->

# The public-facing server is harder to abuse

Branch `fix/public-surface-hardening`. No separate spec. Drawn from an internal review of everything an outsider can reach through the Cloudflare tunnel; the Other Cloudflare-side findings from that review are handled separately and are not part of this change.

## Executive Summary

- **Pages no longer redirect using a caller-chosen prefix.** The server used to answer a request for `/path` with a redirect to `/path/` (and similar), building the new address partly from a header a caller could set. The redirect stayed on this site, so it could not send anyone to another one, but it let a caller pick part of the address. The server now never redirects for those cases, and a missed server address such as `/ping/` answers "not found" instead of the web app.
- **Direct local-network callers share the same overall limit as everyone.** The first-time setup page and the invitation pages had a rate limit with a hole: callers on the local network were exempt from the overall ceiling, so a flood from inside the network could not be stopped by it. The exemption is gone.
- **One person's failed logins no longer lock out everybody.** Behind the tunnel every visitor appeared to come from one of three relay machines, so the failed-login counter was shared. Now it counts each real visitor separately (and groups an IPv6 visitor by their /64 block so rotating addresses does not reset it).
- **The server can refuse strangers outright.** A new setting lists the networks allowed to connect directly. Anyone else gets "forbidden" before the server looks at what they asked for. Left empty, nothing changes and the server logs a warning that it accepts anyone.
- **The web app now has a browser-enforced script policy.** Browsers are told to run only the app's own scripts, plus one small theme script allowed by its fingerprint. Injected script would be refused. A second header turns off camera, microphone, location, payment and USB access.
- **The shared guest key is gone.** Invitations used to hand every guest the same Cloudflare pass, in the email and on the invitation page. Anyone holding a live invitation link could read it. It is removed. Guests now get through Cloudflare by signing in with their own email address, which is added to the guest list when they accept. For now guests use a browser only: the phone-app steps are removed from the email and page, which say phone app access will be added to the account soon (a separate change gives each guest their own phone-app pass).
- **Checked against the real thing.** The web app was loaded in a real browser against the finished program, and no policy violations appeared on the main pages.

## Redirects

**What it was.** The web framework's default is to "fix" a missing or extra trailing slash by redirecting, using a prefix header the caller can supply.

**Why it mattered.** A caller could choose part of the address the redirect pointed to on this site, on pages that skip the Cloudflare sign-in. The redirect never left this site, so the risk was a header-controlled path rather than a jump to another site.

**The fix.** The automatic trailing-slash redirect is switched off, and so is the case-fixing one (which was not doing anything here). A request that misses a server address by a slash, a doubled slash or letter case now answers "not found", never the web app page. Nothing was checked about whether any client relied on the old redirect; a test sends every registered address in its other slash form and confirms no redirect.

## Overall limit on local callers

**What it was.** The setup and invitation limiters counted each caller and also kept an overall ceiling, but skipped the ceiling for callers connecting directly from the local network.

**Why it mattered.** Anything running on that network, including a compromised device, could hammer those endpoints without ever tripping the overall limit.

**The fix.** Every caller now counts toward the overall ceiling. One consequence to know: the setup limiter lives in memory, so restarting the service clears a ceiling that has been used up.

## Failed-login counting

**What it was.** The failed-login counter was keyed on the machine that handed the request to the server, which for tunnel traffic is one of three relays.

**Why it mattered.** A single attacker could use up the budget and block every real user behind that relay, and a user could not be told apart from an attacker.

**The fix.** The counter uses the visitor's real address, believed only when it comes from a listed relay or a verified Cloudflare sign-in. A request from anywhere else is counted by its own address, so a forged header cannot pick a counter. Because one visitor with many addresses would still get a fresh allowance per address, password login also has overall limits on failed attempts, per relay and for the whole service. Reaching them does not lock everyone out: people signed in through Cloudflare are exempt, and anyone else is simply slowed by a second or more before their password is checked, and only refused if their own account has already failed five times. The remaining cost is that, while the limits are used up, someone guessing at one account can get that account's password login refused for up to 15 minutes. People already signed in are not affected, and a restart clears it.

## Allowed-peer list

**What it was.** The server listens on every network interface and answered anyone who could reach it.

**Why it mattered.** One mistaken port forward or firewall rule would have exposed the whole interface to the internet.

**The fix.** A new setting names the allowed networks. This computer itself (loopback) and the tunnel relays are always allowed; the computer's own network address is allowed only if the list covers it. Anything else is refused before routing, on every listener, including the secure, HTTP/3 and port-80 ones. A mistyped entry stops the server starting instead of being skipped, and the effective list is logged. HTTP/3's early-data mode is switched off so a refused caller cannot present a faked address. The HTTP/3 secure handshake still completes before the refusal, since the refusal is an answer to a request.

## Shared guest pass

**What it was.** Every invitation carried the same pair of Cloudflare credentials (a service token) that let a phone app past Cloudflare's front door. The invitation page showed them and the email repeated them.

**Why it mattered.** The pair was identical for every guest and lived as long as the token did, so one forwarded email or one leaked invitation link exposed a standing way past the front door for everyone.

**The fix.** The pair is removed from the page, the email, the admin views and the settings, with no fallback. Accepting an invitation adds the guest's email address to Cloudflare's guest list, and the page and email tell the guest to sign in to Cloudflare with that address and the one-time code it sends, only when that will actually happen for this invitation. Guests are browser-only for now, so the phone-app steps are replaced by one line saying phone app access will be added soon. A server still carrying the old settings logs a warning and ignores them. The old token should be revoked in Cloudflare.

## Browser script policy

**What it was.** The web app was served with no instruction telling the browser which scripts it may run.

**Why it mattered.** If a bug ever let an attacker slip a script into a page, the browser would have run it with the user's login.

**The fix.** The server sends a content security policy. The one inline script in the page is allowed by a fingerprint worked out from the exact page being served, so editing it cannot leave the policy out of date. Styles still need to be allowed inline because the interface library creates them while running, and images may come from any secure address because metadata search shows cover pictures from outside providers.
