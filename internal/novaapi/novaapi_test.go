package novaapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectPlatform(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":                "youtube",
		"https://youtu.be/dQw4w9WgXcQ":                               "youtube",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ":                  "youtube",
		"https://music.youtube.com/watch?v=dQw4w9WgXcQ":              "youtube",
		"https://www.youtube-nocookie.com/embed/x":                   "youtube",
		"https://www.tiktok.com/@zachking/video/6768504823336815877": "tiktok",
		"https://vm.tiktok.com/abc123/":                              "tiktok",
		"https://www.facebook.com/reel/1121767350734551/":            "facebook",
		"https://fb.watch/abc/":                                      "facebook",
		"https://m.facebook.com/watch/?v=123":                        "facebook",
		"https://x.com/SpaceX/status/2121715216597827756":            "x",
		"https://twitter.com/nasa/status/123":                        "x",
		"https://soundcloud.com/marion-1/marion-over-the-clouds":     "soundcloud",
		"https://on.soundcloud.com/abc":                              "soundcloud",
		"https://example.com/stream/master.m3u8":                     "hls",
		"https://example.com/MASTER.M3U8?token=1":                    "hls",
		"https://example.com/file.mp4":                               "direct",
		"https://example.com/image.jpg":                              "direct",
		"https://www.instagram.com/reel/abc/":                        "direct", // detect says direct; routing sends it to yt-dlp
		"not a url":                                                  "direct",
	}
	for in, want := range cases {
		if got := DetectPlatform(in); got != want {
			t.Errorf("DetectPlatform(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsFallbackHost(t *testing.T) {
	yes := []string{
		"https://www.instagram.com/reel/DewzwXOAREy/",
		"https://www.pinterest.com/pin/1026891152548201938/",
		"https://www.twitch.tv/videos/2912355720",
		"https://www.reddit.com/r/videos/comments/abc/title/",
		"https://vimeo.com/76979871",
		"https://www.dailymotion.com/video/x7tgad0",
	}
	for _, u := range yes {
		if !IsFallbackHost(u) {
			t.Errorf("IsFallbackHost(%q) = false, want true", u)
		}
	}
	no := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.tiktok.com/@x/video/1",
		"https://example.com/file.mp4",
	}
	for _, u := range no {
		if IsFallbackHost(u) {
			t.Errorf("IsFallbackHost(%q) = true, want false", u)
		}
	}
}

func TestIsPlaylistURL(t *testing.T) {
	yes := []string{
		"https://www.youtube.com/playlist?list=PL123",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL123",
		"https://www.youtube.com/@BlenderFoundation",
		"https://www.youtube.com/channel/UC123",
		"https://www.youtube.com/c/SomeChannel",
	}
	for _, u := range yes {
		if !IsPlaylistURL(u) {
			t.Errorf("IsPlaylistURL(%q) = false, want true", u)
		}
	}
	no := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://www.tiktok.com/@x/video/1",
	}
	for _, u := range no {
		if IsPlaylistURL(u) {
			t.Errorf("IsPlaylistURL(%q) = true, want false", u)
		}
	}
}

const fakeAPIResponse = `{
  "title": "Test Video",
  "thumbnail": "https://example.com/thumb.jpg",
  "source": "tiktok",
  "qualities": [
    {"id": "tiktok-1080p", "label": "1080p", "ext": "mp4",
     "url": "https://v19-webapp-prime.us.tiktok.com/video.mp4",
     "alternates": ["https://v16-webapp-prime.us.tiktok.com/video.mp4"],
     "headers": {"User-Agent": "Mozilla/5.0", "Referer": "https://www.tiktok.com/", "Cookie": "ttwid=abc; tt_csrf_token=def"},
     "size": 1995631},
    {"id": "tiktok-audio", "label": "audio", "ext": "m4a",
     "url": "https://v19-webapp-prime.us.tiktok.com/audio.m4a",
     "headers": {"User-Agent": "Mozilla/5.0"},
     "audioOnly": true, "size": 123456}
  ]
}`

func TestResolveSuccess(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeAPIResponse))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Resolve(context.Background(), "https://www.tiktok.com/@zachking/video/6768504823336815877")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotPath != "/tiktok" {
		t.Fatalf("endpoint = %q, want /tiktok", gotPath)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil || sent["url"] == "" {
		t.Fatalf("request body = %q", gotBody)
	}
	if res.Title != "Test Video" || res.Platform != "tiktok" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Qualities) != 2 {
		t.Fatalf("qualities = %d, want 2", len(res.Qualities))
	}
	q := res.Qualities[0]
	// Header passthrough: the cookies + Referer the CDN gates need.
	if q.Headers["Cookie"] != "ttwid=abc; tt_csrf_token=def" {
		t.Fatalf("Cookie header lost: %v", q.Headers)
	}
	if q.Headers["Referer"] != "https://www.tiktok.com/" {
		t.Fatalf("Referer header lost: %v", q.Headers)
	}
	if len(q.Alternates) != 1 || !strings.Contains(q.Alternates[0], "v16-webapp-prime") {
		t.Fatalf("alternates lost: %v", q.Alternates)
	}
	if !q.Combined() || q.HasVideo() != true || q.HasAudio() != true {
		t.Fatalf("combined flags wrong: %+v", q)
	}
	if got := res.BestAudio(); got == nil || got.ID != "tiktok-audio" {
		t.Fatalf("BestAudio = %+v", got)
	}
	if got := res.Find("tiktok-1080p"); got == nil || got.Label != "1080p" {
		t.Fatalf("Find failed: %+v", got)
	}
	if res.Find("nope") != nil {
		t.Fatal("Find(nonexistent) should be nil")
	}
}

func TestResolveErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"BLOCKED","message":"Facebook blocked this request"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Resolve(context.Background(), "https://www.facebook.com/reel/1121767350734551/")
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != "BLOCKED" || apiErr.Status != 502 {
		t.Fatalf("APIError = %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "Facebook blocked") {
		t.Fatalf("error message lost the API text: %v", err)
	}
}

func TestResolveHTTPErrorWithoutJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Resolve(context.Background(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != "UPSTREAM_ERROR" {
		t.Fatalf("error = %T %v, want UPSTREAM_ERROR", err, err)
	}
}

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Errorf("health path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	if err := NewClient(srv.URL).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if err := NewClient(bad.URL).Health(context.Background()); err == nil {
		t.Fatal("Health on 500 should fail")
	}
}
