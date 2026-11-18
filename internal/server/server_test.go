package server

import (
	"errors"
	"strings"
	"testing"
)

// Error texts are verbatim (trimmed) from the 2026-11-19 platform probe
// with yt-dlp 2026.10.26 — see platform-probe-2026-11-19.md.
func TestProbeErrorHint(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // "" means no hint
	}{
		{
			name: "vimeo needs login",
			err: errors.New("ERROR: [vimeo] 1084537: The web client only works when logged-in. " +
				"Use --cookies, --cookies-from-browser, --username and --password, --netrc-cmd, " +
				"or --netrc (vimeo) to provide account credentials."),
			want: "Hint: this site needs login — add your cookies.txt in Settings and try again.",
		},
		{
			name: "youtube bot-check is a login hint",
			err: errors.New("ERROR: [youtube] aqz-KE-bpKQ: Sign in to confirm you're not a bot. " +
				"Use --cookies-from-browser or --cookies for the authentication."),
			want: "Hint: this site needs login — add your cookies.txt in Settings and try again.",
		},
		{
			name: "dailymotion transport failure",
			err: errors.New("ERROR: [dailymotion] x7tgad0: Unable to download JSON metadata: " +
				"Remote end closed connection without response " +
				"(caused by TransportError('Remote end closed connection without response'))"),
			want: "Hint: the site could not be reached from this network — try again later, or add cookies.txt in Settings.",
		},
		{
			name: "reddit truncated response",
			err: errors.New("ERROR: [Reddit] mnouq4: mnouq4: Error reading response: 49152 bytes read, " +
				"140756 more expected (caused by <IncompleteRead: 49152 bytes read, 140756 more expected>)"),
			want: "Hint: the site could not be reached from this network — try again later, or add cookies.txt in Settings.",
		},
		{
			name: "threads unsupported url",
			err:  errors.New("ERROR: Unsupported URL: https://www.threads.com/@zuck/post/DZpPDXbCeTt"),
			want: "Hint: this link is not supported by the current engine — update yt-dlp from Settings and try again.",
		},
		{
			name: "unrelated error gets no hint",
			err:  errors.New("ERROR: [youtube] aqz-KE-bpKQ: Video unavailable"),
			want: "",
		},
		{
			name: "nil error gets no hint",
			err:  nil,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := probeErrorHint(c.err); got != c.want {
				t.Errorf("probeErrorHint(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

func TestWithProbeHintKeepsRawError(t *testing.T) {
	raw := errors.New("ERROR: [vimeo] 1084537: The web client only works when logged-in.")
	wrapped := withProbeHint(raw)
	if !errors.Is(wrapped, raw) {
		t.Errorf("withProbeHint should wrap the raw error with %%w, got %v", wrapped)
	}
	if !strings.HasPrefix(wrapped.Error(), raw.Error()) {
		t.Errorf("wrapped error should start with the raw error, got %q", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), "Hint: this site needs login") {
		t.Errorf("wrapped error should contain the login hint, got %q", wrapped.Error())
	}

	plain := errors.New("ERROR: [youtube] aqz-KE-bpKQ: Video unavailable")
	if got := withProbeHint(plain); got != plain {
		t.Errorf("withProbeHint with no hint should return the error unchanged, got %v", got)
	}
}
