# ytclip

Clip a section out of a YouTube video or livestream VOD, fast, with a TUI.

Downloads **only** the requested time range, then optionally converts to
H.264 for editing. One binary, no runtime, Linux / macOS / Windows.

Replaces the `youtube-clip.ps1` + `youtube-clip.sh` pair with a single
codebase.

## Install

Download the binary for your platform from Releases, or:

```
go install github.com/shivangrathore/ytclip@latest
```

You also need **yt-dlp** and **ffmpeg**. If they are not already there,
ytclip fetches them itself — start it and press `i`, or run:

```
ytclip --install       # download only what is missing
ytclip --doctor        # what is installed, and which encoders work
ytclip --uninstall     # delete what ytclip downloaded
```

Downloads go to ytclip's own data dir (`~/.cache/youtube-clip/bin`, or
the Windows equivalent). Nothing needs admin rights and nothing is added
to PATH.

Where they come from, and how they are checked:

| tool | source | verified against |
|---|---|---|
| yt-dlp | `github.com/yt-dlp/yt-dlp` latest release | its published `SHA2-256SUMS` |
| ffmpeg + ffprobe | `github.com/BtbN/FFmpeg-Builds` latest GPL build | its published `checksums.sha256` |

Every download is SHA-256 checked against the hash the project publishes
beside it, and a mismatch aborts. The GPL ffmpeg builds are the ones
carrying NVENC, AMF and VAAPI. The yt-dlp binaries are PyInstaller
bundles with their own Python inside, so installing one adds no runtime
to the machine.

**macOS ffmpeg is the exception.** There is no upstream static build at
a stable URL, so ytclip tells you to `brew install ffmpeg` instead. Every
macOS ffmpeg carries VideoToolbox, which is the encoder that matters
there anyway. yt-dlp still auto-installs on macOS.

`--install` skips anything already present. If you deliberately want
ytclip's own copies over your system ones, `--reinstall` forces it —
`--doctor` then tells you it is shadowing PATH, and `--uninstall` undoes
it.

Binaries dropped next to `ytclip` are picked up too, so a fully portable
folder with all four works.

## Use

```
ytclip
```

That is it — the TUI walks through URL, quality, format, time range.

### Time formats

Start and end accept whatever you would naturally type. The form echoes
back what it understood before anything runs.

```
1:20:00                    clock
1:20                       mm:ss
90                         bare seconds
1h20m   1h 20m 30s         compact units
1 hr 20 minute             spelled out
1 hour and 20 minutes      commas and "and" are ignored
20 minutes    45 sec       single unit
1.5h                       decimals
1h 20                      a trailing bare number takes the next unit down
```

Leave both blank for the whole video; leave one blank for
start-of-video or end-of-video.

Worth noting: Go's `time.ParseDuration`, and every duration library
built on it, rejects all but the compact forms — no spaces, no
spelled-out units, no clock form, no bare seconds. Hence the parser in
`internal/core/timecode.go`, which is tested to accept everything the
stdlib does plus the rest.

```
ytclip --doctor          # dependency + encoder report
ytclip --no-convert      # skip the H.264 re-encode, keep the source codec
ytclip --quality 23      # CRF scale, lower is better (default 20)
ytclip --speed quality   # fast | balanced | quality
ytclip --encoder libx264 # force one encoder
ytclip --out ~/clips     # output directory (default ./clips)
```

## Why it is fast

Every format selector asks for YouTube's HLS (`m3u8`) variant first.

YouTube serves the same video two ways. Over `https` it is one large
file on one connection, which Google throttles hard — measured ~0.5
MB/s. Over `m3u8` it is the same stream cut into segments, each a
separate request, so the per-connection throttle never gets a chance to
bite — measured ~12.5 MB/s.

Same itag, same bitrate, same pixels. 25x for identical output.

`--no-hls` turns it off if you want to watch the fallback happen.

## Encoder detection

`ffmpeg -encoders` listing an encoder proves the build has the code. It
does not prove this machine has the GPU, the driver, or a display server
to talk to.

So each candidate gets a real one-frame encode before it is offered:

```
$ ytclip --doctor

ENCODER            WORKS  DETAIL
h264_nvenc         YES
h264_qsv           no     Error creating a MFX session: -9.
h264_videotoolbox  no     not available on linux
h264_amf           no     DLL libamfrt64.so.1 failed to open
h264_vaapi         no     No usable encoding entrypoint found for profile ...
libx264            YES
```

Three of those are *listed* by this ffmpeg build and all three fail.
Preference order is NVENC → Quick Sync → VideoToolbox → AMF → VAAPI →
libx264, and the result is cached per ffmpeg build for a week.

Quality is always given on the CRF scale because that is the one people
know. Each encoder maps it to its own units — they are not
interchangeable, and VideoToolbox's `-q:v` runs the opposite direction.

## The traps it knows about

Four ways this job fails silently. Each has a check.

**A livestream that is still processing has no seek index.** Ask for a
section of one and ffmpeg's seek lands nowhere: it returns ~2 seconds
from the tail of the DVR window, reports a negative timestamp, and exits
0. yt-dlp succeeds, a file exists, it really is 1440p60 — it is just 2
seconds long instead of 16 minutes. `ytclip` detects the live status up
front and offers to download the whole stream and cut locally, which is
the only thing that works.

**A section download can come back short anyway.** So the file is
measured against what was asked for, with a keyframe-drift tolerance,
before an encode is spent on it.

**Format selectors fall through without a word.** Ask for 2K and a
fallback branch can hand back 360p with no error anywhere. The file is
probed and a real degradation stops the run.

**YouTube publishes no H.264 above 1080p, and exactly one combined
format (360p30).** Picking "Best MP4" at 1440p silently gets you 720p;
picking "already combined" silently ignores your quality entirely. Both
are capped and labelled in the form instead.

## Layout

```
main.go                    flags, --doctor
internal/core/             no UI, all testable
  tools.go                 binary discovery + preflight
  encoder.go               the H.264 encoder registry
  detect.go                list, probe for real, cache
  probe.go                 yt-dlp metadata, ffprobe
  format.go                quality/format menus -> selector strings
  job.go                   argument building
  verify.go                the silent-failure checks
  run.go                   progress streaming
internal/tui/              bubbletea, consumes core
```

`internal/core` has no dependency on the TUI, so a non-interactive CLI
mode is a matter of a second front end.

## Develop

```
make            build ./ytclip for this machine
make run        build and run it
make test       unit tests
make check      gofmt check + go vet + tests
make install    copy onto PATH (PREFIX=~/.local by default)
make clean      remove build output
```

## Release builds

```
make dist       every target, archived and checksummed, into dist/
make linux      just linux/amd64 and linux/arm64
make windows    just windows/amd64 and windows/arm64
make -j dist    the same, in parallel
```

Every cross target is CGO-free, so any host builds any of them — the
Windows and ARM binaries in `dist/` come off a Linux x86 machine with no
toolchain beyond Go.

`make dist` produces:

```
ytclip_<version>_linux_amd64.tar.gz     binary + START-HERE.txt + README
ytclip_<version>_linux_arm64.tar.gz
ytclip_<version>_windows_amd64.zip
ytclip_<version>_windows_arm64.zip
checksums.txt
```

Version and commit are taken from `git describe` and stamped into the
binary (`ytclip --version`); an untagged local build reports `dev`.
Override with `make dist VERSION=1.2.0`.

Adding a platform is one word in the `LINUX_TARGETS` or
`WINDOWS_TARGETS` list at the top of the Makefile.

`goreleaser release --clean` also works, once the repo has a remote.
