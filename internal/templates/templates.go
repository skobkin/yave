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

// Placeholders documents every placeholder accepted by Resolve.
var Placeholders = map[string]string{
	"dir":       "original input directory",
	"name":      "original filename without extension",
	"ext":       "selected output extension without dot",
	"input_ext": "original extension without dot",
	"datetime":  "local date and time in YYYYMMDD-HHMMSS format",
	"date":      "local date in YYYYMMDD format",
	"time":      "local time in HHMMSS format",
	"profile":   "selected profile name",
	"codec":     "selected video encoder name",
	"width":     "configured maximum width or source width",
	"height":    "configured maximum height or source height",
}

type Request struct {
	InputPath string
	Extension string
	Profile   string
	Codec     string
	Width     int
	Height    int
	Time      time.Time
}

// Values is a compatibility alias for Request.
type Values = Request

// Resolve expands a template without permitting placeholder values to add path
// components. Values.Time must be supplied by the caller to keep preview and
// execution deterministic.
func Resolve(template string, values Request) (string, error) {
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
	base := filepath.Base(inputPath)
	inputExt := strings.TrimPrefix(filepath.Ext(base), ".")
	name := strings.TrimSuffix(base, filepath.Ext(base))
	ext := strings.TrimPrefix(values.Extension, ".")

	components := map[string]string{
		"name":      sanitize(name),
		"ext":       sanitize(ext),
		"input_ext": sanitize(inputExt),
		"datetime":  values.Time.Format("20060102-150405"),
		"date":      values.Time.Format("20060102"),
		"time":      values.Time.Format("150405"),
		"profile":   sanitize(values.Profile),
		"codec":     sanitize(values.Codec),
		"width":     sanitizeDimension(values.Width),
		"height":    sanitizeDimension(values.Height),
	}

	var resolveErr error
	resolved := placeholderPattern.ReplaceAllStringFunc(template, func(token string) string {
		key := token[1 : len(token)-1]
		if _, known := Placeholders[key]; !known {
			resolveErr = fmt.Errorf("unknown output placeholder %q", token)
			return token
		}
		if key == "dir" {
			return filepath.Dir(inputPath)
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
	if err := ValidateOutput(inputPath, cleaned); err != nil {
		return "", err
	}
	return cleaned, nil
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

func samePath(left, right string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}
