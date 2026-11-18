// Package detect classifies a pasted URL: is it a video page (handled by
// yt-dlp), a direct file/image (plain HTTP download), or something we
// cannot download?
package detect

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Kind of a pasted link.
type Kind string

const (
	// KindVideo is a video/audio page (YouTube, Vimeo, TikTok, ...).
	KindVideo Kind = "video"
	// KindFile is a direct file, image, archive, document, ...
	KindFile Kind = "file"
	// KindUnknown could not be classified.
	KindUnknown Kind = "unknown"
)

// videoHosts are sites whose pages are video/audio, not direct files.
// The list is only a fast path — any other HTML page still gets a yt-dlp
// probe attempt before we give up.
var videoHosts = map[string]bool{
	"youtube.com": true, "www.youtube.com": true, "m.youtube.com": true,
	"youtu.be": true, "youtube-nocookie.com": true, "www.youtube-nocookie.com": true,
	"vimeo.com": true, "www.vimeo.com": true,
	"dailymotion.com": true, "www.dailymotion.com": true,
	"twitch.tv": true, "www.twitch.tv": true,
	"tiktok.com": true, "www.tiktok.com": true,
	"instagram.com": true, "www.instagram.com": true,
	"facebook.com": true, "www.facebook.com": true, "fb.watch": true,
	"x.com": true, "twitter.com": true,
	"soundcloud.com": true, "www.soundcloud.com": true,
	"reddit.com": true, "www.reddit.com": true, "old.reddit.com": true,
	"bilibili.com": true, "www.bilibili.com": true,
	"odysee.com": true, "rumble.com": true,
}

// IsVideoHost reports whether the URL belongs to a known video site.
func IsVideoHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return videoHosts[host]
}

// HeadResult is what a HEAD request learned about a direct link.
type HeadResult struct {
	Size        int64
	ContentType string
	Filename    string
	AcceptRange bool
	URL         string // final URL after redirects
}

// Head issues a HEAD request (falling back to a ranged GET when the
// server refuses HEAD) and returns size/type/filename when known.
func Head(rawURL string) (*HeadResult, error) {
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "nova-downloader/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusForbidden {
		return headViaRangeGet(client, rawURL)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, &HTTPError{Status: resp.Status}
	}
	return headFromResponse(resp), nil
}

func headViaRangeGet(client *http.Client, rawURL string) (*HeadResult, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "nova-downloader/1.0")
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, &HTTPError{Status: resp.Status}
	}
	return headFromResponse(resp), nil
}

func headFromResponse(resp *http.Response) *HeadResult {
	r := &HeadResult{
		Size:        resp.ContentLength,
		ContentType: resp.Header.Get("Content-Type"),
		URL:         resp.Request.URL.String(),
		AcceptRange: strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes"),
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		r.Filename = parseContentDisposition(cd)
	}
	if r.Filename == "" {
		r.Filename = filenameFromURL(r.URL)
	}
	return r
}

// HTTPError is a non-2xx/3xx response from the server.
type HTTPError struct{ Status string }

func (e *HTTPError) Error() string { return "HTTP " + e.Status }

// fileExtensions are path suffixes that mean "direct file" even when the
// server refuses HEAD (403, Cloudflare, ...) — the download itself
// may still succeed with a plain GET.
var fileExtensions = map[string]bool{
	"mp4": true, "mkv": true, "webm": true, "mov": true, "avi": true,
	"mp3": true, "m4a": true, "opus": true, "flac": true, "wav": true, "ogg": true,
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "bmp": true, "svg": true,
	"pdf": true, "epub": true, "mobi": true,
	"zip": true, "rar": true, "7z": true, "tar": true, "gz": true, "xz": true, "bz2": true,
	"exe": true, "apk": true, "dmg": true, "iso": true, "msi": true,
	"doc": true, "docx": true, "xls": true, "xlsx": true, "ppt": true, "pptx": true,
	"txt": true, "csv": true, "json": true, "bin": true, "torrent": true,
}

func hasFileExtension(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return fileExtensions[p[i+1:]]
	}
	return false
}

// Classify decides the kind of a URL without downloading its body.
// Video hosts are video immediately; anything else gets a HEAD request —
// a non-HTML content type means a direct file, an HTML page means
// "try yt-dlp" (KindVideo, probe will confirm). When HEAD itself fails
// (server blocks it, offline, ...), a file-looking path is still treated
// as a direct file; anything else falls back to a yt-dlp probe, which
// has its own fetch logic and decides.
func Classify(rawURL string) (Kind, *HeadResult, error) {
	if IsVideoHost(rawURL) {
		return KindVideo, nil, nil
	}
	head, err := Head(rawURL)
	if err != nil {
		if hasFileExtension(rawURL) {
			return KindFile, &HeadResult{Filename: filenameFromURL(rawURL)}, nil
		}
		return KindVideo, nil, nil
	}
	ct := strings.ToLower(head.ContentType)
	if strings.HasPrefix(ct, "text/html") || ct == "" {
		// An HTML page on an unknown host — probably a video page
		// yt-dlp supports. Probe will confirm or reject.
		return KindVideo, head, nil
	}
	return KindFile, head, nil
}

func parseContentDisposition(cd string) string {
	// filename="name.ext" or filename*=UTF-8''name.ext
	for _, part := range strings.Split(cd, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "filename*=") {
			v := strings.TrimPrefix(part, "filename*=")
			v = strings.TrimPrefix(v, "filename*=")
			if i := strings.LastIndex(v, "''"); i >= 0 {
				v = v[i+2:]
			}
			return strings.Trim(v, `"`)
		}
		if strings.HasPrefix(strings.ToLower(part), "filename=") {
			v := strings.TrimPrefix(part, "filename=")
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func filenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "download"
	}
	p := strings.TrimSuffix(u.Path, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	if p == "" {
		return "download"
	}
	// Strip query-like junk some CDNs append to the path.
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return p
}

// GuessFilename returns a sane output filename for a direct download.
func GuessFilename(rawURL string, head *HeadResult) string {
	if head != nil && head.Filename != "" {
		return head.Filename
	}
	return filenameFromURL(rawURL)
}
