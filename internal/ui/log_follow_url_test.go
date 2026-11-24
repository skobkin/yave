package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLiveLogHeldWhileNotFollowing(t *testing.T) {
	c := dirtyTestController(t)
	c.setLogText("one")
	c.logFollow.SetChecked(false)
	c.setLiveLogText("one\ntwo")
	if c.log.Text != "one" {
		t.Fatalf("log = %q, view must stay untouched", c.log.Text)
	}
	c.logFollow.SetChecked(true)
	if c.log.Text != "one\ntwo" {
		t.Fatalf("log = %q, pending text must show when following resumes", c.log.Text)
	}
}

func TestFileURLIsAbsolute(t *testing.T) {
	u := fileURL("out.mkv")
	wd, _ := os.Getwd()
	if u.Host != "" || !strings.HasSuffix(u.Path, "/out.mkv") || !strings.HasPrefix(u.Path, "/") {
		t.Fatalf("url = %v", u)
	}
	if runtime.GOOS != "windows" && u.Path != filepath.ToSlash(filepath.Join(wd, "out.mkv")) {
		t.Fatalf("path = %q", u.Path)
	}
}
