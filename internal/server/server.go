// Package server is the local web UI + JSON API of NOVA Downloader.
// It listens on 127.0.0.1 only: the UI is a browser page, the "app" is a
// single Go binary with no external runtime.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irfan420x/nova-downloader/internal/detect"
	"github.com/irfan420x/nova-downloader/internal/engine"
	"github.com/irfan420x/nova-downloader/internal/httpdl"
	"github.com/irfan420x/nova-downloader/internal/novaapi"
	"github.com/irfan420x/nova-downloader/internal/queue"
	"github.com/irfan420x/nova-downloader/internal/yt"
)

//go:embed web/index.html
var webFS embed.FS

// Settings persisted in the app data dir.
type Settings struct {
	OutDir        string `json:"out_dir"`
	MaxConcurrent int    `json:"max_concurrent"`
	// CookiesFile is an optional cookies.txt for yt-dlp (exported from
	// the browser) — needed when YouTube shows its bot check.
	CookiesFile string `json:"cookies_file"`
	// ApiBaseURL is the NOVA Downloader API used as the primary
	// resolve engine. "" means the production default.
	ApiBaseURL string `json:"api_base_url"`
}

// Server wires the queue manager, engine and HTTP handlers.
type Server struct {
	mgr      *queue.Manager
	settings Settings
	mu       sync.Mutex
	dataDir  string
	api      *novaapi.Client
	// apiHealth caches the last /api/health result (checked at most
	// once a minute — the UI polls /api/status every 10s).
	apiHealthOK  bool
	apiHealthErr string
	apiHealthAt  time.Time
}

// New builds a Server. settingsPath is the JSON file for settings.
func New(mgr *queue.Manager, settings Settings, dataDir string) *Server {
	return &Server{mgr: mgr, settings: settings, dataDir: dataDir, api: novaapi.NewClient(settings.ApiBaseURL)}
}

// DefaultDownloadDir is ~/Downloads/NOVA (NOVA family convention).
func DefaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads", "NOVA")
}

// LoadSettings reads settings.json, falling back to defaults.
func LoadSettings(dataDir string) Settings {
	s := Settings{OutDir: DefaultDownloadDir(), MaxConcurrent: 2, ApiBaseURL: novaapi.DefaultBaseURL}
	data, err := os.ReadFile(filepath.Join(dataDir, "settings.json"))
	if err != nil {
		return s
	}
	var loaded Settings
	if err := json.Unmarshal(data, &loaded); err != nil {
		return s
	}
	if loaded.OutDir != "" {
		s.OutDir = loaded.OutDir
	}
	if loaded.MaxConcurrent >= 1 && loaded.MaxConcurrent <= 8 {
		s.MaxConcurrent = loaded.MaxConcurrent
	}
	if loaded.CookiesFile != "" {
		s.CookiesFile = loaded.CookiesFile
	}
	if loaded.ApiBaseURL != "" {
		s.ApiBaseURL = loaded.ApiBaseURL
	}
	return s
}

func (s *Server) saveSettings() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(s.dataDir, 0o755)
	_ = os.WriteFile(filepath.Join(s.dataDir, "settings.json"), data, 0o644)
}

// Runner executes one queued job: the NOVA API for API-resolved
// jobs, yt-dlp for fallback video, plain HTTP for files.
// It reads the cookies file from the current settings on every call.
func (s *Server) Runner(ctx context.Context, j *queue.Job, report func(queue.Progress)) error {
	s.mu.Lock()
	cookies := s.settings.CookiesFile
	s.mu.Unlock()
	// API-resolved jobs (video or file) go through the API runner,
	// which downloads the resolved stream with its headers.
	if j.Engine == "api" {
		return s.runAPIJob(ctx, j, report)
	}
	if j.Kind == queue.KindVideo {
		ytPath := engine.Find()
		if ytPath == "" {
			return errors.New("yt-dlp engine not found — download it from Settings")
		}
		err := yt.Download(ctx, ytPath, yt.Options{
			URL:         j.URL,
			FormatID:    j.FormatID,
			OutDir:      j.OutDir,
			AudioOnly:   j.AudioOnly,
			Subtitles:   j.Subtitles,
			SubLangs:    j.SubLangs,
			CookiesFile: cookies,
			OnProgress: func(p yt.Progress) {
				report(queue.Progress{
					Percent: p.Percent, Done: p.Downloaded, Total: p.Total,
					Speed: p.Speed, ETA: p.ETA, Filename: p.Filename,
				})
			},
			OnFilename: func(name string) {
				report(queue.Progress{Filename: name})
			},
		})
		if err != nil && errors.Is(err, yt.ErrPaused) {
			return queue.ErrPaused
		}
		return err
	}
	// Direct file download.
	name := j.Filename
	if name == "" {
		name = detect.GuessFilename(j.URL, nil)
	}
	dest := filepath.Join(j.OutDir, name)
	if err := os.MkdirAll(j.OutDir, 0o755); err != nil {
		return err
	}
	return httpdl.Download(ctx, j.URL, dest, func(p httpdl.Progress) {
		pct := 0.0
		if p.Total > 0 {
			pct = float64(p.Done) / float64(p.Total) * 100
		}
		report(queue.Progress{
			Percent: pct, Done: p.Done, Total: p.Total,
			Speed:    yt.HumanSize(p.SpeedBps) + "/s",
			Filename: name,
		})
	})
}

// Handler returns the full HTTP handler (UI + API).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/batch", s.handleBatch)
	mux.HandleFunc("/api/queue", s.handleQueue)
	mux.HandleFunc("/api/jobs", s.handleJobs)
	mux.HandleFunc("/api/jobs/", s.handleJobAction)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/engine/download", s.handleEngineDownload)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "UI missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusResp struct {
	EnginePath    string `json:"engine_path"`
	EngineVersion string `json:"engine_version"`
	FFmpeg        bool   `json:"ffmpeg"`
	OutDir        string `json:"out_dir"`
	MaxConcurrent int    `json:"max_concurrent"`
	Jobs          int    `json:"jobs"`
	Active        int    `json:"active"`
	APIURL        string `json:"api_url"`
	APIOK         bool   `json:"api_ok"`
	APIErr        string `json:"api_error,omitempty"`
}

// apiHealth returns the cached API health (rechecked at most once a
// minute so the UI's 10s status poll doesn't hammer the API).
func (s *Server) apiHealth() (bool, string) {
	s.mu.Lock()
	if !s.apiHealthAt.IsZero() && time.Since(s.apiHealthAt) < time.Minute {
		ok, errStr := s.apiHealthOK, s.apiHealthErr
		s.mu.Unlock()
		return ok, errStr
	}
	s.mu.Unlock()
	err := s.api.Health(context.Background())
	s.mu.Lock()
	s.apiHealthAt = time.Now()
	if err != nil {
		s.apiHealthOK, s.apiHealthErr = false, err.Error()
	} else {
		s.apiHealthOK, s.apiHealthErr = true, ""
	}
	ok, errStr := s.apiHealthOK, s.apiHealthErr
	s.mu.Unlock()
	return ok, errStr
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ytPath := engine.Find()
	ver := ""
	if ytPath != "" {
		ver, _ = engine.Version(ytPath)
	}
	active := 0
	for _, j := range s.mgr.List() {
		if j.Status == queue.StatusDownloading {
			active++
		}
	}
	s.mu.Lock()
	outDir, maxC := s.settings.OutDir, s.settings.MaxConcurrent
	apiURL := s.settings.ApiBaseURL
	s.mu.Unlock()
	apiOK, apiErr := s.apiHealth()
	writeJSON(w, http.StatusOK, statusResp{
		EnginePath: ytPath, EngineVersion: ver,
		FFmpeg: engine.FFmpeg() != "",
		OutDir: outDir, MaxConcurrent: maxC,
		Jobs: len(s.mgr.List()), Active: active,
		APIURL: apiURL, APIOK: apiOK, APIErr: apiErr,
	})
}

type probeReq struct {
	URL string `json:"url"`
}

type probeResp struct {
	Kind       string             `json:"kind"`   // video | file | playlist
	Engine     string             `json:"engine"` // "api" | "ytdlp" | "file"
	Platform   string             `json:"platform,omitempty"`
	Title      string             `json:"title"`
	Uploader   string             `json:"uploader"`
	Thumbnail  string             `json:"thumbnail"`
	Duration   float64            `json:"duration,omitempty"`
	URL        string             `json:"url"`
	Filename   string             `json:"filename,omitempty"`
	Size       int64              `json:"size,omitempty"`
	ContentTyp string             `json:"content_type,omitempty"`
	Combined   []yt.Format        `json:"combined,omitempty"`
	VideoOnly  []yt.Format        `json:"video_only,omitempty"`
	AudioOnly  []yt.Format        `json:"audio_only,omitempty"`
	Entries    []yt.PlaylistEntry `json:"entries,omitempty"`
	// SubtitlesSupported is false for API probes (the API returns no
	// subtitles) and true for yt-dlp probes.
	SubtitlesSupported bool `json:"subtitles_supported"`
	// Streams carries the API stream data for file probes (the single
	// direct-file quality), so the UI can queue it with headers.
	Streams []yt.Format `json:"streams,omitempty"`
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req probeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON")
		return
	}
	rawURL := strings.TrimSpace(req.URL)
	if _, err := url.ParseRequestURI(rawURL); err != nil || !strings.HasPrefix(rawURL, "http") {
		writeErr(w, http.StatusBadRequest, "paste a valid http(s) link")
		return
	}
	resp, err := s.probeURL(r.Context(), rawURL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// probeErrorHint classifies a yt-dlp probe failure and returns a short
// plain-English hint for the user, or "" when the error speaks for itself.
// Login problems (site wants an account/cookies), network problems (the
// site could not be reached from this network) and unsupported links
// (no extractor in the current engine) each get their own hint.
func probeErrorHint(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "login"),
		strings.Contains(msg, "logged-in"),
		strings.Contains(msg, "logged in"),
		strings.Contains(msg, "sign in"),
		strings.Contains(msg, "not a bot"),
		strings.Contains(msg, "cookies"):
		return "Hint: this site needs login — add your cookies.txt in Settings and try again."
	case strings.Contains(msg, "unsupported url"):
		return "Hint: this link is not supported by the current engine — update yt-dlp from Settings and try again."
	case strings.Contains(msg, "remote end closed"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "incompleteread"),
		strings.Contains(msg, "transport"),
		strings.Contains(msg, "network"),
		strings.Contains(msg, "timed out"):
		return "Hint: the site could not be reached from this network — try again later, or add cookies.txt in Settings."
	default:
		return ""
	}
}

// withProbeHint appends the classification hint (if any) to a probe error,
// keeping the raw error text first so the original detail is never lost.
func withProbeHint(err error) error {
	if hint := probeErrorHint(err); hint != "" {
		return fmt.Errorf("%w. %s", err, hint)
	}
	return err
}

func (s *Server) probeURL(ctx context.Context, rawURL string) (*probeResp, error) {
	// Playlists / channels are not API territory — the API resolves
	// single videos. yt-dlp keeps playlist support working.
	if novaapi.IsPlaylistURL(rawURL) {
		return s.probeYTDLP(ctx, rawURL)
	}
	platform := novaapi.DetectPlatform(rawURL)
	switch {
	case platform != novaapi.PlatformDirect:
		// A platform the API covers (YouTube, TikTok, Facebook, X,
		// SoundCloud, HLS) — resolve through the API. No yt-dlp
		// for these, even when the API call fails: the API's
		// honest error is the answer.
		return s.apiProbe(ctx, rawURL)
	case novaapi.IsFallbackHost(rawURL):
		// A video site with no API endpoint — yt-dlp fallback.
		return s.probeYTDLP(ctx, rawURL)
	default:
		// Unknown host: HEAD it. A direct file goes to the API
		// /direct endpoint (local fallback when the API is
		// unreachable); an HTML page goes to the yt-dlp probe,
		// which supports 1000+ sites.
		kind, head, _ := detect.Classify(rawURL)
		if kind == detect.KindFile && head != nil {
			return s.probeDirectFile(ctx, rawURL, head)
		}
		return s.probeYTDLP(ctx, rawURL)
	}
}

// probeYTDLP is the original yt-dlp probe path (fallback engine).
func (s *Server) probeYTDLP(ctx context.Context, rawURL string) (*probeResp, error) {
	kind, head, _ := detect.Classify(rawURL)
	if kind == detect.KindFile && head != nil {
		return &probeResp{
			Kind: "file", Engine: "file", URL: rawURL,
			Title: head.Filename, Filename: head.Filename,
			Size: head.Size, ContentTyp: head.ContentType,
		}, nil
	}
	// Video path — needs yt-dlp.
	ytPath := engine.Find()
	if ytPath == "" {
		return nil, errors.New("yt-dlp engine is missing — open Settings and download it (one click), then try again")
	}
	s.mu.Lock()
	cookies := s.settings.CookiesFile
	s.mu.Unlock()
	info, err := yt.Probe(ctx, ytPath, rawURL, cookies)
	if err != nil {
		// Probe failed but HEAD said "file": fall back to direct download.
		if head != nil && kind == detect.KindVideo {
			ct := strings.ToLower(head.ContentType)
			if !strings.HasPrefix(ct, "text/html") && ct != "" {
				return &probeResp{
					Kind: "file", Engine: "file", URL: rawURL,
					Title: head.Filename, Filename: head.Filename,
					Size: head.Size, ContentTyp: head.ContentType,
				}, nil
			}
		}
		return nil, withProbeHint(err)
	}
	if info.IsPlaylist() {
		return &probeResp{
			Kind: "playlist", Engine: "ytdlp", URL: rawURL,
			Title: info.Title, Uploader: info.Uploader,
			Thumbnail: info.Thumbnail, Entries: info.Entries,
			SubtitlesSupported: true,
		}, nil
	}
	dur := 0.0
	if info.Duration != nil {
		dur = *info.Duration
	}
	combined := info.CombinedFormats()
	if len(combined) == 0 {
		// Modern YouTube etc. ship no single-file formats — offer
		// merged (video+audio) options built from the video heights.
		combined = info.MergedOptions()
	}
	return &probeResp{
		Kind: "video", Engine: "ytdlp", URL: rawURL,
		Title: info.Title, Uploader: info.Uploader,
		Thumbnail: info.Thumbnail, Duration: dur,
		Combined:           combined,
		VideoOnly:          info.VideoOnlyFormats(),
		AudioOnly:          info.AudioOnlyFormats(),
		SubtitlesSupported: true,
	}, nil
}

type batchReq struct {
	URLs []string `json:"urls"`
}

type batchResp struct {
	Results []*probeResp `json:"results"`
	Errors  []string     `json:"errors"`
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req batchReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON")
		return
	}
	if len(req.URLs) == 0 || len(req.URLs) > 50 {
		writeErr(w, http.StatusBadRequest, "batch needs 1–50 links")
		return
	}
	resp := &batchResp{}
	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		pr, err := s.probeURL(r.Context(), u)
		if err != nil {
			resp.Errors = append(resp.Errors, u+": "+err.Error())
			continue
		}
		resp.Results = append(resp.Results, pr)
	}
	writeJSON(w, http.StatusOK, resp)
}

type queueReq struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	Kind       string `json:"kind"` // video | file
	FormatID   string `json:"format_id"`
	FormatNote string `json:"format_note"`
	Filename   string `json:"filename"`
	AudioOnly  bool   `json:"audio_only"`
	Subtitles  bool   `json:"subtitles"`
	SubLangs   string `json:"sub_langs"`
	// API-resolved stream (engine "api" only). In-memory only —
	// the queue never persists stream URLs or headers.
	Engine             string            `json:"engine"`
	Platform           string            `json:"platform"`
	StreamURL          string            `json:"stream_url"`
	StreamHeaders      map[string]string `json:"stream_headers"`
	StreamExt          string            `json:"stream_ext"`
	Alternates         []string          `json:"alternates"`
	AudioStreamURL     string            `json:"audio_stream_url"`
	AudioStreamHeaders map[string]string `json:"audio_stream_headers"`
	AudioStreamExt     string            `json:"audio_stream_ext"`
	Merged             bool              `json:"merged"`
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req queueReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON")
		return
	}
	rawURL := strings.TrimSpace(req.URL)
	if rawURL == "" {
		writeErr(w, http.StatusBadRequest, "url is required")
		return
	}
	kind := queue.KindVideo
	if req.Kind == "file" {
		kind = queue.KindFile
	}
	engineName := req.Engine
	if engineName == "" {
		// Legacy clients (and yt-dlp fallback probes) send no engine.
		engineName = "ytdlp"
		if kind == queue.KindFile {
			engineName = "file"
		}
	}
	j := &queue.Job{
		URL: req.URL, Title: req.Title, Kind: kind,
		FormatID: req.FormatID, FormatNote: req.FormatNote,
		Filename: req.Filename, AudioOnly: req.AudioOnly,
		Subtitles: req.Subtitles, SubLangs: req.SubLangs,
		Engine: engineName, Platform: req.Platform,
		StreamURL: req.StreamURL, StreamHeaders: req.StreamHeaders,
		StreamExt: req.StreamExt, Alternates: req.Alternates,
		AudioStreamURL:     req.AudioStreamURL,
		AudioStreamHeaders: req.AudioStreamHeaders,
		AudioStreamExt:     req.AudioStreamExt, Merged: req.Merged,
	}
	if j.Title == "" {
		j.Title = j.Filename
	}
	if j.Title == "" {
		j.Title = rawURL
	}
	j = s.mgr.Add(j)
	writeJSON(w, http.StatusCreated, j)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, http.StatusOK, s.mgr.List())
}

// /api/jobs/{id}/pause | resume | cancel ; POST /api/jobs/clear-finished
func (s *Server) handleJobAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	if rest == "clear-finished" {
		s.mgr.ClearFinished()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		writeErr(w, http.StatusBadRequest, "expected /api/jobs/{id}/{action}")
		return
	}
	id, action := parts[0], parts[1]
	var err error
	switch action {
	case "pause":
		err = s.mgr.Pause(id)
	case "resume":
		err = s.mgr.Resume(id)
	case "cancel":
		err = s.mgr.Cancel(id)
	default:
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, http.StatusOK, s.settings)
	case http.MethodPost:
		var req Settings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad JSON")
			return
		}
		if req.OutDir != "" {
			if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
				writeErr(w, http.StatusBadRequest, "cannot create download folder: "+err.Error())
				return
			}
			s.mu.Lock()
			s.settings.OutDir = req.OutDir
			s.mu.Unlock()
			s.mgr.SetOutDir(req.OutDir)
		}
		if req.MaxConcurrent >= 1 && req.MaxConcurrent <= 8 {
			s.mu.Lock()
			s.settings.MaxConcurrent = req.MaxConcurrent
			s.mu.Unlock()
			s.mgr.SetMaxConcurrent(req.MaxConcurrent)
		}
		if req.CookiesFile != "" {
			if _, err := os.Stat(req.CookiesFile); err != nil {
				writeErr(w, http.StatusBadRequest, "cookies file not found: "+req.CookiesFile)
				return
			}
			s.mu.Lock()
			s.settings.CookiesFile = req.CookiesFile
			s.mu.Unlock()
		}
		if req.ApiBaseURL != "" {
			if !strings.HasPrefix(req.ApiBaseURL, "http://") && !strings.HasPrefix(req.ApiBaseURL, "https://") {
				writeErr(w, http.StatusBadRequest, "API URL must start with http:// or https://")
				return
			}
			s.mu.Lock()
			s.settings.ApiBaseURL = strings.TrimRight(req.ApiBaseURL, "/")
			s.api = novaapi.NewClient(s.settings.ApiBaseURL)
			s.apiHealthAt = time.Time{} // force a fresh health check
			s.mu.Unlock()
		}
		s.saveSettings()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET or POST only")
	}
}

func (s *Server) handleEngineDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	// Stream progress as plain text lines; client reads until done.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	flusher.Flush()
	ctx := r.Context()
	path, err := engine.Download(ctx, func(received, total int64) {
		fmt.Fprintf(w, "progress %d %d\n", received, total)
		flusher.Flush()
	})
	if err != nil {
		fmt.Fprintf(w, "error %s\n", err.Error())
		return
	}
	ver, _ := engine.Version(path)
	fmt.Fprintf(w, "done %s %s\n", path, ver)
	_ = io.Discard
}
