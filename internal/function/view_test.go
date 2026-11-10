package function

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/disintegration/imaging"
)

func sampleImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{uint8(19 + x*21), uint8(23 + y*29), uint8(31 + x*11 + y*13), 255})
		}
	}
	return img
}

func pngFixture(t testing.TB, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "image.png")
	writeFixture(t, path, buf.Bytes())
	return path
}

func decodeView(t testing.TB, result functionResult) image.Image {
	t.Helper()
	requireSuccess(t, result)
	if len(result.parts) != 3 {
		t.Fatalf("view did not return an image: %#v", result.parts)
	}
	img, err := png.Decode(bytes.NewReader(result.parts[1].Content))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func requireImageEqual(t testing.TB, got, want image.Image) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("image bounds = %v, want %v", got.Bounds(), want.Bounds())
	}
	for y := want.Bounds().Min.Y; y < want.Bounds().Max.Y; y++ {
		for x := want.Bounds().Min.X; x < want.Bounds().Max.X; x++ {
			g := color.NRGBAModel.Convert(got.At(x, y))
			w := color.NRGBAModel.Convert(want.At(x, y))
			if g != w {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, g, w)
			}
		}
	}
}

func TestViewTransforms(t *testing.T) {
	input := sampleImage(8, 6)
	path := pngFixture(t, input)
	for _, tc := range []struct {
		name string
		args map[string]any
		want func(image.Image) image.Image
	}{
		{"identity", nil, func(img image.Image) image.Image { return img }},
		{"horizontal", map[string]any{"horizontal_flip": true}, func(img image.Image) image.Image { return imaging.FlipH(img) }},
		{"vertical", map[string]any{"vertical_flip": true}, func(img image.Image) image.Image { return imaging.FlipV(img) }},
		{"crop rounding", map[string]any{"crop": []any{json.Number("0.1"), json.Number("0.1"), json.Number("0.6"), json.Number("0.6")}}, func(img image.Image) image.Image { return imaging.Crop(img, image.Rect(0, 0, 5, 4)) }},
		{"empty crop", map[string]any{"crop": []any{}}, func(img image.Image) image.Image { return img }},
		{"rotate90", map[string]any{"rotate": number(90)}, func(img image.Image) image.Image { return imaging.Rotate90(img) }},
		{"rotate180", map[string]any{"rotate": number(180)}, func(img image.Image) image.Image { return imaging.Rotate180(img) }},
		{"rotate270", map[string]any{"rotate": number(270)}, func(img image.Image) image.Image { return imaging.Rotate270(img) }},
		{"negative rotation", map[string]any{"rotate": number(-450)}, func(img image.Image) image.Image { return imaging.Rotate270(img) }},
		{"rotation modulo", map[string]any{"rotate": number(720)}, func(img image.Image) image.Image { return img }},
		{"arbitrary rotation", map[string]any{"rotate": number(30)}, func(img image.Image) image.Image { return imaging.Rotate(img, 30, color.NRGBA{}) }},
		{"small fractional rotation", map[string]any{"rotate": json.Number("0.1")}, func(img image.Image) image.Image { return imaging.Rotate(img, 0.1, color.NRGBA{}) }},
		{"scale", map[string]any{"scale": json.Number("0.5")}, func(img image.Image) image.Image { return imaging.Resize(img, 4, 3, imaging.Lanczos) }},
		{"tiny scale", map[string]any{"scale": json.Number("0.00001")}, func(img image.Image) image.Image { return imaging.Resize(img, 1, 1, imaging.Lanczos) }},
		{"grayscale", map[string]any{"grayscale": true}, func(img image.Image) image.Image { return imaging.Grayscale(img) }},
		{"invert", map[string]any{"invert": true}, func(img image.Image) image.Image { return imaging.Invert(img) }},
		{"brightness", map[string]any{"brightness": json.Number("0.2")}, func(img image.Image) image.Image { return imaging.AdjustBrightness(img, 20) }},
		{"contrast", map[string]any{"contrast": json.Number("-0.2")}, func(img image.Image) image.Image { return imaging.AdjustContrast(img, -20) }},
		{"saturation", map[string]any{"saturation": json.Number("0.2")}, func(img image.Image) image.Image { return imaging.AdjustSaturation(img, 20) }},
		{"gamma", map[string]any{"gamma": json.Number("1.2")}, func(img image.Image) image.Image { return imaging.AdjustGamma(img, 1.2) }},
		{"blur", map[string]any{"blur": json.Number("0.5")}, func(img image.Image) image.Image { return imaging.Blur(img, 0.5) }},
		{"sharpen", map[string]any{"sharpness": json.Number("0.5")}, func(img image.Image) image.Image { return imaging.Sharpen(img, 0.5) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, v, _, _ := NewFileFunctions()
			args := map[string]any{"path": path}
			for key, value := range tc.args {
				args[key] = value
			}
			result := invoke(t, v, context.Background(), args)
			requireImageEqual(t, decodeView(t, result), tc.want(input))
			requireEmptyRegistry(t, v.locks)
		})
	}
}

func TestViewTransformOrder(t *testing.T) {
	input := sampleImage(8, 6)
	_, v, _, _ := NewFileFunctions()
	path := pngFixture(t, input)
	args := map[string]any{
		"path":            path,
		"crop":            []any{json.Number("0.25"), json.Number("0"), json.Number("1"), json.Number("1")},
		"horizontal_flip": true, "vertical_flip": true, "rotate": number(90), "scale": json.Number("0.5"),
		"grayscale": true, "invert": true, "brightness": json.Number("0.1"), "contrast": json.Number("-0.2"),
		"saturation": json.Number("0.3"), "gamma": json.Number("1.2"), "blur": json.Number("0.5"), "sharpness": json.Number("0.5"),
	}
	var expected image.Image = imaging.Crop(input, image.Rect(2, 0, 8, 6))
	expected = imaging.FlipH(expected)
	expected = imaging.FlipV(expected)
	expected = imaging.Rotate90(expected)
	expected = imaging.Resize(expected, 3, 3, imaging.Lanczos)
	expected = imaging.Grayscale(expected)
	expected = imaging.Invert(expected)
	expected = imaging.AdjustBrightness(expected, 10)
	expected = imaging.AdjustContrast(expected, -20)
	expected = imaging.AdjustSaturation(expected, 30)
	expected = imaging.AdjustGamma(expected, 1.2)
	expected = imaging.Blur(expected, 0.5)
	expected = imaging.Sharpen(expected, 0.5)
	requireImageEqual(t, decodeView(t, invoke(t, v, context.Background(), args)), expected)
}

func TestViewNullDefaultsAndTransparency(t *testing.T) {
	input := sampleImage(4, 3)
	input.SetNRGBA(1, 1, color.NRGBA{10, 20, 30, 100})
	input.SetNRGBA(2, 1, color.NRGBA{})
	_, v, _, _ := NewFileFunctions()
	args := map[string]any{"path": pngFixture(t, input)}
	for key := range v.Schema()["parameters"].(map[string]any)["properties"].(map[string]any) {
		if key != "path" {
			args[key] = nil
		}
	}
	requireImageEqual(t, decodeView(t, invoke(t, v, context.Background(), args)), input)
}

func TestViewFinalEdgeLimit(t *testing.T) {
	for _, size := range [][2]int{{1920, 2}, {1921, 4}, {4, 1921}, {5000, 10}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			_, v, _, _ := NewFileFunctions()
			input := sampleImage(size[0], size[1])
			img := decodeView(t, invoke(t, v, context.Background(), map[string]any{"path": pngFixture(t, input)}))
			w, h := size[0], size[1]
			if edge := max(w, h); edge > 1920 {
				w, h = max(1, w*1920/edge), max(1, h*1920/edge)
			}
			if img.Bounds().Dx() != w || img.Bounds().Dy() != h {
				t.Fatalf("output size = %v, want %dx%d", img.Bounds(), w, h)
			}
		})
	}
}

func TestViewFormatsAndJPEGOrientation(t *testing.T) {
	input := sampleImage(4, 3)
	var pngData, jpegData bytes.Buffer
	if err := png.Encode(&pngData, input); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpegData, input, nil); err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAYAAAAdQqKIUr/+BiOh/AAA=")
	if err != nil {
		t.Fatal(err)
	}
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	oriented := append([]byte{}, jpegData.Bytes()[:2]...)
	oriented = append(oriented, 0xff, 0xe1, byte((len(exif)+2)>>8), byte(len(exif)+2))
	oriented = append(oriented, exif...)
	oriented = append(oriented, jpegData.Bytes()[2:]...)
	for _, tc := range []struct {
		name string
		data []byte
		w, h int
	}{
		{"PNG", pngData.Bytes(), 4, 3},
		{"JPEG", jpegData.Bytes(), 4, 3},
		{"WebP", webp, 2, 3},
		{"JPEG EXIF orientation", oriented, 3, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, v, _, _ := NewFileFunctions()
			path := filepath.Join(t.TempDir(), "no-extension")
			writeFixture(t, path, tc.data)
			img := decodeView(t, invoke(t, v, context.Background(), map[string]any{"path": path}))
			if img.Bounds().Dx() != tc.w || img.Bounds().Dy() != tc.h {
				t.Fatalf("decoded size = %v, want %dx%d", img.Bounds(), tc.w, tc.h)
			}
		})
	}
}

func TestViewValidation(t *testing.T) {
	_, v, _, _ := NewFileFunctions()
	for _, key := range []string{"horizontal_flip", "vertical_flip", "grayscale", "invert"} {
		requireError(t, invoke(t, v, context.Background(), map[string]any{"path": "missing", key: "true"}), key+" is not a bool")
	}
	for _, key := range []string{"rotate", "scale", "brightness", "contrast", "saturation", "gamma", "blur", "sharpness"} {
		for _, value := range []any{"1", true, json.Number("NaN"), json.Number("1e9999")} {
			requireError(t, invoke(t, v, context.Background(), map[string]any{"path": "missing", key: value}), "invalid arguments")
		}
	}
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"scale", number(0)}, {"scale", number(-1)}, {"gamma", number(0)}, {"gamma", number(-1)},
		{"brightness", json.Number("-1.01")}, {"brightness", json.Number("1.01")},
		{"contrast", json.Number("-1.01")}, {"contrast", json.Number("1.01")},
		{"saturation", json.Number("-1.01")}, {"saturation", json.Number("1.01")},
		{"blur", json.Number("-0.1")}, {"blur", json.Number("10.1")},
		{"sharpness", json.Number("-0.1")}, {"sharpness", json.Number("10.1")},
		{"crop", "wrong"}, {"crop", []float64{0, 0, 1, 1}}, {"crop", []any{number(0)}},
		{"crop", []any{0.0, number(0), number(1), number(1)}},
		{"crop", []any{number(-1), number(0), number(1), number(1)}},
		{"crop", []any{number(0), number(0), json.Number("NaN"), number(1)}},
		{"crop", []any{number(0), number(0), json.Number("1e9999"), number(1)}},
		{"crop", []any{number(0), number(0), number(2), number(1)}},
		{"crop", []any{number(1), number(0), number(0), number(1)}},
		{"crop", []any{number(0), number(1), number(1), number(1)}},
	} {
		requireError(t, invoke(t, v, context.Background(), map[string]any{"path": "missing", tc.key: tc.value}), "invalid arguments")
	}
}

func TestViewRejectsCorruptImages(t *testing.T) {
	var valid, unsupported bytes.Buffer
	if err := png.Encode(&valid, sampleImage(2, 2)); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&unsupported, sampleImage(2, 2), nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		err  string
	}{
		{"empty", nil, "unsupported format"},
		{"text", []byte("not an image"), "unsupported format"},
		{"GIF", unsupported.Bytes(), "unsupported format"},
		{"bad PNG header", []byte("\x89PNG\r\n\x1a\ninvalid"), "decode image header"},
		{"bad JPEG header", []byte("\xff\xd8\xffinvalid"), "decode image header"},
		{"missing PNG pixels", valid.Bytes()[:33], "decode image:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, v, _, _ := NewFileFunctions()
			path := filepath.Join(t.TempDir(), "input")
			writeFixture(t, path, tc.data)
			requireError(t, invoke(t, v, context.Background(), map[string]any{"path": path}), tc.err)
			requireEmptyRegistry(t, v.locks)
		})
	}
}

func TestViewResourceLimits(t *testing.T) {
	for _, size := range [][2]int{{16385, 1}, {1, 16385}, {6000, 6000}} {
		_, v, _, _ := NewFileFunctions()
		var buf bytes.Buffer
		if err := png.Encode(&buf, sampleImage(1, 1)); err != nil {
			t.Fatal(err)
		}
		data := buf.Bytes()
		binary.BigEndian.PutUint32(data[16:20], uint32(size[0]))
		binary.BigEndian.PutUint32(data[20:24], uint32(size[1]))
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		path := filepath.Join(t.TempDir(), "bomb.png")
		writeFixture(t, path, data)
		requireError(t, invoke(t, v, context.Background(), map[string]any{"path": path}), "image exceeds size limits")
	}
	_, v, _, _ := NewFileFunctions()
	path := filepath.Join(t.TempDir(), "large")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((128 << 20) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	requireError(t, invoke(t, v, context.Background(), map[string]any{"path": path}), "file exceeds 128 MiB")
	path = pngFixture(t, sampleImage(8, 6))
	for _, scale := range []string{"1000", "3000", "1e308"} {
		requireError(t, invoke(t, v, context.Background(), map[string]any{"path": path, "scale": json.Number(scale)}), "scale exceeds size limits")
	}
	path = pngFixture(t, sampleImage(16384, 1))
	requireError(t, invoke(t, v, context.Background(), map[string]any{"path": path, "rotate": number(45)}), "rotation exceeds size limits")
}
