package tool

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

type View struct{}

func (*View) Schema() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "view",
		"description": "View a PNG, JPEG, or WebP image. Optional transformations are applied in the order listed below.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file.",
				},
				"crop": map[string]any{
					"type":     []string{"array", "null"},
					"minItems": 4,
					"maxItems": 4,
					"items": map[string]any{
						"type":    "number",
						"minimum": 0,
						"maximum": 1,
					},
					"description": "1. Crop region using relative coordinates [left, top, right, bottom] with 0 <= left < right <= 1 and 0 <= top < bottom <= 1. Defaults to no crop.",
				},
				"horizontal_flip": map[string]any{
					"type":        []string{"boolean", "null"},
					"description": "2. Flip horizontally. Defaults to false.",
				},
				"vertical_flip": map[string]any{
					"type":        []string{"boolean", "null"},
					"description": "3. Flip vertically. Defaults to false.",
				},
				"rotate": map[string]any{
					"type":        []string{"number", "null"},
					"description": "4. Rotate counterclockwise in degrees. Defaults to 0.",
				},
				"scale": map[string]any{
					"type":             []string{"number", "null"},
					"exclusiveMinimum": 0,
					"description":      "5. Positive scale factor. Defaults to 1.",
				},
				"grayscale": map[string]any{
					"type":        []string{"boolean", "null"},
					"description": "6. Convert to grayscale. Defaults to false.",
				},
				"invert": map[string]any{
					"type":        []string{"boolean", "null"},
					"description": "7. Invert colors. Defaults to false.",
				},
				"brightness": map[string]any{
					"type":        []string{"number", "null"},
					"minimum":     -1,
					"maximum":     1,
					"description": "8. Adjust brightness, from -1 to 1. Defaults to 0 (unchanged).",
				},
				"contrast": map[string]any{
					"type":        []string{"number", "null"},
					"minimum":     -1,
					"maximum":     1,
					"description": "9. Adjust contrast after brightness, from -1 to 1. Defaults to 0 (unchanged).",
				},
				"saturation": map[string]any{
					"type":        []string{"number", "null"},
					"minimum":     -1,
					"maximum":     1,
					"description": "10. Adjust saturation, from -1 to 1. Defaults to 0 (unchanged).",
				},
				"gamma": map[string]any{
					"type":             []string{"number", "null"},
					"exclusiveMinimum": 0,
					"description":      "11. Positive gamma correction. Defaults to 1 (unchanged).",
				},
				"blur": map[string]any{
					"type":        []string{"number", "null"},
					"minimum":     0,
					"maximum":     10,
					"description": "12. Gaussian blur sigma, from 0 to 10. Defaults to 0 (disabled).",
				},
				"sharpness": map[string]any{
					"type":        []string{"number", "null"},
					"minimum":     0,
					"maximum":     10,
					"description": "13. Gaussian sharpening sigma, from 0 to 10. Defaults to 0 (disabled).",
				},
			},
			"required":             []string{"path", "crop", "horizontal_flip", "vertical_flip", "rotate", "scale", "grayscale", "invert", "brightness", "contrast", "saturation", "gamma", "blur", "sharpness"},
			"additionalProperties": false,
		},
		"strict": true,
	}
}

func (*View) Call(in string) string {
	out := "<view"
	if !utf8.ValidString(in) {
		return out + " error=\"invalid arguments: invalid utf-8\"/>"
	}
	dec := json.NewDecoder(strings.NewReader(in))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return out + fmt.Sprintf(" error=%q/>", "invalid arguments: "+err.Error())
	}
	v, ok := args["path"]
	if !ok {
		return out + " error=\"invalid arguments: missing path\"/>"
	}
	path, ok := v.(string)
	if !ok {
		return out + " error=\"invalid arguments: path is not a string\"/>"
	}
	if path == "" {
		return out + " error=\"invalid arguments: path is empty\"/>"
	}
	out += fmt.Sprintf(" path=%q", path)
	horizontalFlip := false
	v, ok = args["horizontal_flip"]
	if ok && v != nil {
		horizontalFlip, ok = v.(bool)
		if !ok {
			return out + " error=\"invalid arguments: horizontal_flip is not a bool\"/>"
		}
	}
	verticalFlip := false
	v, ok = args["vertical_flip"]
	if ok && v != nil {
		verticalFlip, ok = v.(bool)
		if !ok {
			return out + " error=\"invalid arguments: vertical_flip is not a bool\"/>"
		}
	}
	grayscale := false
	v, ok = args["grayscale"]
	if ok && v != nil {
		grayscale, ok = v.(bool)
		if !ok {
			return out + " error=\"invalid arguments: grayscale is not a bool\"/>"
		}
	}
	invert := false
	v, ok = args["invert"]
	if ok && v != nil {
		invert, ok = v.(bool)
		if !ok {
			return out + " error=\"invalid arguments: invert is not a bool\"/>"
		}
	}
	rotate := 0.0
	v, ok = args["rotate"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: rotate is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: rotate must be finite\"/>"
		}
		rotate = f
	}
	scale := 1.0
	v, ok = args["scale"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: scale is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: scale must be finite\"/>"
		}
		if f <= 0 {
			return out + " error=\"invalid arguments: scale must be greater than 0\"/>"
		}
		scale = f
	}
	brightness := 0.0
	v, ok = args["brightness"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: brightness is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: brightness must be finite\"/>"
		}
		if f < -1 || f > 1 {
			return out + " error=\"invalid arguments: brightness must be between -1 and 1\"/>"
		}
		brightness = f
	}
	contrast := 0.0
	v, ok = args["contrast"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: contrast is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: contrast must be finite\"/>"
		}
		if f < -1 || f > 1 {
			return out + " error=\"invalid arguments: contrast must be between -1 and 1\"/>"
		}
		contrast = f
	}
	saturation := 0.0
	v, ok = args["saturation"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: saturation is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: saturation must be finite\"/>"
		}
		if f < -1 || f > 1 {
			return out + " error=\"invalid arguments: saturation must be between -1 and 1\"/>"
		}
		saturation = f
	}
	gamma := 1.0
	v, ok = args["gamma"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: gamma is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: gamma must be finite\"/>"
		}
		if f <= 0 {
			return out + " error=\"invalid arguments: gamma must be greater than 0\"/>"
		}
		gamma = f
	}
	blur := 0.0
	v, ok = args["blur"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: blur is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: blur must be finite\"/>"
		}
		if f < 0 || f > 10 {
			return out + " error=\"invalid arguments: blur must be between 0 and 10\"/>"
		}
		blur = f
	}
	sharpness := 0.0
	v, ok = args["sharpness"]
	if ok && v != nil {
		n, ok := v.(json.Number)
		if !ok {
			return out + " error=\"invalid arguments: sharpness is not a number\"/>"
		}
		f, err := n.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return out + " error=\"invalid arguments: sharpness must be finite\"/>"
		}
		if f < 0 || f > 10 {
			return out + " error=\"invalid arguments: sharpness must be between 0 and 10\"/>"
		}
		sharpness = f
	}
	var crop []float64
	if v, ok := args["crop"]; ok && v != nil {
		values, ok := v.([]any)
		if !ok {
			return out + " error=\"invalid arguments: crop is not an array\"/>"
		}
		if len(values) != 0 && len(values) != 4 {
			return out + " error=\"invalid arguments: crop must be empty or contain [left, top, right, bottom]\"/>"
		}
		for _, v := range values {
			n, ok := v.(json.Number)
			if !ok {
				return out + " error=\"invalid arguments: crop coordinates must be numbers\"/>"
			}
			f, err := n.Float64()
			if err != nil || !(0 <= f && f <= 1) {
				return out + " error=\"invalid arguments: crop coordinates must be between 0 and 1\"/>"
			}
			crop = append(crop, f)
		}
		if len(crop) != 0 && (crop[0] >= crop[2] || crop[1] >= crop[3]) {
			return out + " error=\"invalid arguments: crop must satisfy left < right and top < bottom\"/>"
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("stat %s: %v", path, err))
	}
	if !info.Mode().IsRegular() {
		return out + fmt.Sprintf(" error=%q/>", path+" is not a regular file")
	}
	if info.Size() > 128<<20 {
		return out + " error=\"file exceeds 128 MiB\"/>"
	}
	file, err := os.Open(path)
	if err != nil {
		return out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("open %s: %v", path, err))
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("stat %s: %v", path, err))
	}
	if !info.Mode().IsRegular() {
		return out + fmt.Sprintf(" error=%q/>", path+" is not a regular file")
	}
	if info.Size() > 128<<20 {
		return out + " error=\"file exceeds 128 MiB\"/>"
	}
	var header [512]byte
	n, err := file.ReadAt(header[:], 0)
	if err != nil && err != io.EOF {
		return out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("read %s: %v", path, err))
	}
	var img image.Image
	switch http.DetectContentType(header[:n]) {
	case "image/png", "image/jpeg", "image/webp":
		config, _, err := image.DecodeConfig(file)
		if err != nil {
			return out + fmt.Sprintf(" error=%q/>", "decode image header: "+err.Error())
		}
		w, h := config.Width, config.Height
		if w <= 0 || h <= 0 || w > 16384 || h > 16384 || int64(w)*int64(h) > 32_000_000 {
			return out + " error=\"image exceeds size limits\"/>"
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return out + fmt.Sprintf(" error=%q/>", fmt.Sprintf("seek %s: %v", path, err))
		}
		img, err = imaging.Decode(file, imaging.AutoOrientation(true))
		if err != nil {
			return out + fmt.Sprintf(" error=%q/>", "decode image: "+err.Error())
		}
	default:
		return out + " error=\"unsupported format; expected PNG, JPEG, or WebP\"/>"
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 || int64(w)*int64(h) > 32_000_000 {
		return out + " error=\"image exceeds size limits\"/>"
	}
	if len(crop) != 0 {
		img = imaging.Crop(img, image.Rect(
			int(math.Floor(crop[0]*float64(w))),
			int(math.Floor(crop[1]*float64(h))),
			int(math.Ceil(crop[2]*float64(w))),
			int(math.Ceil(crop[3]*float64(h))),
		).Add(img.Bounds().Min))
		w, h = img.Bounds().Dx(), img.Bounds().Dy()
	}
	if horizontalFlip {
		img = imaging.FlipH(img)
	}
	if verticalFlip {
		img = imaging.FlipV(img)
	}
	angle := math.Mod(rotate, 360)
	if angle < 0 {
		angle += 360
	}
	if angle != 0 {
		if angle == 90 || angle == 270 {
			w, h = h, w
		} else if angle != 180 {
			sin, cos := math.Sincos(angle * math.Pi / 180)
			rw := float64(w-1)*math.Abs(cos) + float64(h-1)*math.Abs(sin) + 1
			rh := float64(w-1)*math.Abs(sin) + float64(h-1)*math.Abs(cos) + 1
			if rw-math.Floor(rw) > 0.1 {
				rw++
			}
			if rh-math.Floor(rh) > 0.1 {
				rh++
			}
			w, h = int(rw), int(rh)
		}
		if w > 16384 || h > 16384 || int64(w)*int64(h) > 32_000_000 {
			return out + " error=\"rotation exceeds size limits\"/>"
		}
		img = imaging.Rotate(img, angle, color.NRGBA{})
	}
	sw := math.Max(1, math.Round(scale*float64(w)))
	sh := math.Max(1, math.Round(scale*float64(h)))
	if sw > 16384 || sh > 16384 || sw*sh > 32_000_000 {
		return out + " error=\"scale exceeds size limits\"/>"
	}
	tw, th := int(sw), int(sh)
	if edge := max(tw, th); edge > 1920 {
		tw = max(1, tw*1920/edge)
		th = max(1, th*1920/edge)
	}
	if tw != w || th != h {
		img = imaging.Resize(img, tw, th, imaging.Lanczos)
	}
	if grayscale {
		img = imaging.Grayscale(img)
	}
	if invert {
		img = imaging.Invert(img)
	}
	if brightness != 0 {
		img = imaging.AdjustBrightness(img, brightness*100)
	}
	if contrast != 0 {
		img = imaging.AdjustContrast(img, contrast*100)
	}
	if saturation != 0 {
		img = imaging.AdjustSaturation(img, saturation*100)
	}
	if gamma != 1 {
		img = imaging.AdjustGamma(img, gamma)
	}
	if blur != 0 {
		img = imaging.Blur(img, blur)
	}
	if sharpness != 0 {
		img = imaging.Sharpen(img, sharpness)
	}
	var body strings.Builder
	body.WriteString(out + " error=\"\">\ndata:image/png;base64,")
	enc := base64.NewEncoder(base64.StdEncoding, &body)
	if err := png.Encode(enc, img); err != nil {
		return out + fmt.Sprintf(" error=%q/>", "encode PNG: "+err.Error())
	}
	if err := enc.Close(); err != nil {
		return out + fmt.Sprintf(" error=%q/>", "encode base64: "+err.Error())
	}
	body.WriteString("\n</view>")
	return body.String()
}
