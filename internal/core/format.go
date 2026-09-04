package core

import (
	"fmt"
	"strings"
)

// Quality is a requested vertical resolution ceiling.
// Height 0 means "best available, no ceiling".
type Quality struct {
	Name   string
	Height int
}

// Qualities is the menu, in display order.
var Qualities = []Quality{
	{"Best available", 0},
	{"4K (up to 2160p)", 2160},
	{"2K / 1440p", 1440},
	{"1080p", 1080},
	{"720p", 720},
	{"480p", 480},
	{"360p", 360},
	{"240p", 240},
	{"144p", 144},
}

// FormatKind identifies one entry in the download-format menu.
type FormatKind int

const (
	FormatBestAV FormatKind = iota
	FormatVideoOnly
	FormatMP4AV
	FormatCombined
	FormatAudioOnly
)

// FormatChoice is one menu entry plus the traps that come with it.
type FormatChoice struct {
	Kind  FormatKind
	Name  string
	Blurb string
	// Warn is shown before the user commits. Empty means no warning.
	Warn string
	// HardCapHeight caps the quality selection regardless of what the
	// user picked, because the codec cannot go higher.
	HardCapHeight int
	// IgnoresQuality means the quality selection is decoration.
	IgnoresQuality bool
	ExtractAudio   bool
}

// Formats is the menu, in display order.
var Formats = []FormatChoice{
	{
		Kind:  FormatBestAV,
		Name:  "Best Video + Audio",
		Blurb: "Highest quality video with best audio",
	},
	{
		Kind:  FormatVideoOnly,
		Name:  "Best Video Only",
		Blurb: "Video only, no audio",
	},
	{
		Kind:  FormatMP4AV,
		Name:  "Best MP4 Video + Audio",
		Blurb: "Prefer H.264/MP4 video when available",
		// Option 3 asks for avc1. YouTube only publishes avc1 up to
		// 1080p, so above that the branch matches nothing and yt-dlp
		// silently drops to a low-res combined format. Cap it here
		// and say so, rather than quietly handing back 720p.
		Warn: "YouTube does not publish H.264 above 1080p. " +
			"This downloads 1080p H.264. For real 1440p/4K use " +
			"\"Best Video + Audio\" and let stage 2 convert.",
		HardCapHeight: 1080,
	},
	{
		Kind:  FormatCombined,
		Name:  "Best Video + Audio Already Combined",
		Blurb: "Single-file format when available",
		// YouTube publishes exactly one combined format: 18, which is
		// 640x360 at 30fps. Everything above that arrives as separate
		// streams that must be muxed. On many livestream VODs format
		// 18 does not exist at all and this simply fails.
		Warn: "YouTube's only combined format is 360p30. Your quality " +
			"choice will be ignored, and on many livestream VODs this " +
			"format does not exist at all.",
		IgnoresQuality: true,
	},
	{
		Kind:         FormatAudioOnly,
		Name:         "Audio Only",
		Blurb:        "Best available audio",
		ExtractAudio: true,
	},
}

// Selection is a resolved quality+format pair.
type Selection struct {
	Quality Quality
	Format  FormatChoice
	// EffectiveHeight is Quality.Height after any hard cap.
	EffectiveHeight int
	PreferHLS       bool
}

// NewSelection applies the format's hard cap to the requested quality.
func NewSelection(q Quality, f FormatChoice, preferHLS bool) Selection {
	h := q.Height
	if f.HardCapHeight > 0 && (h == 0 || h > f.HardCapHeight) {
		h = f.HardCapHeight
	}
	return Selection{Quality: q, Format: f, EffectiveHeight: h, PreferHLS: preferHLS}
}

// Capped reports whether the format overrode the requested quality.
func (s Selection) Capped() bool {
	return s.Quality.Height == 0 && s.EffectiveHeight > 0 ||
		s.Quality.Height > 0 && s.EffectiveHeight < s.Quality.Height
}

// Selector builds the yt-dlp --format string.
//
// Every branch is "HLS variant / anything". yt-dlp walks the list left
// to right, so we get the fast segmented delivery when it exists and
// the slow single-connection path only when it genuinely does not.
//
// That ordering is the whole speed story: same itag, same bitrate, same
// pixels, ~25x throughput, because a per-segment request never gives
// Google's per-connection throttle time to bite.
func (s Selection) Selector() string {
	height := ""
	if s.EffectiveHeight > 0 {
		height = fmt.Sprintf("[height<=%d]", s.EffectiveHeight)
	}

	hls := ""
	if s.PreferHLS {
		hls = "[protocol^=m3u8]"
	}

	var branches []string
	switch s.Format.Kind {
	case FormatBestAV:
		branches = []string{
			"bestvideo" + hls + height + "+bestaudio" + hls,
			"bestvideo" + height + "+bestaudio",
			"best" + height,
		}
	case FormatVideoOnly:
		branches = []string{
			"bestvideo" + hls + height,
			"bestvideo" + height,
		}
	case FormatMP4AV:
		branches = []string{
			"bestvideo" + hls + height + "[vcodec^=avc1]+bestaudio" + hls,
			"bestvideo" + height + "[vcodec^=avc1]+bestaudio[acodec^=mp4a]",
			"best" + height,
		}
	case FormatCombined:
		branches = []string{
			"best" + hls + height,
			"best" + height,
		}
	case FormatAudioOnly:
		branches = []string{
			"bestaudio" + hls,
			"bestaudio",
		}
	}

	// Deduplicate: with PreferHLS off the HLS and plain branches are
	// identical strings, and a repeated branch is just noise in logs.
	seen := map[string]bool{}
	out := branches[:0]
	for _, b := range branches {
		if seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	return strings.Join(out, "/")
}
