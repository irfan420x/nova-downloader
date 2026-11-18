# NOVA Downloader

**Paste any public link — pick a quality — download it.** A fast, lightweight
desktop downloader with a beautiful dark UI. Video, audio, files, images —
no ads, no trackers, no account.

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go)
![License](https://img.shields.io/badge/license-MIT-8B5CF6)
![Size](https://img.shields.io/badge/binary-~11%20MB-22D3EE)

## What it does

- **Any public link** — YouTube, Vimeo, TikTok, Instagram, SoundCloud and
  1000+ other sites (via yt-dlp), plus any direct file / image / document
  link (built-in HTTP downloader).
- **All qualities at download time** — paste a video link and pick from
  every available quality (2160p down to 144p, plus video-only and
  audio-only streams). Modern YouTube ships no single-file formats, so the
  app builds merged video+audio options per quality and merges them for you.
- **Per-quality download** — the file you get is exactly the quality you
  picked, in mp4.
- **Pause / resume** — video downloads resume with `--continue`, direct
  downloads resume with HTTP Range. Unfinished jobs survive a restart.
- **Batch / queue** — paste up to 50 links at once, queue them, run 2 at a
  time (configurable).
- **Format choice** — mp4 video, or audio-only mp3 (needs ffmpeg).
- **Subtitles** — download subtitles alongside the video (en + bn by default).
- **Playlists** — paste a playlist, tick the videos you want, queue them all.
- **Clipboard auto-detect** — one click pastes from your clipboard.
- **One-click engine install** — if yt-dlp is missing, the app downloads
  the official release for you. No terminal needed.
- **YouTube bot-check?** — export a `cookies.txt` from your browser and
  point the app at it in Settings; yt-dlp then downloads as you.
- **Private by design** — the UI is a page served on `127.0.0.1` only.
  Nothing leaves your machine except the downloads you ask for.

## Platforms

Tested 2026-11-19 with yt-dlp 2026.10.26. Results can vary by network and
IP — a site that fails here may work from your connection, and engine
updates can change this table. Update yt-dlp from Settings for the newest
extractors.

| Platform | Status |
| --- | --- |
| YouTube | ✅ Works (bot-check is intermittent from datacentre IPs — `cookies.txt` in Settings fixes it) |
| TikTok | ✅ Works |
| Instagram | ✅ Works |
| Facebook | ✅ Works (reels serve a single format, so quality choice is limited) |
| X / Twitter | ✅ Works |
| SoundCloud | ✅ Works |
| Pinterest | ✅ Works |
| Vimeo | 🔒 Needs login — public videos currently ask for `cookies.txt` in Settings |
| Dailymotion | ⚠️ IP-dependent — failed from this network ("Remote end closed connection without response") |
| Twitch | ⚠️ IP-dependent — failed from this network |
| Reddit | ⚠️ IP-dependent — truncated responses from this network |
| Threads | ❌ Not supported by the engine yet — no Threads extractor in yt-dlp 2026.10.26 ("Unsupported URL") |

Any other public link still goes through yt-dlp's 1000+ supported sites,
plus any direct file / image / document link via the built-in downloader.
When a probe fails, the app now shows a short hint with the error
(login / network / unsupported) telling you what to try next.

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
paste link ──▶ classify ──▶ video site? ──▶ yt-dlp -J ──▶ quality picker
                         └─▶ direct file? ──▶ HEAD ──▶ direct download
queue ──▶ worker (max 2) ──▶ yt-dlp --continue / HTTP Range resume
```

- **Video:** `yt-dlp` does extraction and download. The app never
  scrapes video pages itself.
- **Files:** a built-in Go HTTP downloader with `Range` resume and
  `.part` files.
- **UI:** a single-page web UI served by the Go binary on localhost.
  No Node, no Electron, no CGO — the whole app is one ~11 MB binary.
- **Stdlib only:** the Go code uses no third-party packages.

## Verified (v1.1.0, 2026-11-19, on Linux)

Unit + race tests: `go test -race ./...` — all packages pass.

Platform probe (12 sites, yt-dlp 2026.10.26, this VM): 7 work —
YouTube, TikTok, Instagram, Facebook, X, SoundCloud, Pinterest. Vimeo
needs login/cookies. Dailymotion, Twitch and Reddit fail from this
network (transport / truncated responses). Threads has no extractor in
the engine yet ("Unsupported URL"). Full report:
`platform-probe-2026-11-19.md`.

Live, against the real app:

| Check | Result |
| --- | --- |
| Platform probe (12 sites) | ✅ 7 work, 1 needs login, 3 IP-dependent, 1 unsupported by engine |
| TikTok end-to-end download | ✅ 4,452,152 B mp4 in ~3 s, valid MP4 |
| Vimeo probe error hint | ✅ error now carries "Hint: this site needs login — add your cookies.txt in Settings and try again." |

Unit + race tests: `go test -race ./...` — all packages pass.

Live, against the real app:

| Check | Result |
| --- | --- |
| YouTube probe (Big Buck Bunny 4K) | 8 merged qualities 2160p–144p, 22 video-only, 5 audio-only |
| Video download, 360p merged | ✅ 25,581,772 B mp4 — ffprobe: AV1 640×360 + Opus, 634.6 s |
| Direct file download | ✅ byte-identical (md5 match) |
| Pause / resume (70 MB file) | ✅ paused at 0.5%, resumed, sha256 match |
| MP3 audio extraction | ✅ real MP3, 634.6 s |
| Batch probe (video + file) | ✅ 2 results, 0 errors |
| Playlist probe | ✅ 31 entries with titles; first entry downloaded fine |
| Engine one-click install | ✅ downloaded 3,073,072 B, version 2026.10.26 |
| Cross-build | ✅ Windows, macOS arm64/amd64, Linux arm64 |

Not verified live yet (code present, needs a real-world case):

- Subtitle download (no subtitled test video tried yet).
- Clipboard button and the UI in a real browser (API-tested with curl;
  JS not executed in a browser yet).
- Download on Windows / macOS (binaries compile; not run there yet).

Known behaviours:

- YouTube intermittently shows a bot check from datacentre IPs
  ("Sign in to confirm you're not a bot"). The app surfaces yt-dlp's
  real error and retries work after a minute; a `cookies.txt` fixes it
  properly.
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
