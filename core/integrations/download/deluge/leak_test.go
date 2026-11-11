// file: core/integrations/download/deluge/leak_test.go
// version: 1.0.1
// guid: a47e2b90-5c13-4d68-8f0a-9e1d7c3b6254
// last-edited: 2026-11-11

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
)

// echoPassword has characters every encoding changes: a quote, an ampersand,
// angle brackets, a slash, a plus and a space. No four-character window of it
// occurs in any error text the adapter writes itself.
const echoPassword = `Gotham&Co "Bat" <cave>/9+W`

// encodings is echoPassword as a server would echo it back.
func encodings() map[string]string {
	j, _ := json.Marshal(echoPassword)
	return map[string]string{
		"plain":        echoPassword,
		"JSON-escaped": strings.Trim(string(j), `"`),
		"HTML-escaped": html.EscapeString(echoPassword),
		"URL-encoded":  url.QueryEscape(echoPassword),
		"URL path":     url.PathEscape(echoPassword),
	}
}

// leaks reports a fragment of 4 or more characters of echoPassword in text,
// or an encoded form of it in whole.
func leaks(text string) string {
	for i := 0; i+4 <= len(echoPassword); i++ {
		if frag := echoPassword[i : i+4]; strings.Contains(text, frag) {
			return frag
		}
	}
	for _, form := range encodings() {
		if strings.Contains(text, form) {
			return form
		}
	}
	return ""
}

// errorTexts is everything an error says: the Error text, every wrapped error,
// the typed errors' kept text and an integrations.Error's Detail.
func errorTexts(err error) []string {
	out := collectErrorText(err)
	if ie, ok := errors.AsType[*integrations.Error](err); ok {
		out = append(out, ie.Error(), ie.Detail)
	}
	return out
}

func echoInstance(t *testing.T, answer func(w http.ResponseWriter, body string)) *instance {
	t.Helper()
	ui := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		answer(w, string(b))
	}))
	t.Cleanup(ui.Close)
	c, err := newRPC(ui.URL, plainHTTP{ui.Client()}, staticSecret{pw: echoPassword})
	if err != nil {
		t.Fatal(err)
	}
	return &instance{rpc: c, ref: integrations.Ref{Kind: integrations.KindDownload, Instance: "deluge"}}
}

// A login whose answer echoes the password, in any encoding, keeps no text of
// that answer in any error.
func TestLogin_EchoedPasswordIsNeverKept(t *testing.T) {
	for enc, echo := range encodings() {
		for shape, answer := range map[string]func(w http.ResponseWriter){
			"HTTP 500": func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "boom "+echo)
			},
			"HTTP 307": func(w http.ResponseWriter) {
				w.Header().Set("Location", "http://192.0.2.1/?pw="+url.QueryEscape(echo))
				w.WriteHeader(http.StatusTemporaryRedirect)
				_, _ = io.WriteString(w, "moved "+echo)
			},
			"not JSON": func(w http.ResponseWriter) { _, _ = io.WriteString(w, "echo "+echo) },
			"RPC error": func(w http.ResponseWriter) {
				m, _ := json.Marshal("bad login " + echo)
				_, _ = io.WriteString(w, `{"id":1,"result":null,"error":{"code":4,"message":`+string(m)+`}}`)
			},
		} {
			t.Run(enc+"/"+shape, func(t *testing.T) {
				inst := echoInstance(t, func(w http.ResponseWriter, _ string) { answer(w) })
				_, rawErr := inst.rpc.callAuthed(context.Background(), "web.connected")
				_, itemsErr := inst.Items(context.Background(), download.ItemFilter{})
				for _, err := range []error{rawErr, itemsErr} {
					if err == nil {
						t.Fatal("no error")
					}
					for _, text := range errorTexts(err) {
						if frag := leaks(text); frag != "" {
							t.Errorf("error text %q leaks %q", text, frag)
						}
					}
					// No text of a login answer is kept at all.
					if st, ok := errors.AsType[*statusError](err); ok && st.Excerpt != "" {
						t.Errorf("a login status error kept %q", st.Excerpt)
					}
					if de, ok := errors.AsType[*decodeError](err); ok {
						if de.Excerpt != "" {
							t.Errorf("a login decode error kept %q", de.Excerpt)
						}
						if de.Err.Error() != "not a JSON-RPC document" {
							t.Errorf("a login decode error kept the parser's text %q", de.Err)
						}
					}
					if re, ok := errors.AsType[*rpcError](err); ok && re.Message != "" {
						t.Errorf("a login RPC error kept %q", re.Message)
					}
				}
			})
		}
	}
}

// After a successful login, a method's error excerpt is scrubbed over the whole
// body and cut afterwards: a password that straddles the 200-byte cut, in any
// encoding, leaves no fragment.
func TestCall_PasswordStraddlingTheExcerptCutIsScrubbed(t *testing.T) {
	for enc, echo := range encodings() {
		for _, offset := range []int{150, 190, 198, 199} {
			for shape, answer := range map[string]func(w http.ResponseWriter, body string){
				"HTTP 500": func(w http.ResponseWriter, body string) {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, body)
				},
				"not JSON": func(w http.ResponseWriter, body string) { _, _ = io.WriteString(w, body) },
				"RPC error": func(w http.ResponseWriter, body string) {
					m, _ := json.Marshal(body)
					_, _ = io.WriteString(w, `{"id":1,"result":null,"error":{"code":4,"message":`+string(m)+`}}`)
				},
			} {
				t.Run(enc+"/"+shape, func(t *testing.T) {
					body := strings.Repeat("x", offset) + echo + strings.Repeat("y", 100)
					inst := echoInstance(t, func(w http.ResponseWriter, req string) {
						if strings.Contains(req, `"auth.login"`) {
							_, _ = io.WriteString(w, `{"id":1,"result":true,"error":null}`)
							return
						}
						answer(w, body)
					})
					_, rawErr := inst.rpc.callAuthed(context.Background(), "web.connected")
					_, itemsErr := inst.Items(context.Background(), download.ItemFilter{})
					for _, err := range []error{rawErr, itemsErr} {
						if err == nil {
							t.Fatal("no error")
						}
						texts := errorTexts(err)
						for _, text := range texts {
							if frag := leaks(text); frag != "" {
								t.Errorf("offset %d: %q leaks %q", offset, text, frag)
							}
						}
						// The scrub left its marker where the password was, when the
						// excerpt reaches that far (the marker itself is cut at the limit).
						if st, ok := errors.AsType[*statusError](err); ok && offset+len("[redacted]") <= excerptLimit && !strings.Contains(st.Excerpt, "[redacted]") {
							t.Errorf("offset %d: excerpt %q has no redaction marker", offset, st.Excerpt)
						}
					}
				})
			}
		}
	}
}

// The password is read to scrub with; when it cannot be read no answer text is
// kept at all.
func TestScrubbed_FailsClosedWithoutThePassword(t *testing.T) {
	c, err := newRPC("http://192.0.2.1:8112", plainHTTP{http.DefaultClient}, staticSecret{err: errors.New("withheld")})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.keptExcerpt(context.Background(), "web.connected", []byte("anything")); got != "" {
		t.Errorf("kept %q without a password to scrub with", got)
	}
	if got := c.keptText(context.Background(), "web.connected", "anything"); got != "" {
		t.Errorf("kept %q without a password to scrub with", got)
	}
}

func TestScrubSecret_Forms(t *testing.T) {
	for enc, echo := range encodings() {
		if got := scrubSecret("a "+echo+" b", echoPassword); strings.Contains(got, echo) {
			t.Errorf("%s form survived: %q", enc, got)
		}
	}
	// A lowercase percent-encoding and an HTML escape of the JSON form.
	j, _ := json.Marshal(echoPassword)
	for name, form := range map[string]string{
		"lowercase percent": strings.NewReplacer("%2F", "%2f", "%26", "%26", "%3C", "%3c", "%3E", "%3e", "%22", "%22", "%2B", "%2b").Replace(url.QueryEscape(echoPassword)),
		"HTML of JSON":      html.EscapeString(strings.Trim(string(j), `"`)),
	} {
		if got := scrubSecret(form, echoPassword); leaks(got) != "" {
			t.Errorf("%s form survived: %q", name, got)
		}
	}
	// A form that is the prefix of a longer form (a trailing ampersand and its
	// entity) must not leave the longer form's tail behind.
	if got := scrubSecret("x"+html.EscapeString("Robin&")+"y", "Robin&"); got != "x[redacted]y" {
		t.Errorf("a prefix form left a tail: %q", got)
	}
	if got := scrubSecret("nothing here", ""); got != "nothing here" {
		t.Errorf("an empty secret changed the text: %q", got)
	}
}

// The excerpt a later decode failure keeps (a result that is not the shape the
// adapter reads) is scrubbed the same way, over the whole result.
func TestDecodeFailureOfAResultIsScrubbed(t *testing.T) {
	for enc, echo := range encodings() {
		t.Run(enc, func(t *testing.T) {
			result, _ := json.Marshal(strings.Repeat("x", 185) + echo + strings.Repeat("y", 100))
			inst := echoInstance(t, func(w http.ResponseWriter, req string) {
				if strings.Contains(req, `"auth.login"`) {
					_, _ = io.WriteString(w, `{"id":1,"result":true,"error":null}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":1,"result":`+string(result)+`,"error":null}`)
			})
			_, err := inst.Items(context.Background(), download.ItemFilter{})
			if _, ok := errors.AsType[*decodeError](err); !ok {
				t.Fatalf("err = %v; want a decodeError", err)
			}
			for _, text := range errorTexts(err) {
				if frag := leaks(text); frag != "" {
					t.Errorf("error text %q leaks %q", text, frag)
				}
			}
		})
	}
}
