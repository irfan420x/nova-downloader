// Package novaapi is the client for the central NOVA Downloader API
// (https://nova-downloader-api.vercel.app): one endpoint per platform
// that resolves a public link into downloadable stream URLs + the
// headers the client must send per quality. The app downloads the bytes
// directly from the CDN itself — the API never proxies video.
//
// The API is the PRIMARY resolve engine. yt-dlp stays only as a
// fallback for platforms the API has no endpoint for (Instagram,
// Pinterest, Twitch, Reddit, …) — see IsFallbackHost.
//
// Cold starts: the first API call after idle can take ~49s (Vercel
// cold start), then ~2s. Resolve uses a 120s timeout and callers must
// show honest progress, never a fake instant failure.
package novaapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the production NOVA Downloader API.
const DefaultBaseURL = "https://nova-downloader-api.vercel.app"

// ResolveTimeout bounds one resolve call (cold start + slow platforms).
const ResolveTimeout = 120 * time.Second

// Platforms the API has an endpoint for.
const (
	PlatformYouTube    = "youtube"
	PlatformTikTok     = "tiktok"
	PlatformFacebook   = "facebook"
	PlatformX          = "x"
	PlatformSoundCloud = "soundcloud"
	PlatformDirect     = "direct"
	PlatformHLS        = "hls"
)

// Quality is one downloadable stream variant from the API.
type Quality struct {
	ID         string            `json:"id"`
	Label      string            `json:"label"`
	Ext        string            `json:"ext"`
	URL        string            `json:"url"`
	Alternates []string          `json:"alternates"`
	Headers    map[string]string `json:"headers"`
	Size       int64             `json:"size"`
	VideoOnly  bool              `json:"videoOnly"`
	AudioOnly  bool              `json:"audioOnly"`
}

// HasVideo reports whether the quality carries a video stream.
func (q Quality) HasVideo() bool { return !q.AudioOnly }

// HasAudio reports whether the quality carries an audio stream.
func (q Quality) HasAudio() bool { return !q.VideoOnly }

// Combined reports whether the quality is a single file with both
// video and audio.
func (q Quality) Combined() bool { return !q.VideoOnly && !q.AudioOnly }

// ResolveResult is a successful API resolve.
type ResolveResult struct {
	Title     string    `json:"title"`
	Thumbnail string    `json:"thumbnail"`
	Source    string    `json:"source"`
	Platform  string    `json:"-"`
	Qualities []Quality `json:"qualities"`
}

// Combined returns the combined (video+audio) qualities, in API order.
func (r *ResolveResult) Combined() []Quality {
	var out []Quality
	for _, q := range r.Qualities {
		if q.Combined() {
			out = append(out, q)
		}
	}
	return out
}

// VideoOnly returns the video-only qualities, in API order.
func (r *ResolveResult) VideoOnly() []Quality {
	var out []Quality
	for _, q := range r.Qualities {
		if q.VideoOnly {
			out = append(out, q)
		}
	}
	return out
}

// AudioOnly returns the audio-only qualities, in API order.
func (r *ResolveResult) AudioOnly() []Quality {
	var out []Quality
	for _, q := range r.Qualities {
		if q.AudioOnly {
			out = append(out, q)
		}
	}
	return out
}

// BestAudio returns the first (best) audio-only quality, or nil.
func (r *ResolveResult) BestAudio() *Quality {
	for i := range r.Qualities {
		if r.Qualities[i].AudioOnly {
			return &r.Qualities[i]
		}
	}
	return nil
}

// Find returns the quality with the given ID, or nil.
func (r *ResolveResult) Find(id string) *Quality {
	for i := range r.Qualities {
		if r.Qualities[i].ID == id {
			return &r.Qualities[i]
		}
	}
	return nil
}

// APIError is an honest error from the API: {error:{code, message}}.
// Code is one of UNSUPPORTED / NOT_FOUND / BLOCKED / UPSTREAM_ERROR.
type APIError struct {
	Code    string
	Message string
	Status  int // HTTP status of the API response
}

func (e *APIError) Error() string {
	return fmt.Sprintf("NOVA API %s: %s", e.Code, e.Message)
}

type apiErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// platformHosts mirrors the API's own detection (lib/detect.js).
// hostMatches allows subdomains, so "www.tiktok.com" matches "tiktok.com".
var platformHosts = map[string][]string{
	PlatformYouTube:    {"youtube.com", "youtu.be", "music.youtube.com", "youtube-nocookie.com"},
	PlatformTikTok:     {"tiktok.com", "vm.tiktok.com", "vt.tiktok.com"},
	PlatformFacebook:   {"facebook.com", "fb.watch", "fb.com"},
	PlatformX:          {"x.com", "twitter.com"},
	PlatformSoundCloud: {"soundcloud.com", "snd.sc", "on.soundcloud.com"},
}

func hostMatches(host string, allowed []string) bool {
	for _, a := range allowed {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// DetectPlatform returns the API endpoint for a URL: one of the
// Platform* constants. Unknown hosts are "direct" (the /direct
// endpoint handles direct files); a URL containing ".m3u8" is "hls".
// It does NOT decide whether the URL should go to yt-dlp instead —
// see IsFallbackHost / IsPlaylistURL for that routing.
func DetectPlatform(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return PlatformDirect
	}
	host := strings.ToLower(u.Hostname())
	for platform, hosts := range platformHosts {
		if hostMatches(host, hosts) {
			return platform
		}
	}
	if strings.Contains(strings.ToLower(rawURL), ".m3u8") {
		return PlatformHLS
	}
	return PlatformDirect
}

// fallbackHosts are video sites the API has NO endpoint for. Links
// there go to the yt-dlp fallback engine instead of the API.
var fallbackHosts = []string{
	"instagram.com", "pinterest.com", "pin.it",
	"twitch.tv", "reddit.com", "vimeo.com", "dailymotion.com",
	"bilibili.com", "odysee.com", "rumble.com",
	"threads.net", "threads.com",
}

// IsFallbackHost reports whether the URL belongs to a video site the
// API does not cover (yt-dlp fallback territory).
func IsFallbackHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return hostMatches(strings.ToLower(u.Hostname()), fallbackHosts)
}

// IsPlaylistURL reports whether the URL is a YouTube playlist,
// channel, or user page — content the API does not resolve (it
// resolves single videos). Those go to the yt-dlp fallback, which
// keeps playlist support working.
func IsPlaylistURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !hostMatches(host, platformHosts[PlatformYouTube]) {
		return false
	}
	if u.Query().Get("list") != "" {
		return true
	}
	p := strings.ToLower(u.Path)
	for _, prefix := range []string{"/@", "/channel/", "/c/", "/user/", "/playlist"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// Client calls the NOVA Downloader API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient builds a Client for baseURL ("" = DefaultBaseURL).
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: ResolveTimeout},
	}
}

// Health checks GET /api/health.
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("NOVA API unreachable: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("NOVA API health: HTTP %s", resp.Status)
	}
	return nil
}

// Resolve detects the platform from rawURL, calls the matching
// endpoint, and returns the qualities. The caller's context should
// allow up to ResolveTimeout — cold starts are slow, and a short
// timeout would report a healthy API as failed.
func (c *Client) Resolve(ctx context.Context, rawURL string) (*ResolveResult, error) {
	platform := DetectPlatform(rawURL)
	ctx, cancel := context.WithTimeout(ctx, ResolveTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"url": rawURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/"+platform, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("NOVA API %s request failed: %w", platform, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("NOVA API %s: unreadable response: %w", platform, err)
	}
	if resp.StatusCode != http.StatusOK {
		var eb apiErrorBody
		if json.Unmarshal(data, &eb) == nil && eb.Error.Code != "" {
			return nil, &APIError{Code: eb.Error.Code, Message: eb.Error.Message, Status: resp.StatusCode}
		}
		return nil, &APIError{Code: "UPSTREAM_ERROR",
			Message: fmt.Sprintf("API returned HTTP %s", resp.Status), Status: resp.StatusCode}
	}
	var result ResolveResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("NOVA API %s: bad JSON: %w", platform, err)
	}
	result.Platform = platform
	if len(result.Qualities) == 0 {
		return nil, &APIError{Code: "NOT_FOUND",
			Message: "API returned no downloadable streams for this link", Status: resp.StatusCode}
	}
	return &result, nil
}
