package templates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var placeholderPattern = regexp.MustCompile(`\{([^{}]+)\}`)

// Placeholders documents every placeholder accepted by Resolve. The
// descriptions double as the template-help dialog text, so each one
// carries a concrete example.
var Placeholders = map[string]string{
	"dir":            "original input directory, for example /home/user/Videos",
	"name":           "original filename without extension, for example movie",
	"ext":            "selected output extension without dot, for example mkv",
	"input_ext":      "original extension without dot, for example mkv",
	"datetime":       "local date and time in YYYYMMDD-HHMMSS format, for example 20260312-143005",
	"date":           "local date in YYYYMMDD format, for example 20260312",
	"time":           "local time in HHMMSS format, for example 143005",
	"profile":        "selected profile name; spaces and unsafe characters become underscores, for example HEVC VAAPI 720p CQ becomes HEVC_VAAPI_720p_CQ",
	"codec":          "selected video encoder name, for example hevc_vaapi",
	"width":          "configured maximum width or source width, for example 1280",
	"height":         "configured maximum height or source height, for example 720",
	"video_settings": "short summary of the video encoder settings, for example libx265_main10_crf40_medium_1280x720",
	"audio_settings": "short summary of the audio encoder settings, for example libopus_96k",
}

type Request struct {
	InputPath string
	Extension string
	Profile   string
	Codec     string
	Width     int
	Height    int
	Time      time.Time
	// VideoSettings and AudioSettings carry the pre-serialized setting
	// summaries for the {video_settings} / {audio_settings} placeholders
	// (encoder.SettingsSummary builds them). They arrive as plain
	// strings so this package stays dependency-free; empty values
	// sanitize to "unknown".
	VideoSettings string
	AudioSettings string
}

// Values is a compatibility alias for Request.
type Values = Request

// Resolve expands a template without permitting placeholder values to add path
// components. Values.Time must be supplied by the caller to keep preview and
// execution deterministic.
func Resolve(template string, values Request) (string, error) {
	return resolveTemplate(template, values, true)
}

// ResolveParts expands a template for a multi-part separate export. It
// is identical to Resolve except that the input-collision check does
// not apply: the base path is only a naming seed there — every written
// target carries a _partNN suffix (SegmentPath), so a base equal to the
// input is harmless, and the per-part targets are validated when the
// export is planned. Traversal and filename checks still apply.
func ResolveParts(template string, values Request) (string, error) {
	return resolveTemplate(template, values, false)
}

func resolveTemplate(template string, values Request, checkInputCollision bool) (string, error) {
	if strings.TrimSpace(template) == "" {
		return "", errors.New("output template is empty")
	}
	if values.InputPath == "" {
		return "", errors.New("input path is empty")
	}
	if values.Time.IsZero() {
		return "", errors.New("template time is required")
	}
	if err := validateLiteralPath(template); err != nil {
		return "", err
	}

	inputPath := filepath.Clean(values.InputPath)
	components := PlaceholderValues(values)

	var resolveErr error
	resolved := placeholderPattern.ReplaceAllStringFunc(template, func(token string) string {
		key := token[1 : len(token)-1]
		if _, known := Placeholders[key]; !known {
			resolveErr = fmt.Errorf("unknown output placeholder %q", token)
			return token
		}
		return components[key]
	})
	if resolveErr != nil {
		return "", resolveErr
	}
	if strings.ContainsAny(resolved, "{}") {
		return "", errors.New("malformed output placeholder")
	}

	cleaned := filepath.Clean(resolved)
	if cleaned == "." || filepath.Base(cleaned) == "." {
		return "", errors.New("output template does not produce a filename")
	}
	if err := rejectTraversal(cleaned); err != nil {
		return "", err
	}
	if checkInputCollision {
		if err := ValidateOutput(inputPath, cleaned); err != nil {
			return "", err
		}
	}
	return cleaned, nil
}

// PlaceholderValues returns what each placeholder expands to for values,
// exactly as Resolve substitutes it. The input-derived placeholders (dir,
// name, input_ext) are absent while InputPath is empty, and the clock ones
// (datetime, date, time) while Time is zero.
func PlaceholderValues(values Request) map[string]string {
	components := map[string]string{
		"ext":            sanitize(strings.TrimPrefix(values.Extension, ".")),
		"profile":        sanitize(values.Profile),
		"codec":          sanitize(values.Codec),
		"width":          sanitizeDimension(values.Width),
		"height":         sanitizeDimension(values.Height),
		"video_settings": sanitize(values.VideoSettings),
		"audio_settings": sanitize(values.AudioSettings),
	}
	if !values.Time.IsZero() {
		components["datetime"] = values.Time.Format("20060102-150405")
		components["date"] = values.Time.Format("20060102")
		components["time"] = values.Time.Format("150405")
	}
	if values.InputPath != "" {
		inputPath := filepath.Clean(values.InputPath)
		base := filepath.Base(inputPath)
		components["dir"] = filepath.Dir(inputPath)
		components["name"] = sanitize(strings.TrimSuffix(base, filepath.Ext(base)))
		components["input_ext"] = sanitize(strings.TrimPrefix(filepath.Ext(base), "."))
	}
	return components
}

// ValidateOutput rejects an empty output and an output that resolves to the
// source file. It intentionally does not reject an existing output; overwrite
// confirmation is a run-time UI concern.
func ValidateOutput(inputPath, outputPath string) error {
	if strings.TrimSpace(inputPath) == "" {
		return errors.New("input path is empty")
	}
	if strings.TrimSpace(outputPath) == "" {
		return errors.New("output path is empty")
	}

	inputAbs, err := filepath.Abs(filepath.Clean(inputPath))
	if err != nil {
		return fmt.Errorf("resolve input path: %w", err)
	}
	outputAbs, err := filepath.Abs(filepath.Clean(outputPath))
	if err != nil {
		return fmt.Errorf("resolve output path: %w", err)
	}
	if samePath(inputAbs, outputAbs) {
		return errors.New("output path is the same as the input path")
	}
	inputInfo, inputErr := os.Stat(inputAbs)
	outputInfo, outputErr := os.Stat(outputAbs)
	if inputErr == nil && outputErr == nil && os.SameFile(inputInfo, outputInfo) {
		return errors.New("output path refers to the input file")
	}
	return nil
}

func validateLiteralPath(template string) error {
	withoutDir := strings.ReplaceAll(template, "{dir}", ".")
	if filepath.IsAbs(withoutDir) || filepath.VolumeName(withoutDir) != "" {
		return errors.New("only {dir} may provide an absolute path")
	}
	return rejectTraversal(withoutDir)
}

func rejectTraversal(path string) error {
	for _, part := range strings.FieldsFunc(filepath.ToSlash(path), func(r rune) bool {
		return r == '/'
	}) {
		if part == ".." {
			return errors.New("output template contains path traversal")
		}
	}
	return nil
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.'
		if valid {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(b.String(), "._")
	if result == "" || result == "." || result == ".." {
		return "unknown"
	}
	return result
}

func sanitizeDimension(value int) string {
	if value <= 0 {
		return "0"
	}
	return strconv.Itoa(value)
}

// SegmentPath returns the output path for segment index (1-based) of a
// separate multi-segment export: path with a deterministic _partNN suffix
// inserted before the extension. A count of one or less returns path
// unchanged, so single-segment exports keep today's names. The suffix is
// inserted after template resolution — callers resolve the template once
// and share the result across every part, keeping {datetime} identical —
// and survives the same alphabet the template sanitizer uses.
func SegmentPath(path string, index, count int) string {
	if count <= 1 {
		return path
	}
	return insertBeforeExt(path, partSuffix(index, count))
}

// partSuffix renders the zero-padded segment marker. The field width is
// two at minimum and grows with the segment count, so every part of a
// given export shares one width and names sort chronologically.
func partSuffix(index, count int) string {
	width := max(2, len(strconv.Itoa(count)))
	return fmt.Sprintf("_part%0*d", width, index)
}

// insertBeforeExt appends suffix to path before its extension. The last
// dot counts as an extension separator only when it is not the first
// character of the base name, so dotfiles and extension-less names keep
// the suffix at the end.
func insertBeforeExt(path, suffix string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext == "" || ext == base {
		return path + suffix
	}
	return strings.TrimSuffix(path, ext) + suffix + ext
}

func samePath(left, right string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}
