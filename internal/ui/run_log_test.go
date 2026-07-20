package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLogKeepsBoundedTailAndCompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversion.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	log := newRunLog(path, file)
	log.AppendLine("first line")
	for i := 0; i < 4000; i++ {
		log.AppendLine(fmt.Sprintf("line-%04d %s", i, strings.Repeat("x", 80)))
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	visible, _ := log.Snapshot()
	if !strings.Contains(visible, "Earlier output omitted from this view") {
		t.Fatal("visible log does not report truncation")
	}
	if strings.Contains(visible, "first line") {
		t.Fatal("visible log retained evicted first line")
	}
	if !strings.Contains(visible, "line-3999") {
		t.Fatal("visible log does not retain newest line")
	}
	if len(visible) > visibleRunLogLimit+len(path)+100 {
		t.Fatalf("visible log size = %d, expected bounded tail", len(visible))
	}

	complete, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(complete), "first line") || !strings.Contains(string(complete), "line-3999") {
		t.Fatal("complete log does not contain full output")
	}
}

func TestRunLogLiveSnapshotKeepsSmallNewestTail(t *testing.T) {
	log := newRunLog("", nil)
	log.AppendLine("first line")
	for i := 0; i < 1000; i++ {
		log.AppendLine(fmt.Sprintf("line-%04d %s", i, strings.Repeat("x", 80)))
	}

	visible, _ := log.LiveSnapshot()
	if !strings.Contains(visible, "Earlier output omitted from this view") {
		t.Fatal("live log does not report truncation")
	}
	if strings.Contains(visible, "first line") {
		t.Fatal("live log retained old output")
	}
	if !strings.Contains(visible, "line-0999") {
		t.Fatal("live log does not retain newest output")
	}
	if len(visible) > liveRunLogLimit+100 {
		t.Fatalf("live log size = %d, expected at most about %d", len(visible), liveRunLogLimit)
	}
}

func TestLastRunLogUsesCacheAndOverwritesPriorRun(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	first, err := openLastRunLog()
	if err != nil {
		t.Fatal(err)
	}
	first.AppendLine("first run")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := openLastRunLog()
	if err != nil {
		t.Fatal(err)
	}
	second.AppendLine("second run")
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(cacheDir, "yave", "last-conversion.log")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "first run") || string(contents) != "second run\n" {
		t.Fatalf("last conversion log = %q", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestRunLogOpenFailureStillProvidesMemoryLog(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cachePath, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", cachePath)

	log, err := openLastRunLog()
	if err == nil {
		t.Fatal("openLastRunLog() error = nil")
	}
	log.AppendLine("conversion continues")
	visible, _ := log.Snapshot()
	if !strings.Contains(visible, "conversion continues") {
		t.Fatalf("memory log = %q", visible)
	}
}
