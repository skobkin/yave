package transcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.skobk.in/skobkin/yave/internal/model"
)

type Callbacks struct {
	OnLog      func(string)
	OnProgress func(model.Progress)
}

type Runner struct{}

func (Runner) Run(ctx context.Context, plan model.RunPlan, duration time.Duration, cb Callbacks) error {
	cmd := exec.CommandContext(ctx, plan.Executable, plan.Args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open FFmpeg progress pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open FFmpeg log pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start FFmpeg: %w", err)
	}

	started := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLogs(stderr, cb.OnLog)
	}()
	go func() {
		defer wg.Done()
		scanProgress(stdout, duration, started, cb.OnProgress)
	}()

	waitErr := cmd.Wait()
	wg.Wait()
	if errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if waitErr != nil {
		return fmt.Errorf("FFmpeg failed: %w", waitErr)
	}
	return nil
}

func scanLogs(r io.Reader, onLog func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if onLog != nil {
			onLog(scanner.Text())
		}
	}
}

func scanProgress(r io.Reader, duration time.Duration, started time.Time, onProgress func(model.Progress)) {
	scanner := bufio.NewScanner(r)
	values := make(map[string]string)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		values[key] = value
		if key != "progress" {
			continue
		}
		progress := ParseProgress(values, duration)
		progress.Elapsed = time.Since(started)
		if onProgress != nil {
			onProgress(progress)
		}
		clear(values)
	}
}

func ParseProgress(values map[string]string, duration time.Duration) model.Progress {
	p := model.Progress{
		Speed:   values["speed"],
		Bitrate: values["bitrate"],
		State:   values["progress"],
	}
	p.Frame, _ = strconv.ParseInt(values["frame"], 10, 64)
	if raw := values["out_time_us"]; raw != "" {
		if micros, err := strconv.ParseInt(raw, 10, 64); err == nil {
			p.OutTime = time.Duration(micros) * time.Microsecond
		}
	} else if raw := values["out_time"]; raw != "" {
		p.OutTime = parseClock(raw)
	}
	if duration > 0 {
		p.Percentage = min(1, max(0, float64(p.OutTime)/float64(duration)))
	}
	return p
}

func parseClock(value string) time.Duration {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0
	}
	hours, errH := strconv.ParseFloat(parts[0], 64)
	minutes, errM := strconv.ParseFloat(parts[1], 64)
	seconds, errS := strconv.ParseFloat(parts[2], 64)
	if errH != nil || errM != nil || errS != nil {
		return 0
	}
	return time.Duration((hours*3600 + minutes*60 + seconds) * float64(time.Second))
}
