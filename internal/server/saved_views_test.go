package server

import (
	"strings"
	"testing"
)

func TestValidSavedViewNameCountsCharacters(t *testing.T) {
	if !validSavedViewName(strings.Repeat("\u00e4", savedViewNameMax)) {
		t.Fatal("60 multibyte characters should be accepted")
	}
	if validSavedViewName(strings.Repeat("\u00e4", savedViewNameMax+1)) {
		t.Fatal("61 characters should be rejected")
	}
	if validSavedViewName("") {
		t.Fatal("empty name should be rejected")
	}
}
