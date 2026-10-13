package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestASCIIFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Rossi - Città.pdf": "Rossi - Citta.pdf",
		"ÉLAN \"x\".pdf":    "ELAN _x_.pdf",
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

func TestEPUBCoverPathInMeta(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "b.epub")
	f, _ := os.Create(file)
	z := zip.NewWriter(f)
	add := func(name, body string) {
		w, _ := z.Create(name)
		w.Write([]byte(body))
	}
	// container.xml first and no mimetype at the start, like some real EPUBs
	add("META-INF/container.xml", `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`)
	add("OEBPS/content.opf", `<package><metadata><meta name="cover" content="Images/c.png"/></metadata>
<manifest><item id="t" href="t.xhtml" media-type="application/xhtml+xml"/></manifest></package>`)
	add("OEBPS/Images/c.png", "\x89PNG\r\n\x1a\n0000")
	add("OEBPS/t.xhtml", "<html><body>"+strings.Repeat("parola ", 900)+"</body></html>")
	z.Close()
	f.Close()

	if !isEPUB(file) {
		t.Fatal("not detected as EPUB")
	}
	if _, ext, err := epubCover(file); err != nil || ext != "png" {
		t.Fatalf("cover: %q %v", ext, err)
	}
	if n := epubPages(file); n != 4 {
		t.Errorf("pages = %d, want 4", n)
	}
}
