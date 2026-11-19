# NOVA Downloader

**Paste any public link — pick a quality — download it.** A fast, lightweight
desktop downloader with a beautiful dark UI. Video, audio, files, images —
no ads, no trackers, no account.

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go)
![License](https://img.shields.io/badge/license-MIT-8B5CF6)
![Size](https://img.shields.io/badge/binary-~11%20MB-22D3EE)

## What it does

- **Any public link** — YouTube, TikTok, Facebook, X and SoundCloud
  resolve through the **NOVA API** (one central API for the NOVA
  desktop + mobile downloaders); Instagram, Pinterest and 1000+ other
  sites work through the **yt-dlp fallback**, plus any direct file /
  image / document link (built-in HTTP downloader).
- **All qualities at download time** — paste a video link and pick from
  every available quality. Video-only picks are merged with the best
  audio automatically when ffmpeg is available.
- **Per-quality download** — the file you get is exactly the quality you
  picked, in mp4 (or mkv when the streams need it).
- **Pause / resume** — API downloads resume with HTTP Range (trying
  alternate CDN hosts when one fails); direct downloads resume the
  same way. Unfinished jobs survive a restart and re-resolve fresh
  (stream URLs expire, so nothing stale is trusted).
- **Batch / queue** — paste up to 50 links at once, queue them, run 2 at a
  time (configurable).
- **Format choice** — mp4 video, or audio-only mp3 (needs ffmpeg).
- **Subtitles** — download subtitles alongside the video (en + bn by default;
  yt-dlp fallback links only — the API returns no subtitles).
- **Playlists** — paste a playlist, tick the videos you want, queue them all
  (yt-dlp fallback).
- **Clipboard auto-detect** — one click pastes from your clipboard.
- **One-click engine install** — if yt-dlp is missing, the app downloads
  the official release for you. No terminal needed.
- **YouTube bot-check?** — export a `cookies.txt` from your browser and
  point the app at it in Settings; yt-dlp then downloads as you.
- **Private by design** — the UI is a page served on `127.0.0.1` only.
  Nothing leaves your machine except the downloads you ask for (and the
  API resolve calls for the links you paste).

## Platforms

Two engines, and the UI always labels which one resolved each link:

| Platform | Engine | Status |
| --- | --- | --- |
| YouTube | NOVA API | ✅ Works — 27 qualities (combined + video/audio-only) |
| TikTok | NOVA API | ✅ Works — 3 qualities, session cookies + Referer handled |
| Facebook | NOVA API | ✅ Works — 6 qualities (combined + DASH video/audio) |
| X / Twitter | NOVA API | ✅ Works — 5 qualities |
| SoundCloud | NOVA API | ✅ Works — HLS transcodings (ffmpeg or built-in HLS) |
| Direct file | NOVA API | ✅ Works (falls back to local download if the API is unreachable) |
| HLS (.m3u8) | NOVA API | ✅ Works — best variant, ffmpeg or built-in segment download |
| Instagram | yt-dlp fallback | ✅ Works (probe 2026-11-19) |
| Pinterest | yt-dlp fallback | ✅ Works (probe 2026-11-19) |
| Vimeo | yt-dlp fallback | 🔒 Needs login — public videos currently ask for `cookies.txt` in Settings |
| Dailymotion | yt-dlp fallback | ⚠️ IP-dependent — failed from this network |
| Twitch | yt-dlp fallback | ⚠️ IP-dependent — failed from this network |
| Reddit | yt-dlp fallback | ⚠️ IP-dependent — truncated responses from this network |
| Threads | yt-dlp fallback | ❌ Not supported by the engine yet — no Threads extractor in yt-dlp 2026.10.26 |

Any other public link still goes through yt-dlp's 1000+ supported sites,
plus any direct file / image / document link via the built-in downloader.
When a probe fails, the app shows the API's (or yt-dlp's) honest error.
The API's first call after idle can take up to ~a minute (cold start);
the UI says so instead of failing fast.

## Install

### Download (recommended)

Grab the latest release for your OS from
[Releases](https://github.com/irfan420x/nova-downloader/releases) —
`nova-downloader-linux-amd64`, `-windows.exe`, `-macos-arm64`,
`-macos-amd64`, `-linux-arm64`. Single file, ~11 MB, no installer.

1. Run it. It opens the UI in your browser
   (`http://127.0.0.1:8765`).
2. If it says the yt-dlp engine is missing, click **Download yt-dlp**
   in Settings (one click, official release).
3. Paste a link. Pick a quality. Download.

Optional but recommended: install [ffmpeg](https://ffmpeg.org) for mp3
extraction and subtitle conversion.

### Build from source

Requires Go 1.27+.

```sh
git clone https://github.com/irfan420x/nova-downloader.git
cd nova-downloader
go build -o nova-downloader ./cmd/nova-downloader
./nova-downloader
```

Cross-builds: `tools/build.sh` produces all five platform binaries.

Get yt-dlp without the UI: `tools/get-ytdlp.sh` (downloads the official
release next to the binary).

## Usage

| Action | How |
| --- | --- |
| Download a video | Paste link → Fetch → pick a quality → Add to queue |
| Audio only | Tick **Audio only (mp3)** before queueing |
| Subtitles | Tick **Subtitles** before queueing |
| Playlist | Paste playlist link → tick videos → Queue selected |
| Many links | **Batch** → one link per line → Fetch all |
| Pause / resume | Pause / Resume buttons on any job |
| Change save folder | Settings → Save to |
| YouTube asks to sign in | Settings → Cookies file → path to `cookies.txt` |

Downloads go to `~/Downloads/NOVA` by default. Unfinished jobs are kept
in `~/.config/nova-downloader/jobs.json` and resume as *paused* on the
next start.

## How it works

```
paste link ──▶ platform? ──▶ NOVA API ──▶ quality picker ──▶ direct CDN download
                         ├─▶ direct file? ──▶ API /direct ──▶ direct download
                         └─▶ other video site? ──▶ yt-dlp -J ──▶ quality picker
queue ──▶ worker (max 2) ──▶ HTTP Range resume (+ alternate hosts) / yt-dlp --continue
```

- **Video (API platforms):** the NOVA API resolves the link into stream
  URLs + the headers each CDN requires (Cookie / Referer / User-Agent).
  The app downloads the bytes straight from the CDN — no video bytes
  ever pass through the API. Video-only picks merge with the best audio
  via ffmpeg when available.
- **Video (fallback):** `yt-dlp` does extraction and download, as before.
- **Files:** a built-in Go HTTP downloader with `Range` resume and
  `.part` files. HLS streams download via ffmpeg when available, else a
  built-in segment downloader.
- **UI:** a single-page web UI served by the Go binary on localhost.
  No Node, no Electron, no CGO — the whole app is one ~11 MB binary.
- **Stdlib only:** the Go code uses no third-party packages.
- **API URL:** Settings → API URL (default
  `https://nova-downloader-api.vercel.app`; point it at your own
  self-hosted API if you run one).

## Verified (v1.2.0, 2026-11-19, on Linux)

Unit tests: `go test ./...` — all packages pass (incl. API client,
header/alternate handling, HLS, stream-field non-persistence).

Live, against the real app (API + real links, this VM):

| Check | Result |
| --- | --- |
| TikTok resolve + download | ✅ API, 3 qualities, 3,143,526 B mp4 |
| YouTube resolve + download (360p combined) | ✅ API, 27 qualities, 11,829,048 B mp4 |
| YouTube 2160p video+audio merge | ✅ API, 362,085,559 B .mkv (VP9 3840×2160 + AAC) |
| YouTube mp3 | ✅ API, 5,065,075 B MP3 |
| Facebook resolve + download | ✅ API, 6 qualities, 3,034,344 B mp4 |
| X resolve + download (2160p) | ✅ API, 5 qualities, 110,014,999 B mp4 |
| SoundCloud resolve + download (aac 160k) | ✅ API, 2 audio-only, 4,514,880 B .m4s |
| Direct file resolve + download | ✅ API, 8,612 B, byte-identical |
| HLS resolve + download (Mux test stream, 720p) | ✅ API, 5 qualities, 177,374,616 B .ts |
| Instagram (yt-dlp fallback) | ✅ yt-dlp, 2,554,214 B mp4 |

Notes from the live run: the YouTube 360p stream is truncated at
random points by this VM's network (curl gets a different short size
on every try of the same URL) — the downloader retries each
candidate up to 6 times, resuming from the `.part` file, and the job
also resumed correctly across a server restart. The HLS stream is
downloaded by the built-in segment downloader (ffmpeg 8.1.2's HLS
demuxer rejects this stream's segments on this VM); output takes
the segment container's extension (`.ts`).

Older live checks (v1.1.0, yt-dlp engine): TikTok end-to-end
4,452,152 B mp4; YouTube 360p merged 25,581,772 B; direct file
byte-identical; pause/resume sha256 match; mp3 extraction; batch
probe; playlist probe (31 entries); engine one-click install;
cross-build (Windows, macOS arm64/amd64, Linux arm64).

Not verified live yet (code present, needs a real-world case):

- Subtitle download (no subtitled test video tried yet).
- Clipboard button and the UI in a real browser (API-tested with curl;
  JS not executed in a browser yet).
- Download on Windows / macOS (binaries compile; not run there yet).

Known behaviours:

- The API's first call after idle can take up to ~a minute (cold
  start); the UI says so instead of failing fast.
- Merged downloads report progress per stream (video, then audio) —
  the percentage restarts between the two parts, then jumps to 100%.

## Roadmap

- [ ] Native file picker for the download folder
- [ ] Speed limit per download
- [ ] Browser extension: "Download with NOVA" right-click
- [ ] Scheduled / off-peak downloads
- [ ] Chapter / thumbnail embedding for mp3

## Privacy

No ads. No trackers. No analytics. No account. The app makes network
requests only to (a) the link you pasted, (b) the yt-dlp GitHub release
when you ask it to install the engine. Your download history stays in
your own config folder.

## License

MIT — see [LICENSE](LICENSE). Part of the [NOVA](https://github.com/irfan420x)
family. yt-dlp is a separate project (Unlicense) — see its
[repo](https://github.com/yt-dlp/yt-dlp).
