package main

import "testing"

func TestASCIIFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Rossi - Città.pdf": "Rossi - Citta.pdf",
		"ÉLAN \"x\".pdf":     "ELAN _x_.pdf",
		"東京.pdf":            "__.pdf",
	} {
		if got := asciiFilename(in); got != want {
			t.Errorf("asciiFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	if got := Slugify("  Saggistica & Città!! "); got != "saggistica-citta" {
		t.Errorf("got %q", got)
	}
}
