// file: internal/transcribe/canary_helpers_test.go
// version: 1.0.0
// guid: 470fe68d-fa44-466d-b34f-e3d795b948fa
// last-edited: 2026-10-27

package transcribe

// Questions about the canary memory that only the tests ask. They lived in
// canary.go with no production caller (N4 of the review of f241ec20a): the
// dispatcher asks endpointRemembered, and forgetCanaryIf decides what is
// forgotten.

// failedCanaryBefore reports whether key itself is remembered. A dispatch asks
// endpointRemembered instead: a machine's worker does not keep one key.
func failedCanaryBefore(key string) bool {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	return canaryFailed.m[key] != nil
}

// clearCanaryFailed drops key's entry whatever its generation.
func clearCanaryFailed(key string) {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	delete(canaryFailed.m, key)
}

// hasUnheardPort is canaryEntry.hasUnheardPort for the entry of key, under the
// lock; false when key is not remembered.
func hasUnheardPort(key string, refused []gpuRefusal, kept []gatedEndpoint) bool {
	canaryFailed.mu.Lock()
	defer canaryFailed.mu.Unlock()
	e := canaryFailed.m[key]
	return e != nil && e.hasUnheardPort(key, refused, kept)
}
