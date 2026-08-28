package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// bandKind is the filter shape, using the vocabulary of AutoEq and Equalizer
// APO files so an imported profile needs no translation table.
type bandKind string

const (
	bandPeak      bandKind = "pk" // peaking, the workhorse
	bandLowShelf  bandKind = "ls" // LSC
	bandHighShelf bandKind = "hs" // HSC
	bandHighPass  bandKind = "hp" // HP - gain is ignored
	bandLowPass   bandKind = "lp" // LP - gain is ignored
)

type band struct {
	Kind bandKind `json:"kind"`
	Freq float64  `json:"freq"`
	Gain float64  `json:"gain"`
	Q    float64  `json:"q"`
}

// profile is one sound profile: a preamp, a list of bands, and any extra mpv
// audio filters that are not bands (dynaudnorm for the night profile).
//
// One representation for every source. An AutoEq download, an Equalizer APO
// config and a baked-in curve all land in this struct, so the renderer and the
// editor each have exactly one input shape to handle.
type profile struct {
	Key    string
	Name   string
	Source string // where the numbers came from, shown on the sound page
	Preamp float64
	Bands  []band
	Extra  []string
}

// Sanity bounds. Imported files are not trusted: a stray parse can otherwise
// hand mpv a 400 dB peak, and mpv will happily oblige.
const (
	minGain, maxGain = -24.0, 24.0
	minQ, maxQ       = 0.1, 20.0
	minFreq, maxFreq = 10.0, 22000.0
)

func clampBand(b band) band {
	b.Freq = math.Min(maxFreq, math.Max(minFreq, b.Freq))
	b.Gain = math.Min(maxGain, math.Max(minGain, b.Gain))
	if b.Q <= 0 {
		b.Q = 0.707 // what APO assumes when a line omits Q
	}
	b.Q = math.Min(maxQ, math.Max(minQ, b.Q))
	return b
}

// profiles are the baked-in curves.
//
// Sourcing rules for anything added here: cite the upstream file, and only
// include stages that actually survive the trip to a stereo mpv filter chain.
// No invented numbers, and no curve that assumes plugins we do not run.
//
// Deliberately absent: AsahiLinux/asahi-audio. Its per-Mac configs are
// six-channel crossovers with per-driver convolution plus LV2 bass exciter and
// loudness compensation, built to REPLACE Apple's DSP on Linux. macOS applies
// that correction downstream of anything we send it, so porting those curves
// here would double-process the speakers, not correct them.
var profiles = []profile{
	{
		Key: "flat", Name: "Flat (off)",
		Source: "no processing - macOS already corrects the built-in speakers",
	},
	{
		Key: "mbp-speakers", Name: "MacBook Pro speakers",
		// github.com/Naozumi520/mbp-16-bootcamp-speaker-mod, config/config.txt:
		// the four PK stages, the 90 Hz high-pass and the -15 dB preamp at the
		// end of its chain. Its Waves MaxxBass/RBass convolution and VST stages
		// have no ffmpeg equivalent and are not reproduced, so the preamp is
		// pulled back to -6 dB: -15 dB existed to leave headroom for a bass
		// exciter we are not running.
		Source: "mbp-16-bootcamp-speaker-mod (EQ stages only)",
		Preamp: -6,
		Bands: []band{
			{bandHighPass, 90, 0, 0.707},
			{bandPeak, 635, -4, 1},
			{bandPeak, 700, -5, 1},
			{bandPeak, 800, 3, 1.25},
			{bandPeak, 3500, -2, 0.7},
		},
	},
	{
		Key: "mbp-vocal", Name: "MacBook Pro vocal clarity",
		// Same repo, config/DynamiQ-master/Extra Vocal Clarity.txt, verbatim.
		// Aggressive by design: it trades bass and air for midrange presence,
		// which is what makes speech legible on tiny drivers.
		Source: "mbp-16-bootcamp-speaker-mod, Extra Vocal Clarity",
		Preamp: -6,
		Bands: []band{
			{bandLowShelf, 400, -12, 0.5},
			{bandHighShelf, 16000, -6, 0.5},
		},
	},
	{
		Key: "airpods-max", Name: "AirPods Max",
		// AutoEq: results/oratory1990/over-ear/Apple AirPods Max/
		// Apple AirPods Max ParametricEQ.txt
		Source: "AutoEq, oratory1990 measurement",
		Preamp: -4.7,
		Bands: []band{
			{bandLowShelf, 105, -3.0, 0.70},
			{bandPeak, 7273, 3.6, 2.41},
			{bandPeak, 218, -2.9, 1.41},
			{bandPeak, 1031, -3.2, 0.99},
			{bandPeak, 3185, 3.1, 0.56},
			{bandHighShelf, 10000, -5.5, 0.70},
			{bandPeak, 9508, 2.7, 2.20},
			{bandPeak, 66, 0.6, 1.65},
			{bandPeak, 4045, 2.1, 5.82},
			{bandPeak, 4834, -1.8, 6.00},
		},
	},
	{
		Key: "airpods-pro-2", Name: "AirPods Pro 2",
		// AutoEq: results/Harpo/in-ear/Apple Airpods Pro 2/
		// Apple Airpods Pro 2 ParametricEQ.txt
		Source: "AutoEq, Harpo measurement",
		Preamp: -5.1,
		Bands: []band{
			{bandLowShelf, 105, 0.4, 0.70},
			{bandPeak, 305, -3.4, 0.58},
			{bandPeak, 4977, 5.1, 1.73},
			{bandPeak, 1077, 2.6, 1.21},
			{bandPeak, 75, 1.0, 2.04},
			{bandHighShelf, 10000, 2.1, 0.70},
			{bandPeak, 8774, 1.1, 4.87},
			{bandPeak, 3085, -0.9, 5.21},
			{bandPeak, 6603, -1.3, 6.00},
			{bandPeak, 40, -0.3, 2.18},
		},
	},
	{
		Key: "night", Name: "Night",
		// Authored here, not imported: a shelf pair plus ffmpeg's dynamic
		// normaliser, so quiet passages stay audible without the loud ones
		// carrying through a wall.
		Source: "authored - dynaudnorm plus gentle shelves",
		Preamp: -3,
		Bands: []band{
			{bandLowShelf, 120, -4, 0.7},
			{bandHighShelf, 8000, -2, 0.7},
		},
		Extra: []string{"dynaudnorm=f=200:g=5:p=0.9"},
	},
	{
		Key: "bass", Name: "Bass boost",
		// Authored here. The 40 Hz high-pass is not decoration: without it the
		// shelf below feeds the drivers energy they can only turn into
		// distortion and excursion.
		Source: "authored - shelf lift with an excursion guard",
		Preamp: -6,
		Bands: []band{
			{bandHighPass, 40, 0, 0.707},
			{bandLowShelf, 110, 6, 0.6},
			{bandPeak, 3000, -1.5, 0.8},
		},
	},
}

func profileByKey(key string) (profile, bool) {
	for _, p := range profiles {
		if p.Key == key {
			return p, true
		}
	}
	return profile{}, false
}

// defaultProfileName is flat on purpose. macOS applies Apple's own speaker
// correction downstream of us, so "no processing" is the honest starting point;
// everything else in the list is taste, and taste is the user's to pick.
func defaultProfileName() string { return "flat" }

// speakerNote describes the machine for the sound page header, so the reader
// knows why the speaker profiles are labelled taste rather than correction.
func speakerNote() string {
	m := macModel()
	if m == "" {
		return ""
	}
	name := map[string]string{
		"Mac16,1": `MacBook Pro 14" (M4)`, "Mac16,5": `MacBook Pro 16" (M4 Pro/Max)`,
		"Mac16,6": `MacBook Pro 14" (M4 Pro/Max)`, "Mac16,7": `MacBook Pro 16" (M4 Pro/Max)`,
		"Mac16,8": `MacBook Pro 14" (M4 Pro/Max)`,
		"Mac15,3": `MacBook Pro 14" (M3)`, "Mac15,6": `MacBook Pro 14" (M3 Pro/Max)`,
		"Mac15,7": `MacBook Pro 16" (M3 Pro/Max)`, "Mac15,8": `MacBook Pro 14" (M3 Pro/Max)`,
		"Mac15,9": `MacBook Pro 16" (M3 Pro/Max)`,
		"Mac14,5": `MacBook Pro 14" (M2 Pro/Max)`, "Mac14,6": `MacBook Pro 16" (M2 Pro/Max)`,
		"Mac14,7":        `MacBook Pro 13" (M2)`,
		"MacBookPro18,1": `MacBook Pro 16" (M1 Pro/Max)`,
		"MacBookPro18,3": `MacBook Pro 14" (M1 Pro/Max)`,
		"MacBookPro17,1": `MacBook Pro 13" (M1)`,
		"MacBookAir10,1": `MacBook Air (M1)`,
		"Mac14,2":        `MacBook Air (M2)`,
		"Mac15,12":       `MacBook Air (M3)`,
		"Mac16,12":       `MacBook Air (M4)`,
	}[m]
	if name == "" {
		name = m
	}
	return name + " - macOS applies its own speaker correction; these profiles are taste on top"
}

// --- rendering to an mpv filter chain --------------------------------------

// afChain renders a profile as an mpv --af value. Values are formatted with %g
// and none of them can contain a comma, which is what mpv splits filters on.
func afChain(p profile) string {
	var parts []string
	if p.Preamp != 0 {
		parts = append(parts, fmt.Sprintf("volume=volume=%gdB", clampGain(p.Preamp)))
	}
	for _, raw := range p.Bands {
		b := clampBand(raw)
		switch b.Kind {
		case bandPeak:
			parts = append(parts, fmt.Sprintf("equalizer=f=%g:width_type=q:w=%g:g=%g", b.Freq, b.Q, b.Gain))
		case bandLowShelf:
			parts = append(parts, fmt.Sprintf("bass=f=%g:width_type=q:w=%g:g=%g", b.Freq, b.Q, b.Gain))
		case bandHighShelf:
			parts = append(parts, fmt.Sprintf("treble=f=%g:width_type=q:w=%g:g=%g", b.Freq, b.Q, b.Gain))
		case bandHighPass:
			parts = append(parts, fmt.Sprintf("highpass=f=%g:width_type=q:w=%g", b.Freq, b.Q))
		case bandLowPass:
			parts = append(parts, fmt.Sprintf("lowpass=f=%g:width_type=q:w=%g", b.Freq, b.Q))
		}
	}
	parts = append(parts, p.Extra...)
	return strings.Join(parts, ",")
}

func clampGain(g float64) float64 { return math.Min(maxGain, math.Max(minGain, g)) }

// --- importing AutoEq / Equalizer APO files --------------------------------

// parseParametricEQ reads an AutoEq ParametricEQ.txt or an Equalizer APO
// config and returns it as a profile. Both formats share the line grammar:
//
//	Preamp: -4.7 dB
//	Filter 1: ON PK Fc 7273 Hz Gain 3.6 dB Q 2.41
//	Filter: ON HP Fc 90 Hz
//
// Unsupported filter shapes (band-pass, notch, all-pass) are skipped rather
// than approximated, and reported in skipped so the caller can say so out
// loud. A file with no usable band at all is an error: silently loading an
// empty curve would look like the EQ is broken.
func parseParametricEQ(r io.Reader) (p profile, skipped int, err error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(strings.ToLower(line), "preamp:"):
			if v, ok := firstFloat(line[len("preamp:"):]); ok {
				p.Preamp = v
			}
		case strings.HasPrefix(strings.ToLower(line), "filter"):
			b, ok := parseFilterLine(line)
			if !ok {
				skipped++
				continue
			}
			p.Bands = append(p.Bands, clampBand(b))
		}
		// Every other APO directive (Device:, Copy:, Convolution:, VSTPlugin:,
		// Include:) describes routing or plugins we cannot honour. Ignoring
		// them is intentional; the caller warns when skipped > 0.
	}
	if err := sc.Err(); err != nil {
		return profile{}, skipped, err
	}
	if len(p.Bands) == 0 {
		return profile{}, skipped, fmt.Errorf("no usable filter lines found")
	}
	return p, skipped, nil
}

// parseFilterLine reads one "Filter ...: ON <TYPE> Fc <f> Hz [Gain <g> dB] [Q <q>]"
// line. A filter marked OFF is not a parse failure, but it is not a band
// either, so it reports ok=false and lands in the skipped count.
func parseFilterLine(line string) (band, bool) {
	_, rest, found := strings.Cut(line, ":")
	if !found {
		return band{}, false
	}
	f := strings.Fields(rest)
	if len(f) < 2 || !strings.EqualFold(f[0], "ON") {
		return band{}, false
	}

	var b band
	switch strings.ToUpper(f[1]) {
	case "PK", "PEQ", "MODAL":
		b.Kind = bandPeak
	case "LSC", "LS", "LSQ":
		b.Kind = bandLowShelf
	case "HSC", "HS", "HSQ":
		b.Kind = bandHighShelf
	case "HP", "HPQ":
		b.Kind = bandHighPass
	case "LP", "LPQ":
		b.Kind = bandLowPass
	default:
		return band{}, false // BP, NO, AP and friends: skipped, not faked
	}

	// Read the labelled values positionally-independently: APO exporters differ
	// on ordering and on whether the unit words are present.
	for i := 2; i < len(f); i++ {
		switch strings.ToLower(f[i]) {
		case "fc", "freq":
			if v, ok := nextFloat(f, i); ok {
				b.Freq = v
			}
		case "gain":
			if v, ok := nextFloat(f, i); ok {
				b.Gain = v
			}
		case "q":
			if v, ok := nextFloat(f, i); ok {
				b.Q = v
			}
		}
	}
	if b.Freq <= 0 {
		return band{}, false
	}
	return b, true
}

func nextFloat(fields []string, i int) (float64, bool) {
	if i+1 >= len(fields) {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[i+1], 64)
	return v, err == nil
}

// firstFloat pulls the first parseable number out of a fragment like " -4.7 dB".
func firstFloat(s string) (float64, bool) {
	for _, tok := range strings.Fields(s) {
		if v, err := strconv.ParseFloat(tok, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// loadProfileFile imports a profile from disk, naming it after the file.
func loadProfileFile(path string) (profile, int, error) {
	f, err := os.Open(expandTilde(path))
	if err != nil {
		return profile{}, 0, err
	}
	defer f.Close()

	p, skipped, err := parseParametricEQ(f)
	if err != nil {
		return profile{}, skipped, err
	}
	p.Key = "file:" + path
	p.Name = strings.TrimSuffix(baseName(path), " ParametricEQ.txt")
	p.Source = "imported from " + path
	return p, skipped, nil
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
