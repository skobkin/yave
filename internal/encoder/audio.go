package encoder

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"git.skobk.in/skobkin/yave/internal/model"
)

// audioEncoder is the shared implementation for the curated audio
// encoders (libopus, opus, aac, libmp3lame, mp3, flac). The differences
// between them are bitrate choices (none for FLAC), compression level
// support (FLAC only), and codec family identifier.
type audioEncoder struct {
	name             string
	label            string
	codecFamily      string
	bitrateChoices   []string // lower-case choices used by validateAudio; nil/empty disables bitrate validation
	compressionRange *IntegerRange
	priority         int
}

func (e *audioEncoder) Name() string         { return e.name }
func (e *audioEncoder) Label() string        { return e.label }
func (e *audioEncoder) Media() string        { return MediaAudio }
func (e *audioEncoder) Kind() string         { return KindEncode }
func (e *audioEncoder) Priority() int        { return e.priority }
func (e *audioEncoder) HWBackend() HWBackend { return HWNone }
func (e *audioEncoder) CodecFamily() string  { return e.codecFamily }
func (e *audioEncoder) AudioCompressionRange() (IntegerRange, bool) {
	if e.compressionRange == nil {
		return IntegerRange{}, false
	}
	return *e.compressionRange, true
}
func (e *audioEncoder) BitrateChoices() []string { return e.bitrateChoices }

// BuildAudioArgs emits -c:a and the encoder-specific flags. The flag
// shape matches the original ffmpeg.buildAudioArgs exactly so existing
// command_test.go tests continue to pass:
//
//	-c:a <name> [-compression_level N | -b:a <bitrate>]
func (e *audioEncoder) BuildAudioArgs(a model.AudioSettings) ([]string, error) {
	if a.Encoder == "" {
		return nil, fmt.Errorf("audio encoder is empty")
	}
	args := []string{"-c:a", e.name}
	if e.compressionRange != nil {
		args = append(args, "-compression_level", strconv.Itoa(a.Compression))
	} else if strings.TrimSpace(a.Bitrate) != "" {
		args = append(args, "-b:a", a.Bitrate)
	}
	return args, nil
}

// Validate enforces encoder-specific bitrate / compression choices.
// The issue list mirrors the field-level errors emitted by the prior
// validateAudio switch.
func (e *audioEncoder) Validate(a model.AudioSettings) ([]Issue, error) {
	var issues []Issue
	if e.compressionRange != nil {
		if !e.compressionRange.Contains(a.Compression) {
			issues = append(issues, Issue{
				Level:   IssueError,
				Field:   "profile.audio.compression",
				Message: fmt.Sprintf("%s compression level must be between %d and %d", strings.ToUpper(e.name), e.compressionRange.Min, e.compressionRange.Max),
			})
		}
		return issues, nil
	}
	if len(e.bitrateChoices) == 0 {
		return nil, nil
	}
	want := strings.ToLower(strings.TrimSpace(a.Bitrate))
	if slices.Contains(e.bitrateChoices, want) {
		return nil, nil
	}
	issues = append(issues, Issue{
		Level:   IssueError,
		Field:   "profile.audio.bitrate",
		Message: fmt.Sprintf("bitrate must be one of: %s", strings.Join(e.bitrateChoices, ", ")),
	})
	return issues, nil
}

// audioEncoders returns the curated audio set. The list is in
// registration order; Registry.All() sorts by priority descending.
func audioEncoders() []Encoder {
	return []Encoder{
		&audioEncoder{
			name:           "libopus",
			label:          "Opus",
			codecFamily:    "opus",
			bitrateChoices: []string{"96k", "128k", "160k", "192k", "256k"},
			priority:       100,
		},
		&audioEncoder{
			name:           "opus",
			label:          "Opus",
			codecFamily:    "opus",
			bitrateChoices: []string{"96k", "128k", "160k", "192k", "256k"},
			priority:       90,
		},
		&audioEncoder{
			name:           "aac",
			label:          "AAC",
			codecFamily:    "aac",
			bitrateChoices: []string{"128k", "160k", "192k", "256k"},
			priority:       100,
		},
		&audioEncoder{
			name:           "libmp3lame",
			label:          "MP3",
			codecFamily:    "mp3",
			bitrateChoices: []string{"128k", "192k", "256k", "320k"},
			priority:       70,
		},
		&audioEncoder{
			name:           "mp3",
			label:          "MP3",
			codecFamily:    "mp3",
			bitrateChoices: []string{"128k", "192k", "256k", "320k"},
			priority:       60,
		},
		&audioEncoder{
			name:             "flac",
			label:            "FLAC",
			codecFamily:      "flac",
			compressionRange: &IntegerRange{Min: 0, Max: 12},
			priority:         70,
		},
	}
}
