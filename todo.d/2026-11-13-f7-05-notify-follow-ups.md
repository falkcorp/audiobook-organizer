- [ ] **TODO-F7-05-SMTP-DIAL-GUARD** (hard precondition of the F7-09 flip) The `core.email` channel is admitted by the
      integrations gate but its SMTP dial has no egress guard: providerhttp's guard
      wraps HTTP only, so an SMTP host that resolves to a private address is not
      refused. Add a dial hook an adapter can use (F7-01 or providerhttp) and use it
      in `core/integrations/notify/mail`. Acceptance: an email instance whose host
      is a private address without `Local` is refused before any connection, with a
      test beside `TestEmail_GateRefusalIsReturnedBeforeAnyConnection`. Seams note:
      `docs/agent-tasks/platform-core/seams.d/F7-05.md`.
- [ ] **TODO-F7-05-WEBHOOK-HOLD-POLICY** A transient 5xx from a notify webhook holds
      its providerhttp scope for 30 minutes (5 minutes for other transient answers),
      and retries inside the hold are refused locally. Decide whether a
      notification channel wants a notify-specific `HoldPolicy` in the webhook
      Descriptor, and make framing headers (Host, Content-Length) refused at Put,
      not only at build (F7-10).
- [ ] **TODO-F7-05-UNORDERED-RETRY-CAP** The outbox keeps at most 64 retrying
      entries per partition for an `Unordered()` consumer, so a downed notify channel
      with more than 64 retrying deliveries can head-block others on its partition.
      Decide whether to raise or remove the cap, or key deliveries more finely.
- [ ] **TODO-F7-05-IMPORTRULE-REFRESH** `NativeImporter`'s `ImportRule` writes the
      rule store but does not refresh a running `notify.Service`'s loaded rules
      (`Service.PutRule` does). F7-08 must reload after an import or build the
      Service after it. Acceptance: a rule imported while a Service runs routes the
      next notification without a restart, with a test.
