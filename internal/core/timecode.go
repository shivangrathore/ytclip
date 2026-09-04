package core

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ParseTimecode accepts three shapes, so nobody has to remember which
// one this tool wants:
//
//	01:20:00  1:20  90        clock form
//	1h20m  1h 20m 30s         unit form
//	1 hour and 20 minutes     unit form, spelled out
//
// A bare number is seconds. In unit form a trailing bare number takes
// the next unit down from the one before it, so "1h 20" is 1h20m and
// "1h 20m 30" is 1h20m30s.
func ParseTimecode(v string) (float64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty timestamp")
	}
	if strings.ContainsRune(v, ':') {
		return parseClock(v)
	}
	if strings.ContainsFunc(v, unicode.IsLetter) {
		return parseUnits(v)
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, invalidTimestamp(v)
	}
	return n, nil
}

func invalidTimestamp(v string) error {
	return fmt.Errorf("%q is not a time - try 1:20:00, 1h 20m, or 90", v)
}

// parseClock handles HH:MM:SS(.mmm), MM:SS, and SS.
func parseClock(v string) (float64, error) {
	parts := strings.Split(v, ":")
	if len(parts) > 3 {
		return 0, invalidTimestamp(v)
	}

	var total float64
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return 0, invalidTimestamp(v)
		}
		n, err := strconv.ParseFloat(p, 64)
		if err != nil || n < 0 {
			return 0, invalidTimestamp(v)
		}
		// Rightmost field is seconds, then minutes, then hours.
		total += n * math.Pow(60, float64(len(parts)-1-i))
	}
	return total, nil
}

// unitSeconds maps every spelling of a unit to its length. Longest
// match wins, so "min" is never read as "m" followed by junk.
var unitSeconds = map[string]float64{
	"h": 3600, "hr": 3600, "hrs": 3600, "hour": 3600, "hours": 3600,
	"m": 60, "min": 60, "mins": 60, "minute": 60, "minutes": 60,
	"s": 1, "sec": 1, "secs": 1, "second": 1, "seconds": 1,
}

// unitToken is one "<number><unit>" pair, with the unit optional.
var unitToken = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]*)\s*`)

// nextUnitDown is what a bare trailing number means after each unit.
var nextUnitDown = map[float64]float64{3600: 60, 60: 1}

func parseUnits(v string) (float64, error) {
	// "and" and commas are how people actually write these, and they
	// carry no meaning worth parsing.
	clean := strings.NewReplacer(",", " ", " and ", " ").Replace(" " + v + " ")

	rest := strings.TrimSpace(clean)
	var total float64
	var lastUnit float64
	seen := false

	for rest != "" {
		m := unitToken.FindStringSubmatch(rest)
		if m == nil {
			return 0, invalidTimestamp(v)
		}

		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil || n < 0 {
			return 0, invalidTimestamp(v)
		}

		var mult float64
		if m[2] == "" {
			// A bare number after a unit means the next unit down;
			// on its own it means seconds.
			if !seen {
				mult = 1
			} else if down, ok := nextUnitDown[lastUnit]; ok {
				mult = down
			} else {
				return 0, invalidTimestamp(v)
			}
		} else {
			mult = unitSeconds[strings.ToLower(m[2])]
			if mult == 0 {
				return 0, fmt.Errorf("%q is not a unit of time - use h, m, or s", m[2])
			}
		}

		total += n * mult
		lastUnit = mult
		seen = true
		rest = strings.TrimSpace(rest[len(m[0]):])
	}

	if !seen {
		return 0, invalidTimestamp(v)
	}
	return total, nil
}

// FormatTimestamp renders HH:MM:SS.mmm for ffmpeg and yt-dlp.
//
// Total hours, not hours-within-a-day: a 30 hour stream must not
// silently wrap to 06.
func FormatTimestamp(sec float64) string {
	if sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		sec = 0
	}
	ms := int64(math.Round(sec * 1000))
	h := ms / 3600000
	m := (ms % 3600000) / 60000
	s := (ms % 60000) / 1000
	milli := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, milli)
}

// FormatClock renders MM:SS for progress lines, "--:--" when unknown.
func FormatClock(sec float64) string {
	if math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 {
		return "--:--"
	}
	t := int64(sec)
	return fmt.Sprintf("%02d:%02d", t/60, t%60)
}

// FormatShortClock renders a timestamp the way a person writes one:
// 1:20:00 with hours, 4:05 without. Used to echo back what a typed
// time was understood to mean.
func FormatShortClock(sec float64) string {
	if math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 {
		return "--"
	}
	t := int64(math.Round(sec))
	h, m, s := t/3600, (t%3600)/60, t%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// FormatDurationHMS renders a human clip length: 1h 02m 03s.
func FormatDurationHMS(sec float64) string {
	if math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 {
		return "--"
	}
	t := int64(math.Round(sec))
	h, m, s := t/3600, (t%3600)/60, t%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// FormatBytes renders a size for the UI.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGT"[exp])
}
