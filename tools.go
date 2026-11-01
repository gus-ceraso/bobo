package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/disintegration/imaging"
	"github.com/gen2brain/go-fitz"
	_ "golang.org/x/image/webp"
)

func Read(path string, lineNumbers bool) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("expected a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("expected a regular file")
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", errors.New("file content must be valid UTF-8")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if !lineNumbers {
		return text, nil
	}
	lines := splitLines(text)
	for i := 0; i < len(lines); i++ {
		lines[i] = fmt.Sprintf("%d:%s", i+1, lines[i])
	}
	return strings.Join(lines, "\n"), nil
}

func View(path string, page int, crop []float64, horizontal_flip, vertical_flip bool, rotate, scale float64, grayscale, invert bool, brightness, contrast, saturation, gamma, blur, sharpness float64) (encImage string, numPages int, err error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var img image.Image
	switch http.DetectContentType(content) {
	case "application/pdf":
		doc, err := fitz.NewFromMemory(content)
		if err != nil {
			err = fmt.Errorf("load PDF: %w", err)
			return "", 0, err
		}
		defer doc.Close()
		numPages = doc.NumPage()
		if page <= 0 || page > numPages {
			err = fmt.Errorf("page must be a 1-based page number; PDF contains %d pages", numPages)
			return "", numPages, err
		}
		img, err = doc.Image(page - 1)
		if err != nil {
			err = fmt.Errorf("render PDF page: %w", err)
			return "", numPages, err
		}
	case "image/png", "image/jpeg", "image/webp":
		img, err = imaging.Decode(bytes.NewReader(content), imaging.AutoOrientation(true))
		if err != nil {
			err = fmt.Errorf("decode image: %w", err)
			return "", 0, err
		}
	default:
		err = errors.New("unsupported format; expected PNG, JPEG, WEBP, or PDF")
		return
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 || h <= 0 {
		err = fmt.Errorf("image dimensions must be positive; decoded size is %d by %d", w, h)
		return
	}
	if len(crop) != 0 {
		if len(crop) != 4 {
			err = errors.New("crop must be empty or contain exactly four values: [left, top, right, bottom]")
			return
		}
		if !(0 <= crop[0] && crop[0] < crop[2] && crop[2] <= 1 &&
			0 <= crop[1] && crop[1] < crop[3] && crop[3] <= 1) {
			err = errors.New("crop coordinates must satisfy 0 <= left < right <= 1 and 0 <= top < bottom <= 1")
			return
		}
		img = imaging.Crop(img, image.Rect(
			int(math.Floor(crop[0]*float64(w))),
			int(math.Floor(crop[1]*float64(h))),
			int(math.Ceil(crop[2]*float64(w))),
			int(math.Ceil(crop[3]*float64(h))),
		).Add(img.Bounds().Min))
		w, h = img.Bounds().Dx(), img.Bounds().Dy()
	}
	if horizontal_flip {
		img = imaging.FlipH(img)
	}
	if vertical_flip {
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
		img = imaging.Rotate(img, angle, color.NRGBA{})
	}
	if scale != 1 {
		if !(scale > 0) || math.IsInf(scale, 1) {
			err = errors.New("scale must be finite and greater than 0")
			return
		}
		sw := math.Max(1, math.Round(scale*float64(w)))
		sh := math.Max(1, math.Round(scale*float64(h)))
		maxInt := float64(int(^uint(0) >> 1))
		if sw >= maxInt || sh >= maxInt {
			err = errors.New("scaled dimensions exceed the supported limit; reduce scale")
			return
		}
		w, h = int(sw), int(sh)
		img = imaging.Resize(img, w, h, imaging.Lanczos)
	}
	if grayscale {
		img = imaging.Grayscale(img)
	}
	if invert {
		img = imaging.Invert(img)
	}
	if brightness != 0 {
		if !(-1 <= brightness && brightness <= 1) {
			err = errors.New("brightness must be between -1 and 1 (inclusive)")
			return
		}
		img = imaging.AdjustBrightness(img, brightness*100)
	}
	if contrast != 0 {
		if !(-1 <= contrast && contrast <= 1) {
			err = errors.New("contrast must be between -1 and 1 (inclusive)")
			return
		}
		img = imaging.AdjustContrast(img, contrast*100)
	}
	if saturation != 0 {
		if !(-1 <= saturation && saturation <= 1) {
			err = errors.New("saturation must be between -1 and 1 (inclusive)")
			return
		}
		img = imaging.AdjustSaturation(img, saturation*100)
	}
	if gamma != 1 {
		if !(gamma > 0) || math.IsInf(gamma, 1) {
			err = errors.New("gamma must be finite and greater than 0")
			return
		}
		img = imaging.AdjustGamma(img, gamma)
	}
	if blur != 0 {
		if !(blur > 0) || math.IsInf(blur, 1) {
			err = errors.New("blur must be finite and nonnegative")
			return
		}
		img = imaging.Blur(img, blur)
	}
	if sharpness != 0 {
		if !(sharpness > 0) || math.IsInf(sharpness, 1) {
			err = errors.New("sharpness must be finite and nonnegative")
			return
		}
		img = imaging.Sharpen(img, sharpness)
	}
	if w > 1920 || h > 1920 {
		img = imaging.Fit(img, 1920, 1920, imaging.Lanczos)
	}
	var buf bytes.Buffer
	err = png.Encode(&buf, img)
	if err != nil {
		err = fmt.Errorf("encode PNG: %w", err)
		return
	}
	encImage = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	return
}

func Search(path, pathPattern, pattern string, hiddenPaths bool) (string, error) {
	pre, err := regexp.Compile(pathPattern)
	if err != nil {
		return "", fmt.Errorf("invalid pathPattern regular expression: %w", err)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern regular expression: %w", err)
	}
	if !hiddenPaths && hasHiddenComponent(path) {
		return "", errors.New("hidden path components require hiddenPaths=true")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	var matches []string
	if !info.IsDir() {
		text, err := Read(path, false)
		if err != nil {
			return "", err
		}
		lines := splitLines(text)
		for i := 0; i < len(lines); i++ {
			if re.MatchString(lines[i]) {
				matches = append(matches, fmt.Sprintf("%d:%s", i+1, lines[i]))
			}
		}
	} else {
		err = fs.WalkDir(os.DirFS(path), ".", func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				if d != nil && (errors.Is(e, fs.ErrNotExist) || errors.Is(e, fs.ErrPermission)) {
					return nil
				}
				return e
			}
			if d.IsDir() {
				if !hiddenPaths && hasHiddenComponent(p) {
					return fs.SkipDir
				}
				return nil
			}
			if !hiddenPaths && hasHiddenComponent(p) {
				return nil
			}
			if !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			if !pre.MatchString(p) {
				return nil
			}
			text, err := Read(filepath.Join(path, filepath.FromSlash(p)), false)
			if err != nil {
				return nil
			}
			lines := splitLines(text)
			for i := 0; i < len(lines); i++ {
				if re.MatchString(lines[i]) {
					matches = append(matches, fmt.Sprintf("%s:%d:%s", p, i+1, lines[i]))
				}
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return strings.Join(matches, "\n"), nil
}

func List(path, pattern string, hiddenPaths bool) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern regular expression: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("expected a directory")
	}
	if !hiddenPaths && hasHiddenComponent(path) {
		return "", errors.New("hidden path components require hiddenPaths=true")
	}
	var lines []string
	err = fs.WalkDir(os.DirFS(path), ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			if d != nil && (errors.Is(e, fs.ErrNotExist) || errors.Is(e, fs.ErrPermission)) {
				return nil
			}
			return e
		}
		if d.IsDir() {
			if !hiddenPaths && hasHiddenComponent(p) {
				return fs.SkipDir
			}
			return nil
		}
		if !hiddenPaths && hasHiddenComponent(p) {
			return nil
		}
		if re.MatchString(p) {
			lines = append(lines, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n"), nil
}

func Write(path, text string) error {
	if !utf8.ValidString(text) {
		return errors.New("text must be valid UTF-8")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directories: %w", err)
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func Edit(path, oldText, newText string) error {
	old := []byte(strings.ReplaceAll(oldText, "\r\n", "\n"))
	old = bytes.ReplaceAll(old, []byte("\r"), []byte("\n"))
	if !utf8.Valid(old) {
		return errors.New("oldText must be valid UTF-8")
	}
	new := []byte(strings.ReplaceAll(newText, "\r\n", "\n"))
	new = bytes.ReplaceAll(new, []byte("\r"), []byte("\n"))
	if !utf8.Valid(new) {
		return errors.New("newText must be valid UTF-8")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("expected a regular file")
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	if !utf8.Valid(raw) {
		return errors.New("file content must be valid UTF-8")
	}
	useCRLF := bytes.Contains(raw, []byte("\r\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))
	if i := bytes.Index(raw, old); i == -1 || i != bytes.LastIndex(raw, old) {
		return errors.New("oldText must occur exactly once in the file")
	}
	raw = bytes.Replace(raw, old, new, 1)
	if useCRLF {
		raw = bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
	}
	if _, err := file.WriteAt(raw, 0); err != nil {
		return err
	}
	if err := file.Truncate(int64(len(raw))); err != nil {
		return err
	}
	return nil
}

func Copy(oldPath, newPath string) error {
	var err error
	oldPath, err = filepath.Abs(oldPath)
	if err != nil {
		return err
	}
	newPath, err = filepath.Abs(newPath)
	if err != nil {
		return err
	}
	if rel, _ := filepath.Rel(oldPath, newPath); filepath.IsLocal(rel) {
		return errors.New("destination must not be the source or a descendant of it")
	}
	info, err := os.Lstat(oldPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		source, err := filepath.EvalSymlinks(oldPath)
		if err != nil {
			return err
		}
		ancestor, suffix := newPath, ""
		for {
			destination, err := filepath.EvalSymlinks(ancestor)
			if err == nil {
				rel, err := filepath.Rel(source, filepath.Join(destination, suffix))
				if err != nil {
					return err
				}
				if filepath.IsLocal(rel) {
					return errors.New("destination must not be the source or a descendant of it")
				}
				break
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if _, statErr := os.Lstat(ancestor); statErr == nil {
				return err
			} else if !errors.Is(statErr, fs.ErrNotExist) {
				return statErr
			}
			parent := filepath.Dir(ancestor)
			if parent == ancestor {
				return err
			}
			suffix = filepath.Join(filepath.Base(ancestor), suffix)
			ancestor = parent
		}
	}
	var directories []struct {
		path string
		mode fs.FileMode
	}
	err = filepath.WalkDir(oldPath, func(old string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(oldPath, old)
		new := filepath.Join(newPath, rel)
		if rel == "." {
			if err := os.MkdirAll(filepath.Dir(new), 0o755); err != nil {
				return err
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsRegular():
			oldFile, err := os.Open(old)
			if err != nil {
				return err
			}
			defer oldFile.Close()
			oldFileInfo, err := oldFile.Stat()
			if err != nil {
				return err
			}
			newFile, err := os.OpenFile(new, os.O_WRONLY|os.O_CREATE, 0)
			if err != nil {
				return err
			}
			defer newFile.Close()
			newFileInfo, err := newFile.Stat()
			if err != nil {
				return err
			}
			if os.SameFile(oldFileInfo, newFileInfo) {
				return errors.New("source and destination are the same file")
			}
			if err := newFile.Truncate(0); err != nil {
				return fmt.Errorf("truncate destination: %w", err)
			}
			if err := newFile.Chmod(oldFileInfo.Mode().Perm()); err != nil {
				return fmt.Errorf("set destination permissions: %w", err)
			}
			if _, err := io.Copy(newFile, oldFile); err != nil {
				return err
			}
			return newFile.Sync()
		case mode.IsDir():
			if info, err := os.Stat(new); err == nil {
				if !info.IsDir() {
					return errors.New("destination must be a directory when the source is a directory")
				}
				return nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := os.Mkdir(new, mode.Perm()|0o700); err != nil {
				return err
			}
			directories = append(directories, struct {
				path string
				mode fs.FileMode
			}{new, mode.Perm()})
			return os.Chmod(new, mode.Perm()|0o700)
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(old)
			if err != nil {
				return err
			}
			if err := os.Remove(new); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return os.Symlink(target, new)
		default:
			return fmt.Errorf("unsupported file type %s; expected a regular file, directory, or symbolic link", mode.Type())
		}
	})
	for i := len(directories) - 1; i >= 0; i-- {
		if chmodErr := os.Chmod(directories[i].path, directories[i].mode); chmodErr != nil {
			err = errors.Join(err, fmt.Errorf("restore directory permissions: %w", chmodErr))
		}
	}
	return err
}

func Move(oldPath, newPath string) error {
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return fmt.Errorf("create destination parent directories: %w", err)
	}
	return os.Rename(oldPath, newPath)
}

func Remove(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		cwd, err = filepath.EvalSymlinks(cwd)
		if err != nil {
			return err
		}
		for {
			ancestor, err := os.Stat(cwd)
			if err != nil {
				return err
			}
			if os.SameFile(info, ancestor) {
				return errors.New("refusing to remove the working directory or one of its ancestors")
			}
			parent := filepath.Dir(cwd)
			if parent == cwd {
				break
			}
			cwd = parent
		}
	}
	return os.RemoveAll(path)
}

func Bash(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", script)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if !utf8.Valid(out) {
		return "", errors.New("command output contains invalid UTF-8")
	}
	return string(out), err
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func hasHiddenComponent(path string) bool {
	for _, component := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if strings.HasPrefix(component, ".") && component != "." && component != ".." {
			return true
		}
	}
	return false
}
