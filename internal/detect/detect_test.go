package detect

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsVideoHost(t *testing.T) {
	cases := map[string]bool{
		"https://www.youtube.com/watch?v=x": true,
		"https://youtu.be/x":                true,
		"https://vimeo.com/123":             true,
		"https://www.tiktok.com/@a/video/1": true,
		"https://example.com/video.mp4":     false,
		"https://example.com/page":          false,
		"not a url":                         false,
	}
	for u, want := range cases {
		if got := IsVideoHost(u); got != want {
			t.Fatalf("IsVideoHost(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestClassifyDirectFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "12345")
		w.Header().Set("Content-Disposition", `attachment; filename="clip.mp4"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	kind, head, err := Classify(srv.URL + "/clip.mp4")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if kind != KindFile {
		t.Fatalf("kind = %q, want file", kind)
	}
	if head.Filename != "clip.mp4" || head.Size != 12345 {
		t.Fatalf("head = %+v", head)
	}
}

func TestClassifyHTMLPageFallsBackToVideoProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html></html>"))
	}))
	defer srv.Close()

	kind, _, err := Classify(srv.URL + "/watch")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if kind != KindVideo {
		t.Fatalf("kind = %q, want video (yt-dlp probe decides)", kind)
	}
}

func TestClassifyVideoHostSkipsHEAD(t *testing.T) {
	// A YouTube URL must be classified without any network call.
	kind, head, err := Classify("https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if kind != KindVideo || head != nil {
		t.Fatalf("kind = %q head = %+v", kind, head)
	}
}

func TestClassifyHeadBlockedFileExtension(t *testing.T) {
	// Server refuses HEAD with 403, but the path is clearly a file.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	kind, head, err := Classify(srv.URL + "/clip.mp4")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if kind != KindFile {
		t.Fatalf("kind = %q, want file (extension fallback)", kind)
	}
	if head == nil || head.Filename != "clip.mp4" {
		t.Fatalf("head = %+v", head)
	}
	// Same server, no file extension → yt-dlp probe fallback.
	kind, _, err = Classify(srv.URL + "/watch")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if kind != KindVideo {
		t.Fatalf("kind = %q, want video", kind)
	}
}

func TestGuessFilename(t *testing.T) {
	if got := GuessFilename("https://cdn.example.com/a/b/report.pdf?token=1", nil); got != "report.pdf" {
		t.Fatalf("filename = %q", got)
	}
	head := &HeadResult{Filename: "from-header.zip"}
	if got := GuessFilename("https://example.com/x", head); got != "from-header.zip" {
		t.Fatalf("filename = %q", got)
	}
}
