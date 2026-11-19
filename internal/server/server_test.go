package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/irfan420x/nova-downloader/internal/novaapi"
	"github.com/irfan420x/nova-downloader/internal/queue"
)

func fakeAPIServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/tiktok":
			_, _ = w.Write([]byte(`{
				"title": "TikTok Test", "thumbnail": "https://example.com/t.jpg", "source": "tiktok",
				"qualities": [
					{"id": "tiktok-720p", "label": "720p", "ext": "mp4",
					 "url": "https://v19-webapp-prime.us.tiktok.com/v.mp4",
					 "alternates": ["https://v16-webapp-prime.us.tiktok.com/v.mp4"],
					 "headers": {"Referer": "https://www.tiktok.com/", "Cookie": "ttwid=x"}, "size": 1000},
					{"id": "tiktok-720p-vo", "label": "720p", "ext": "mp4",
					 "url": "https://v19-webapp-prime.us.tiktok.com/v-only.mp4",
					 "headers": {}, "videoOnly": true, "size": 900},
					{"id": "tiktok-audio", "label": "audio", "ext": "m4a",
					 "url": "https://v19-webapp-prime.us.tiktok.com/a.m4a",
					 "headers": {}, "audioOnly": true, "size": 100}
				]}`))
		case "/direct":
			_, _ = w.Write([]byte(`{
				"title": "file.pdf", "source": "direct",
				"qualities": [
					{"id": "direct-file", "label": "pdf", "ext": "pdf",
					 "url": "https://example.com/file.pdf", "headers": {}, "size": 4242}
				]}`))
		case "/facebook":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"code":"BLOCKED","message":"Facebook blocked this request"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND","message":"no such endpoint"}}`))
		}
	}))
}

func testServer(t *testing.T, apiBase string) *Server {
	t.Helper()
	mgr := queue.New(t.TempDir(), 2, "", nil)
	return New(mgr, Settings{OutDir: t.TempDir(), MaxConcurrent: 2, ApiBaseURL: apiBase}, t.TempDir())
}

func TestProbeURLAPI(t *testing.T) {
	srv := fakeAPIServer(t)
	defer srv.Close()
	s := testServer(t, srv.URL)

	pr, err := s.probeURL(context.Background(), "https://www.tiktok.com/@x/video/123")
	if err != nil {
		t.Fatalf("probeURL: %v", err)
	}
	if pr.Engine != "api" || pr.Platform != "tiktok" || pr.Kind != "video" {
		t.Fatalf("probe = %+v", pr)
	}
	if pr.Title != "TikTok Test" {
		t.Fatalf("title = %q", pr.Title)
	}
	if pr.SubtitlesSupported {
		t.Fatal("API probes must not offer subtitles")
	}
	if len(pr.Combined) != 1 || pr.Combined[0].ID != "tiktok-720p" {
		t.Fatalf("combined = %+v", pr.Combined)
	}
	if pr.Combined[0].StreamURL != "https://v19-webapp-prime.us.tiktok.com/v.mp4" {
		t.Fatalf("stream URL lost: %+v", pr.Combined[0])
	}
	if pr.Combined[0].StreamHeaders["Cookie"] != "ttwid=x" {
		t.Fatalf("Cookie header lost: %+v", pr.Combined[0].StreamHeaders)
	}
	if len(pr.Combined[0].Alternates) != 1 {
		t.Fatalf("alternates lost: %+v", pr.Combined[0])
	}
	if pr.Combined[0].Height != 720 {
		t.Fatalf("height = %d, want 720", pr.Combined[0].Height)
	}
	if len(pr.VideoOnly) != 1 || len(pr.AudioOnly) != 1 {
		t.Fatalf("videoOnly=%d audioOnly=%d", len(pr.VideoOnly), len(pr.AudioOnly))
	}
}

func TestProbeURLAPIDirect(t *testing.T) {
	srv := fakeAPIServer(t)
	defer srv.Close()
	s := testServer(t, srv.URL)

	pr, err := s.probeURL(context.Background(), "https://example.com/file.pdf")
	if err != nil {
		t.Fatalf("probeURL: %v", err)
	}
	if pr.Kind != "file" || pr.Engine != "api" || pr.Filename != "file.pdf" {
		t.Fatalf("probe = %+v", pr)
	}
	if pr.Size != 4242 || pr.ContentTyp != "application/pdf" {
		t.Fatalf("size/type = %d %q", pr.Size, pr.ContentTyp)
	}
	if len(pr.Streams) != 1 || pr.Streams[0].StreamURL != "https://example.com/file.pdf" {
		t.Fatalf("streams = %+v", pr.Streams)
	}
}

func TestProbeURLAPIErrorHonest(t *testing.T) {
	srv := fakeAPIServer(t)
	defer srv.Close()
	s := testServer(t, srv.URL)

	_, err := s.probeURL(context.Background(), "https://www.facebook.com/reel/1121767350734551/")
	if err == nil {
		t.Fatal("expected the API BLOCKED error")
	}
	apiErr, ok := err.(*novaapi.APIError)
	if !ok || apiErr.Code != "BLOCKED" {
		t.Fatalf("error = %T %v, want APIError BLOCKED", err, err)
	}
	if !strings.Contains(err.Error(), "Facebook blocked") {
		t.Fatalf("error lost the API message: %v", err)
	}
}

func TestProbeURLDirectFallsBackLocallyWhenAPIUnreachable(t *testing.T) {
	// API base points at a dead port: a direct file must still
	// resolve via the local HEAD path instead of failing outright.
	s := testServer(t, "http://127.0.0.1:1")
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Length", "4242")
		w.Header().Set("Content-Disposition", `attachment; filename="file.pdf"`)
	}))
	defer fileSrv.Close()

	pr, err := s.probeURL(context.Background(), fileSrv.URL+"/file.pdf")
	if err != nil {
		t.Fatalf("probeURL: %v", err)
	}
	if pr.Kind != "file" || pr.Engine != "file" || pr.Filename != "file.pdf" {
		t.Fatalf("probe = %+v (want local file fallback)", pr)
	}
}

func TestSafeFilename(t *testing.T) {
	cases := []struct{ title, ext, want string }{
		{"Big Buck Bunny", "mp4", "Big Buck Bunny.mp4"},
		{"a/b:c*d?e\"f<g>h|i", "mp4", "a_b_c_d_e_f_g_h_i.mp4"},
		{"", "mp4", "download.mp4"},
		{"already.mp4", "mp4", "already.mp4"},
		{"  spaced  ", "", "spaced"},
	}
	for _, c := range cases {
		if got := SafeFilename(c.title, c.ext); got != c.want {
			t.Errorf("SafeFilename(%q, %q) = %q, want %q", c.title, c.ext, got, c.want)
		}
	}
	long := strings.Repeat("x", 300)
	if got := SafeFilename(long, "mp4"); len(got) > 190 {
		t.Errorf("long filename not capped: %d bytes", len(got))
	}
}

func TestAPIQualityMapping(t *testing.T) {
	f := apiQualityToFormat(novaapi.Quality{ID: "yt-1080p", Label: "1080p", Ext: "mp4", Size: 100})
	if f.Height != 1080 || f.VCodec == "none" || f.ACodec == "none" {
		t.Fatalf("combined mapping wrong: %+v", f)
	}
	vo := apiQualityToFormat(novaapi.Quality{ID: "v", Label: "720p", Ext: "mp4", VideoOnly: true})
	if !vo.HasVideo() || vo.HasAudio() {
		t.Fatalf("video-only mapping wrong: %+v", vo)
	}
	ao := apiQualityToFormat(novaapi.Quality{ID: "a", Label: "aac 160k", Ext: "m4a", AudioOnly: true})
	if ao.HasVideo() || !ao.HasAudio() || ao.TBR == nil || *ao.TBR != 160 {
		t.Fatalf("audio mapping wrong: %+v", ao)
	}
	if got := parseHeight("1080p60"); got != 1080 {
		t.Fatalf("parseHeight 1080p60 = %d", got)
	}
	if got := parseHeight("audio"); got != 0 {
		t.Fatalf("parseHeight audio = %d", got)
	}
	var decoded struct {
		Engine string `json:"engine"`
	}
	_ = json.Unmarshal([]byte(`{"engine":"api"}`), &decoded)
}

// An HLS playlist must save as media (.mp4), never ".m3u8" — ffmpeg
// treats a .m3u8 output as "write a playlist + segments" and litters
// the download folder with .ts files (seen live with SoundCloud).
func TestOutputExt(t *testing.T) {
	cases := []struct {
		ext, want string
		audioOnly bool
	}{
		{"mp4", "mp4", false},
		{"webm", "webm", false},
		{"m3u8", "mp4", false},
		{"M3U8", "mp4", false},
		{"m4a", "mp3", true},
		{"m3u8", "mp3", true},
	}
	for _, c := range cases {
		if got := outputExt(c.ext, c.audioOnly); got != c.want {
			t.Errorf("outputExt(%q, audioOnly=%v) = %q, want %q", c.ext, c.audioOnly, got, c.want)
		}
	}
}

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
