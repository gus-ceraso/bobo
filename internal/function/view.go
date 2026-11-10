package function

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"

	"ceraso.dev/bobo/internal/provider"
	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

// View reads PNG, JPEG, or WebP images and returns a PNG preview with optional
// transformations. Previews fit within 1920 pixels on the longest edge. Inputs
// are limited to 128 MiB, 16384 pixels per edge, and 32 million pixels; rotation
// and scaling must also stay within the pixel limits. Create View with
// NewFileFunctions.
type View struct {
	locks *fileLocks
}

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

func (v *View) Call(ctx context.Context, args map[string]any) []provider.Part {
	out := newToolResult("view")
	body, err := v.view(ctx, args, &out)
	if err != nil {
		return out.status(err)
	}
	return out.image(body)
}

func (v *View) view(ctx context.Context, args map[string]any, out *toolResult) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := nonemptyStringArgument(args, "path")
	if err != nil {
		return nil, err
	}
	out.attribute("path", path)
	horizontalFlip, err := boolArgument(args, "horizontal_flip")
	if err != nil {
		return nil, err
	}
	verticalFlip, err := boolArgument(args, "vertical_flip")
	if err != nil {
		return nil, err
	}
	grayscale, err := boolArgument(args, "grayscale")
	if err != nil {
		return nil, err
	}
	invert, err := boolArgument(args, "invert")
	if err != nil {
		return nil, err
	}
	rotate, err := numberArgument(args, "rotate", 0)
	if err != nil {
		return nil, err
	}
	scale, err := positiveNumberArgument(args, "scale", 1)
	if err != nil {
		return nil, err
	}
	brightness, err := boundedNumberArgument(args, "brightness", 0, -1, 1)
	if err != nil {
		return nil, err
	}
	contrast, err := boundedNumberArgument(args, "contrast", 0, -1, 1)
	if err != nil {
		return nil, err
	}
	saturation, err := boundedNumberArgument(args, "saturation", 0, -1, 1)
	if err != nil {
		return nil, err
	}
	gamma, err := positiveNumberArgument(args, "gamma", 1)
	if err != nil {
		return nil, err
	}
	blur, err := boundedNumberArgument(args, "blur", 0, 0, 10)
	if err != nil {
		return nil, err
	}
	sharpness, err := boundedNumberArgument(args, "sharpness", 0, 0, 10)
	if err != nil {
		return nil, err
	}
	var crop []float64
	if v := args["crop"]; v != nil {
		values, ok := v.([]any)
		if !ok {
			return nil, errors.New("invalid arguments: crop is not an array")
		}
		if len(values) != 0 && len(values) != 4 {
			return nil, errors.New("invalid arguments: crop must be empty or contain [left, top, right, bottom]")
		}
		for _, v := range values {
			n, ok := v.(json.Number)
			if !ok {
				return nil, errors.New("invalid arguments: crop coordinates must be numbers")
			}
			f, err := n.Float64()
			if err != nil || !(0 <= f && f <= 1) {
				return nil, errors.New("invalid arguments: crop coordinates must be between 0 and 1")
			}
			crop = append(crop, f)
		}
		if len(crop) != 0 && (crop[0] >= crop[2] || crop[1] >= crop[3]) {
			return nil, errors.New("invalid arguments: crop must satisfy left < right and top < bottom")
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, unlock, err := v.locks.acquire(ctx, path)
	if err != nil {
		return nil, err
	}
	defer unlock()

	file, info, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > 128<<20 {
		return nil, errors.New("file exceeds 128 MiB")
	}
	reader := io.NewSectionReader(file, 0, 128<<20)
	var header [512]byte
	n, err := reader.ReadAt(header[:], 0)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read %s: %v", path, err)
	}
	switch http.DetectContentType(header[:n]) {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return nil, errors.New("unsupported format; expected PNG, JPEG, or WebP")
	}
	config, _, err := image.DecodeConfig(reader)
	if err != nil {
		return nil, errors.New("decode image header: " + err.Error())
	}
	w, h := config.Width, config.Height
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 || int64(w)*int64(h) > 32_000_000 {
		return nil, errors.New("image exceeds size limits")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek %s: %v", path, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	img, err := imaging.Decode(reader, imaging.AutoOrientation(true))
	if err != nil {
		return nil, errors.New("decode image: " + err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w, h = img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 || int64(w)*int64(h) > 32_000_000 {
		return nil, errors.New("image exceeds size limits")
	}
	if len(crop) != 0 {
		img = imaging.Crop(img, image.Rect(
			int(math.Floor(crop[0]*float64(w))),
			int(math.Floor(crop[1]*float64(h))),
			int(math.Ceil(crop[2]*float64(w))),
			int(math.Ceil(crop[3]*float64(h))),
		).Add(img.Bounds().Min))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		w, h = img.Bounds().Dx(), img.Bounds().Dy()
	}
	if horizontalFlip {
		img = imaging.FlipH(img)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if verticalFlip {
		img = imaging.FlipV(img)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
			return nil, errors.New("rotation exceeds size limits")
		}
		img = imaging.Rotate(img, angle, color.NRGBA{})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	sw := math.Max(1, math.Round(scale*float64(w)))
	sh := math.Max(1, math.Round(scale*float64(h)))
	if sw > 16384 || sh > 16384 || sw*sh > 32_000_000 {
		return nil, errors.New("scale exceeds size limits")
	}
	tw, th := int(sw), int(sh)
	if edge := max(tw, th); edge > 1920 {
		tw = max(1, tw*1920/edge)
		th = max(1, th*1920/edge)
	}
	if tw != w || th != h {
		img = imaging.Resize(img, tw, th, imaging.Lanczos)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if grayscale {
		img = imaging.Grayscale(img)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if invert {
		img = imaging.Invert(img)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if brightness != 0 {
		img = imaging.AdjustBrightness(img, brightness*100)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if contrast != 0 {
		img = imaging.AdjustContrast(img, contrast*100)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if saturation != 0 {
		img = imaging.AdjustSaturation(img, saturation*100)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if gamma != 1 {
		img = imaging.AdjustGamma(img, gamma)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if blur != 0 {
		img = imaging.Blur(img, blur)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if sharpness != 0 {
		img = imaging.Sharpen(img, sharpness)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	var body bytes.Buffer
	if err := png.Encode(&body, img); err != nil {
		return nil, errors.New("encode PNG: " + err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}
