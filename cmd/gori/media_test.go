package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/leelsey/gori"
)

func TestSaveMedia(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	msg := gori.Message{Content: []gori.Content{
		gori.Image{MediaType: "image/png", Data: []byte{1, 2, 3}},
		gori.Audio{MediaType: "audio/wav", Data: []byte{4, 5, 6}},
	}}
	saveMedia(msg, io.Discard)

	entries, _ := os.ReadDir(dir)
	var sawImage, sawAudio bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "gori-image-1") {
			sawImage = true
		}
		if strings.HasPrefix(e.Name(), "gori-audio-1") {
			sawAudio = true
		}
	}
	if !sawImage || !sawAudio {
		t.Errorf("expected saved image and audio files, got %v", entries)
	}
}
