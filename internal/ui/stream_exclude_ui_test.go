package ui

import "testing"

func TestSubtitlesStayWithoutAudio(t *testing.T) {
	c := dirtyTestController(t)
	c.includeAudio.SetChecked(false)
	items := c.audioFormItems()
	if len(items) != 1 || items[0].Text != "Subtitles" {
		t.Fatalf("items = %v, want only Subtitles", items)
	}
	c.includeVideo.SetChecked(false)
	if got := c.audioFormItems(); len(got) != 0 {
		t.Fatalf("both excluded: got %d rows", len(got))
	}
}

func TestExcludedVideoIgnoresSizeFields(t *testing.T) {
	c := dirtyTestController(t)
	c.maxWidth.SetText("720p")
	if len(c.numericFieldProblems()) == 0 {
		t.Fatal("invalid width must block while video is included")
	}
	c.includeVideo.SetChecked(false)
	if p := c.numericFieldProblems(); len(p) != 0 {
		t.Fatalf("problems = %v, want none with video excluded", p)
	}
}
