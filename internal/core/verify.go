package core

import "math"

// Verification is the verdict on a finished download.
//
// The failure this exists for: yt-dlp exits 0, the file exists, ffprobe
// confirms 2560x1440 at 60fps - and it holds 2 seconds instead of the
// 16 minutes that were asked for. Every other check in the old scripts
// passed it. So compare what arrived against what was requested and
// refuse to encode a clip that is not the clip.
type Verification struct {
	Info MediaInfo

	// EncodeDuration is what stage 2 will actually process, and what
	// the encode ETA divides by. Measured, never assumed.
	EncodeDuration float64

	ShortDownload bool
	Expected      float64
	Received      float64

	// Degraded means the format selector fell through to something
	// far below what was asked for.
	Degraded        bool
	RequestedHeight int
}

// Verify measures the download against the request.
func (j *Job) Verify(info MediaInfo) Verification {
	v := Verification{
		Info:            info,
		EncodeDuration:  -1,
		RequestedHeight: j.Sel.EffectiveHeight,
	}

	requested := j.RequestedDuration()

	switch {
	case j.FullVideo():
		// No requested length exists, so the encode ETA has nothing to
		// divide by. Take it from the file.
		if info.HasDuration {
			v.EncodeDuration = info.Duration
		}

	case j.TrimLocally:
		// The whole stream is on disk; only the slice after the seek
		// point gets encoded, and it cannot be longer than what is
		// actually there.
		if info.HasDuration {
			available := math.Max(0, info.Duration-j.DownloadStartSec())
			if requested < 0 || requested > available {
				v.EncodeDuration = available
			} else {
				v.EncodeDuration = requested
			}
		} else if requested > 0 {
			v.EncodeDuration = requested
		}

	default:
		if requested > 0 {
			v.EncodeDuration = requested
		} else if info.HasDuration {
			v.EncodeDuration = info.Duration
		}
	}

	// ---- length check
	//
	// Only meaningful for a server-side section download. A local trim
	// has the whole file, and a full-video run has nothing to compare.
	if !j.FullVideo() && !j.TrimLocally && info.HasDuration && requested > 0 {
		expected := requested

		// A section running past the end of the video is legitimately
		// short. Expect only what could actually exist.
		if j.Meta.HasDuration {
			expected = math.Min(expected,
				math.Max(0, j.Meta.Duration-j.DownloadStartSec()))
		}

		// Keyframe alignment moves the edges by a second or so either
		// way. Anything beyond that is a real failure.
		tolerance := math.Max(2, expected*0.02)

		if info.Duration < expected-tolerance {
			v.ShortDownload = true
			v.Expected = expected
			v.Received = info.Duration
			// If the user chooses to encode it anyway, the ETA has to
			// match reality.
			v.EncodeDuration = info.Duration
		}
	}

	// ---- degradation check
	//
	// Asking for 1440p on a video that only has 1080p is normal and
	// not worth shouting about. Getting 360p when you asked for 2K
	// means a selector fell through and the clip is junk.
	if info.HasVideo && j.Sel.EffectiveHeight > 0 &&
		info.Height < j.Sel.EffectiveHeight && info.Height < 720 {
		v.Degraded = true
	}

	return v
}
