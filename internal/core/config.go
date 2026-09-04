package core

// Config is everything the .ps1 kept as top-of-file constants.
//
// A compiled binary cannot be edited the way a script can, so these
// have to be reachable from flags and a config file. Defaults here
// match the tuned values the scripts shipped with.
type Config struct {
	// PreferHLS orders every format selector to try YouTube's
	// segmented m3u8 delivery first.
	//
	// THIS IS THE SPEED SETTING. Leave it on.
	//
	// https -> one giant file over one connection, which Google
	// throttles hard: measured ~0.5 MB/s. m3u8 -> the same stream cut
	// into segments, each a separate request, so the per-connection
	// throttle never gets a chance to bite: measured ~12.5 MB/s.
	// Same itag, same bitrate, same pixels. 25x for identical output.
	PreferHLS bool

	// ConcurrentFragments is the parallel segment fetch count. Only
	// bites on fragmented formats, which is why PreferHLS exists.
	ConcurrentFragments int

	// ConvertToH264 runs the stage 2 re-encode.
	//
	// VP9/AV1 decode poorly in most NLEs, which is the whole reason
	// this exists. Archiving or re-uploading? Turn it off and skip
	// the entire encode stage.
	ConvertToH264 bool

	// Quality is on the CRF scale: lower is better and larger. Each
	// encoder maps it to its own units. 20 is a generous editing
	// intermediate, well above YouTube's source bitrate; 23-26 gives
	// something closer to the original file size.
	Quality int

	// Speed trades encode time for quality.
	Speed Speed

	// Encoder forces one ffmpeg encoder by name. Empty means auto:
	// the highest-priority candidate that passes a real probe.
	Encoder string

	// Padding adds seconds either side of the requested section.
	Padding float64

	// OutputDir holds the finished clips. Empty means ./clips next to
	// the binary.
	OutputDir string
}

// DefaultConfig returns the shipped defaults.
func DefaultConfig() Config {
	return Config{
		PreferHLS:           true,
		ConcurrentFragments: 16,
		ConvertToH264:       true,
		Quality:             20,
		Speed:               SpeedBalanced,
		Padding:             0,
	}
}
