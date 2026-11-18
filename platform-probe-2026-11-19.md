# Platform probe — NOVA Desktop Downloader engine (yt-dlp)

- **Date:** 2026-11-19 (SGT), run on this VM
- **Engine:** `~/workspace/nova-downloader/bin/yt-dlp`, version **2026.10.26**
- **Method:** `yt-dlp -J --no-playlist <url>`, 90 s timeout, up to 2 attempts per URL (round 1). Ambiguous failures got one extra round with a fallback URL or a re-confirmation (round 2). No files downloaded except the single end-to-end test below.
- **Raw probe output:** `/tmp/platform-test/` (`<platform>.json` / `<platform>.err`, round-2 files suffixed `-r2`)

## Summary

**7 of 12 work** with no login: YouTube, SoundCloud, TikTok, X/Twitter, Instagram, Facebook, Pinterest.
**1 needs login:** Vimeo. **4 fail:** Dailymotion, Twitch, Reddit, Threads.

| Platform | URL used | Result | Formats | Error (short) |
|---|---|---|---|---|
| YouTube | https://www.youtube.com/watch?v=aqz-KE-bpKQ | WORKS * | 53 | Round 1: bot-check ("Sign in to confirm you're not a bot"); round 2 OK — intermittent from this VM's IP |
| Vimeo | https://vimeo.com/76979871 (r2: https://vimeo.com/1084537) | NEEDS-LOGIN | — | "The web client only works when logged-in" (both video IDs) |
| Dailymotion | https://www.dailymotion.com/video/x7tgad0 | FAILS | — | "Remote end closed connection without response" (both rounds) |
| SoundCloud | https://soundcloud.com/marion-1/marion-over-the-clouds | WORKS | 2 | — |
| TikTok | https://www.tiktok.com/@zachking/video/6768504823336815877 | WORKS | 14 | — (warning only: no impersonation target available) |
| X / Twitter | https://x.com/MGGA2021h/status/2122919356707598725 | WORKS | 9 | — |
| Twitch | clip https://www.twitch.tv/phoenpc/clip/CoyAmazingClipzFUNgineer-dXY5WP0UvPbSLUR_ (r2 VOD https://www.twitch.tv/videos/2912355720) | FAILS | — | Clip: "This clip is no longer available"; VOD: "Remote end closed connection without response" |
| Reddit | https://www.reddit.com/r/nextfuckinglevel/comments/mnouq4/learning_to_sing/ | FAILS | — | `IncompleteRead` — truncated response (both rounds, different byte counts) |
| Instagram | https://www.instagram.com/reel/DewzwXOAREy/ | WORKS | 5 | — |
| Facebook | https://www.facebook.com/reel/1752522482505248/ | WORKS | 1 | — |
| Threads | https://www.threads.com/@zuck/post/DZpPDXbCeTt (r2: https://www.threads.net/@zuck/post/DZpPDXbCeTt) | FAILS | — | "Unsupported URL" (both domains — no working Threads extractor in yt-dlp 2026.10.26) |
| Pinterest | https://www.pinterest.com/pin/1026891152548201938/ | WORKS | 5 | — |

\* YouTube failed round 1 with the bot-check, then passed round 2 with 53 formats, and the same video was already downloaded end-to-end through the app earlier today (144p, 14.9 MB MP4). The bot-check is intermittent from this VM's datacentre IP — the app's `cookies.txt` setting is the documented remedy.

## Titles found (working platforms)

- YouTube: "Big Buck Bunny 60fps 4K - Official Blender Foundation Short Film" (53 formats)
- SoundCloud: "MARION - Over The Clouds" (2 formats, audio)
- TikTok: "They rejected my application to Hogwarts but I still found a way to b…" (14 formats)
- X: "Morlock Nr.2 - Pastor Riemenschneider zur heuchlerischen Kirche und dem AfD-Nikolaus…" (9 formats)
- Instagram: "Video by boramigas1" (5 formats)
- Facebook: "extended text kcyvsnsgkzwsswaedtlvdgdizddikbdqrbrfqlctfxxmzkswal | Angelo's Photography" (1 format)
- Pinterest: "cat 🐈 electrician" (5 formats)

## End-to-end download test (non-YouTube platform)

Picked **TikTok** (most formats of the non-YouTube winners):

```
yt-dlp -f worst -o "%(title)s.%(ext)s" "https://www.tiktok.com/@zachking/video/6768504823336815877"
```

Result: **OK** — `They rejected my application to Hogwarts but I still found a way to b....mp4`,
4,452,152 bytes (4.25 MiB), `file` reports "ISO Media, MP4 Base Media v1", downloaded in ~3 s.
File kept at `/tmp/platform-test/` (under the 30 MB limit, so not deleted).

## Raw errors (verbatim)

### YouTube — round 1 (round 2 passed, stderr empty)
```
ERROR: [youtube] aqz-KE-bpKQ: Sign in to confirm you’re not a bot. Use --cookies-from-browser or --cookies for the authentication. See  https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp  for how to manually pass cookies. Also see  https://github.com/yt-dlp/yt-dlp/wiki/Extractors#exporting-youtube-cookies  for tips on effectively exporting YouTube cookies
```

### Vimeo — round 1 (76979871) and round 2 (1084537), identical except the ID
```
ERROR: [vimeo] 76979871: The web client only works when logged-in. Use --cookies, --cookies-from-browser, --username and --password, --netrc-cmd, or --netrc (vimeo) to provide account credentials. See  https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp  for how to manually pass cookies
ERROR: [vimeo] 1084537: The web client only works when logged-in. Use --cookies, --cookies-from-browser, --username and --password, --netrc-cmd, or --netrc (vimeo) to provide account credentials. See  https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp  for how to manually pass cookies
```

### Dailymotion — round 1 and round 2, identical
```
ERROR: [dailymotion] x7tgad0: Unable to download JSON metadata: Remote end closed connection without response (caused by TransportError('Remote end closed connection without response'))
```

### Twitch — round 1 (clip) and round 2 (VOD)
```
ERROR: [twitch:clips] CoyAmazingClipzFUNgineer-dXY5WP0UvPbSLUR_: This clip is no longer available
ERROR: [twitch:vod] 2912355720: Unable to download JSON metadata: Remote end closed connection without response (caused by TransportError('Remote end closed connection without response'))
```

### Reddit — round 1 and round 2 (byte counts differ between rounds)
```
ERROR: [Reddit] mnouq4: mnouq4: Error reading response: 49152 bytes read, 140756 more expected (caused by <IncompleteRead: 49152 bytes read, 140756 more expected>); please report this issue on  https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using  yt-dlp -U
ERROR: [Reddit] mnouq4: mnouq4: Error reading response: 80856 bytes read, 109052 more expected (caused by <IncompleteRead: 80856 bytes read, 109052 more expected>); please report this issue on  https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using  yt-dlp -U
```

### Threads — round 1 (threads.com) and round 2 (threads.net), identical
```
ERROR: Unsupported URL: https://www.threads.com/@zuck/post/DZpPDXbCeTt
```

## Notes / caveats

- **YouTube bot-check is intermittent, not permanent.** Failed once, passed minutes later with 53 formats, and the app downloaded the same video earlier today. Users hitting it need the app's `cookies.txt` setting.
- **Vimeo "login required" is surprising for public videos** and hit both the original ID (76979871, reported taken down elsewhere) and the live replacement (1084537). Could be a yt-dlp 2026.10.26 web-client limitation or IP-based; either way, Vimeo currently needs cookies through this engine.
- **Dailymotion and Twitch VOD failed with the identical transport error** ("Remote end closed connection without response") — looks like a network/IP-level block from this VM rather than two independent site outages. Worth re-testing from a residential IP before declaring the platforms broken.
- **Reddit truncated responses twice** with different byte counts — consistent with Reddit throttling/blocking this IP range, not a one-off glitch.
- **Threads has no working extractor in yt-dlp 2026.10.26** for either `threads.com` or `threads.net` post URLs ("Unsupported URL"). A newer yt-dlp or a different extractor would be needed.
- **Twitch clip failure is about that clip, not Twitch as a whole** ("This clip is no longer available" — the clip was deleted/expired). The VOD attempt then hit the transport error above, so Twitch as a platform remains unproven either way.
- **Facebook returned only 1 format** but the probe succeeded; quality choice on Facebook reels is limited to what Facebook serves (single stream).
- TikTok probe printed an impersonation warning ("no impersonate target is available") but still returned 14 formats and downloaded fine.
