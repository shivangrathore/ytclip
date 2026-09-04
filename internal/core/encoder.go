package core

import (
	"path/filepath"
	"runtime"
	"strconv"
)

// Speed is an encoder-independent speed/quality dial.
//
// The .ps1 exposed "p5", which is an NVENC-only concept and means
// nothing to a Mac or an Intel user. Every encoder maps these three
// onto whatever knob it actually has, and some have none at all.
type Speed string

const (
	SpeedFast     Speed = "fast"
	SpeedBalanced Speed = "balanced"
	SpeedQuality  Speed = "quality"
)

func (s Speed) valid() Speed {
	switch s {
	case SpeedFast, SpeedBalanced, SpeedQuality:
		return s
	}
	return SpeedBalanced
}

// Encoder describes one H.264 encode path.
//
// Quality is always given on the CRF scale (roughly 0-51, lower is
// better) because that is the one users already understand. Each
// encoder translates it to its own units, which are NOT interchangeable:
// NVENC's CQ 20 and VideoToolbox's q:v 20 are wildly different pictures.
type Encoder struct {
	Name   string // ffmpeg encoder name
	Label  string // shown in the UI
	Vendor string
	// GOOS restricts a candidate to one platform. Empty means any.
	// h264_videotoolbox is listed by ffmpeg builds on other platforms
	// in some distributions, and always fails there.
	GOOS string
	// Hardware means "needs a working GPU + driver", which is exactly
	// the class of encoder that lists successfully and then fails.
	Hardware bool

	// initArgs run before -i (device setup).
	initArgs func() []string
	// filters are the -vf chain needed to get frames into the right
	// memory for this encoder. Only VAAPI needs one.
	filters func() []string
	// codecArgs is the -c:v ... block.
	codecArgs func(crf int, s Speed) []string
	// decodeArgs enable matching hardware decode, so a VP9/AV1 source
	// never round-trips through the CPU. ffmpeg falls back to software
	// decode by itself when the source codec has no hardware path.
	decodeArgs func() []string
}

// CodecArgs returns the -c:v block for this encoder.
func (e *Encoder) CodecArgs(crf int, s Speed) []string {
	return e.codecArgs(clampCRF(crf), s.valid())
}

// InitArgs returns args that must precede -i.
func (e *Encoder) InitArgs() []string {
	if e.initArgs == nil {
		return nil
	}
	return e.initArgs()
}

// Filters returns the -vf chain, if any.
func (e *Encoder) Filters() []string {
	if e.filters == nil {
		return nil
	}
	return e.filters()
}

// DecodeArgs returns hardware-decode args, if any.
func (e *Encoder) DecodeArgs() []string {
	if e.decodeArgs == nil {
		return nil
	}
	return e.decodeArgs()
}

func clampCRF(v int) int {
	if v < 0 {
		return 0
	}
	if v > 51 {
		return 51
	}
	return v
}

func itoa(v int) string { return strconv.Itoa(v) }

// registry is ordered by preference: first working candidate wins.
//
// Hardware first because that is the whole point, and libx264 last
// because it always works.
var registry = []*Encoder{
	{
		Name:     "h264_nvenc",
		Label:    "NVIDIA NVENC",
		Vendor:   "nvidia",
		Hardware: true,
		codecArgs: func(crf int, s Speed) []string {
			preset := map[Speed]string{
				SpeedFast: "p1", SpeedBalanced: "p5", SpeedQuality: "p7",
			}[s]
			return []string{
				"-c:v", "h264_nvenc",
				"-preset", preset,
				"-rc", "vbr",
				"-cq", itoa(crf),
				"-b:v", "0",
				"-pix_fmt", "yuv420p",
			}
		},
		decodeArgs: func() []string { return []string{"-hwaccel", "cuda"} },
	},
	{
		Name:     "h264_qsv",
		Label:    "Intel Quick Sync",
		Vendor:   "intel",
		Hardware: true,
		codecArgs: func(crf int, s Speed) []string {
			preset := map[Speed]string{
				SpeedFast: "veryfast", SpeedBalanced: "medium", SpeedQuality: "slower",
			}[s]
			return []string{
				"-c:v", "h264_qsv",
				"-preset", preset,
				// QSV's ICQ mode. Same direction as CRF, close enough
				// in magnitude that reusing the number is honest.
				"-global_quality", itoa(crf),
				"-pix_fmt", "nv12",
			}
		},
		decodeArgs: func() []string { return []string{"-hwaccel", "qsv"} },
	},
	{
		Name:     "h264_videotoolbox",
		Label:    "Apple VideoToolbox",
		Vendor:   "apple",
		GOOS:     "darwin",
		Hardware: true,
		codecArgs: func(crf int, s Speed) []string {
			// VideoToolbox has no CRF and no preset. -q:v is 1-100 and
			// runs the OTHER way: higher is better. Map the CRF scale
			// onto it so one config number drives every encoder.
			//
			//   crf 18 -> 76    crf 23 -> 65    crf 28 -> 54
			q := 100 - int(float64(crf)*2.2)
			if q < 1 {
				q = 1
			}
			if q > 100 {
				q = 100
			}
			return []string{
				"-c:v", "h264_videotoolbox",
				"-q:v", itoa(q),
				"-pix_fmt", "yuv420p",
			}
		},
		decodeArgs: func() []string { return []string{"-hwaccel", "videotoolbox"} },
	},
	{
		Name:     "h264_amf",
		Label:    "AMD AMF",
		Vendor:   "amd",
		Hardware: true,
		codecArgs: func(crf int, s Speed) []string {
			quality := map[Speed]string{
				SpeedFast: "speed", SpeedBalanced: "balanced", SpeedQuality: "quality",
			}[s]
			return []string{
				"-c:v", "h264_amf",
				"-quality", quality,
				"-rc", "cqp",
				"-qp_i", itoa(crf),
				"-qp_p", itoa(crf),
				"-qp_b", itoa(crf),
				"-pix_fmt", "yuv420p",
			}
		},
	},
	{
		Name:     "h264_vaapi",
		Label:    "VAAPI",
		Vendor:   "vaapi",
		GOOS:     "linux",
		Hardware: true,
		initArgs: func() []string {
			d := vaapiDevice()
			if d == "" {
				return nil
			}
			return []string{"-vaapi_device", d}
		},
		// VAAPI encodes from GPU memory only, so frames have to be
		// converted and uploaded first. Without this the encoder
		// rejects every frame.
		filters: func() []string {
			return []string{"-vf", "format=nv12,hwupload"}
		},
		codecArgs: func(crf int, s Speed) []string {
			return []string{
				"-c:v", "h264_vaapi",
				"-rc_mode", "CQP",
				"-qp", itoa(crf),
			}
		},
	},
	{
		Name:   "libx264",
		Label:  "CPU (libx264)",
		Vendor: "cpu",
		codecArgs: func(crf int, s Speed) []string {
			preset := map[Speed]string{
				SpeedFast: "veryfast", SpeedBalanced: "medium", SpeedQuality: "slow",
			}[s]
			return []string{
				"-c:v", "libx264",
				"-preset", preset,
				"-crf", itoa(crf),
				"-pix_fmt", "yuv420p",
			}
		},
	},
}

// vaapiDevice returns the first DRM render node, or "".
//
// renderD* rather than card*: the render node needs no seat and no
// root, the card node generally does.
func vaapiDevice() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	matches, err := filepath.Glob("/dev/dri/renderD*")
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// ByName returns a registry entry, or nil.
func ByName(name string) *Encoder {
	for _, e := range registry {
		if e.Name == name {
			return e
		}
	}
	return nil
}

// AllEncoders returns every known candidate in preference order.
func AllEncoders() []*Encoder { return registry }
