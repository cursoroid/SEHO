package main

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	_ "image/jpeg" // embedded cover art is almost always JPEG
	_ "image/png"
	"os"
	"strings"

	"github.com/dhowden/tag"
)

// Real covers, hi-res scans included, essentially never exceed 3000 square, and
// we downsample to 28x28 regardless — so this rejects bombs without ever
// refusing a genuine cover.
// ponytail: a dimension gate, not a streaming decoder. The caller already falls
// back to a generated tile, so "too big" and "no art" take the same path.
const maxCoverPixels = 4096 * 4096

// AlbumArt renders a track's embedded cover as tview markup: h lines of w
// cells, each cell two stacked pixels. Falls back to a flat block keyed off
// the file path when there is no usable picture.
func AlbumArt(path string, w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	img, err := coverImage(path)
	if err != nil {
		return fallbackBlock(path, w, h)
	}
	return halfBlocks(img, w, h)
}

func coverImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	md, err := tag.ReadFrom(f)
	if err != nil {
		return nil, err
	}
	pic := md.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, fmt.Errorf("no embedded picture in %s", path)
	}
	return decodeCoverBytes(pic.Data)
}

// decodeCoverBytes decodes raw embedded-picture bytes, rejecting anything
// whose declared dimensions exceed maxCoverPixels before allocating a pixel
// buffer for it. Split out from coverImage so the dimension gate can be unit
// tested with a hand-built payload instead of a real tagged audio fixture.
func decodeCoverBytes(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("cover art header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		int64(cfg.Width)*int64(cfg.Height) > maxCoverPixels {
		return nil, fmt.Errorf("cover art %dx%d exceeds the decode limit", cfg.Width, cfg.Height)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// halfBlocks maps the image onto a w×h grid of cells. Each cell is the
// upper-half-block rune with foreground = upper pixel, background = lower.
func halfBlocks(img image.Image, w, h int) string {
	var b strings.Builder
	b.Grow(w * h * 24)
	for cy := 0; cy < h; cy++ {
		for cx := 0; cx < w; cx++ {
			top := boxAverage(img, cx, cy*2, w, h*2)
			bot := boxAverage(img, cx, cy*2+1, w, h*2)
			fmt.Fprintf(&b, "[#%02x%02x%02x:#%02x%02x%02x]▀",
				top.R, top.G, top.B, bot.R, bot.G, bot.B)
		}
		b.WriteString("[-:-]\n")
	}
	return b.String()
}

// boxAverage averages every source pixel falling inside cell (gx,gy) of a
// gw×gh grid. Averaging rather than sampling is what keeps a 500px cover
// legible at 28px.
func boxAverage(img image.Image, gx, gy, gw, gh int) color.RGBA {
	bd := img.Bounds()
	x0 := bd.Min.X + gx*bd.Dx()/gw
	x1 := bd.Min.X + (gx+1)*bd.Dx()/gw
	y0 := bd.Min.Y + gy*bd.Dy()/gh
	y1 := bd.Min.Y + (gy+1)*bd.Dy()/gh
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}

	var r, g, bl, n uint64
	for y := y0; y < y1 && y < bd.Max.Y; y++ {
		for x := x0; x < x1 && x < bd.Max.X; x++ {
			cr, cg, cb, ca := img.At(x, y).RGBA()
			if ca == 0 {
				continue // fully transparent pixel contributes nothing
			}
			// img.At(...).RGBA() returns alpha-premultiplied components; undo
			// that so a partially transparent PNG cover does not blend to
			// black, then drop back to 8 bits.
			r += uint64(cr) * 0xffff / uint64(ca) >> 8
			g += uint64(cg) * 0xffff / uint64(ca) >> 8
			bl += uint64(cb) * 0xffff / uint64(ca) >> 8
			n++
		}
	}
	if n == 0 {
		return color.RGBA{0, 0, 0, 255}
	}
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255}
}

// fallbackBlock derives a stable muted color from the path so tracks without
// art still get a distinct, non-jarring tile.
func fallbackBlock(seed string, w, h int) string {
	sum := fnv.New32a()
	sum.Write([]byte(seed))
	v := sum.Sum32()
	// Bias toward Mocha's darker surfaces rather than full-saturation noise.
	c := color.RGBA{
		R: uint8(0x30 + v&0x3f),
		G: uint8(0x30 + (v>>8)&0x3f),
		B: uint8(0x40 + (v>>16)&0x3f),
		A: 255,
	}
	cell := fmt.Sprintf("[#%02x%02x%02x:#%02x%02x%02x]▀", c.R, c.G, c.B, c.R, c.G, c.B)
	var b strings.Builder
	for y := 0; y < h; y++ {
		b.WriteString(strings.Repeat(cell, w))
		b.WriteString("[-:-]\n")
	}
	return b.String()
}
