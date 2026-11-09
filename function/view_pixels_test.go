package function

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"testing"
)

func TestViewPixelGeometry(t *testing.T) {
	// Keep the oracle independent of imaging: letters identify exact pixels in
	// a small, asymmetric grid, and each expected grid is specified by hand.
	palette := map[byte]color.NRGBA{
		'A': {255, 0, 0, 255}, 'B': {0, 255, 0, 255}, 'C': {0, 0, 255, 255},
		'D': {255, 255, 0, 255}, 'E': {255, 0, 255, 255}, 'F': {0, 255, 255, 255},
	}
	grid := func(rows ...string) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, len(rows[0]), len(rows)))
		for y, row := range rows {
			for x := range len(row) {
				img.SetNRGBA(x, y, palette[row[x]])
			}
		}
		return img
	}
	path := pngFixture(t, grid("ABC", "DEF"))
	crop := []any{json.Number("0.5"), number(0), number(1), number(1)}
	for _, tc := range []struct {
		name string
		args map[string]any
		rows []string
	}{
		{"identity", nil, []string{"ABC", "DEF"}},
		{"horizontal", map[string]any{"horizontal_flip": true}, []string{"CBA", "FED"}},
		{"vertical", map[string]any{"vertical_flip": true}, []string{"DEF", "ABC"}},
		{"counterclockwise 90", map[string]any{"rotate": number(90)}, []string{"CF", "BE", "AD"}},
		{"counterclockwise 180", map[string]any{"rotate": number(180)}, []string{"FED", "CBA"}},
		{"counterclockwise 270", map[string]any{"rotate": number(270)}, []string{"DA", "EB", "FC"}},
		{"crop rounding", map[string]any{"crop": crop}, []string{"BC", "EF"}},
		{"crop before horizontal flip", map[string]any{"crop": crop, "horizontal_flip": true}, []string{"CB", "FE"}},
		{"horizontal flip before rotation", map[string]any{"horizontal_flip": true, "rotate": number(90)}, []string{"AD", "BE", "CF"}},
		{"vertical flip before rotation", map[string]any{"vertical_flip": true, "rotate": number(90)}, []string{"FC", "EB", "DA"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, v, _, _ := NewFileFunctions()
			args := map[string]any{"path": path}
			for key, value := range tc.args {
				args[key] = value
			}
			requireImageEqual(t, decodeView(t, invoke(t, v, context.Background(), args)), grid(tc.rows...))
			requireEmptyRegistry(t, v.locks)
		})
	}
}

func TestViewEXIFOrientationPixels(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, sampleImage(5, 3), &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	// Compare with the decoded JPEG, not the pre-compression fixture: JPEG is
	// lossy, but adding an EXIF segment must only rearrange its decoded pixels.
	base, err := jpeg.Decode(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	w, h := base.Bounds().Dx(), base.Bounds().Dy()
	for _, order := range []struct {
		name  string
		order binary.ByteOrder
	}{
		{"little endian", binary.LittleEndian},
		{"big endian", binary.BigEndian},
	} {
		for orientation := uint16(1); orientation <= 8; orientation++ {
			t.Run(fmt.Sprintf("%s/%d", order.name, orientation), func(t *testing.T) {
				// EXIF header, TIFF header, one IFD entry, and a null next-IFD pointer.
				exif := make([]byte, 32)
				copy(exif, "Exif\x00\x00")
				if order.order == binary.LittleEndian {
					copy(exif[6:], "II")
				} else {
					copy(exif[6:], "MM")
				}
				order.order.PutUint16(exif[8:], 42)
				order.order.PutUint32(exif[10:], 8)
				order.order.PutUint16(exif[14:], 1)
				order.order.PutUint16(exif[16:], 0x0112) // orientation tag
				order.order.PutUint16(exif[18:], 3)      // SHORT
				order.order.PutUint32(exif[20:], 1)
				order.order.PutUint16(exif[24:], orientation)
				var data bytes.Buffer
				data.Write(encoded.Bytes()[:2]) // SOI
				data.Write([]byte{0xff, 0xe1, 0, byte(len(exif) + 2)})
				data.Write(exif)
				data.Write(encoded.Bytes()[2:])
				path := filepath.Join(t.TempDir(), "oriented.jpg")
				writeFixture(t, path, data.Bytes())

				bounds := image.Rect(0, 0, w, h)
				if orientation >= 5 {
					bounds = image.Rect(0, 0, h, w)
				}
				want := image.NewNRGBA(bounds)
				for y := range h {
					for x := range w {
						dx, dy := x, y
						switch orientation {
						case 2:
							dx = w - 1 - x
						case 3:
							dx, dy = w-1-x, h-1-y
						case 4:
							dy = h - 1 - y
						case 5:
							dx, dy = y, x
						case 6:
							dx, dy = h-1-y, x
						case 7:
							dx, dy = h-1-y, w-1-x
						case 8:
							dx, dy = y, w-1-x
						}
						want.Set(dx, dy, base.At(x, y))
					}
				}
				_, v, _, _ := NewFileFunctions()
				requireImageEqual(t, decodeView(t, invoke(t, v, context.Background(), map[string]any{"path": path})), want)
				requireEmptyRegistry(t, v.locks)
			})
		}
	}
}
