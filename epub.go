package main

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// charsPerPage approximates a printed page when estimating EPUB length.
const charsPerPage = 1800

type epubPackage struct {
	Metas []struct {
		Name    string `xml:"name,attr"`
		Content string `xml:"content,attr"`
	} `xml:"metadata>meta"`
	Items []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
}

// openEPUB returns the zip reader, the OPF directory and the parsed package.
func openEPUB(file string) (*zip.ReadCloser, string, *epubPackage, error) {
	z, err := zip.OpenReader(file)
	if err != nil {
		return nil, "", nil, err
	}
	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := readZipXML(&z.Reader, "META-INF/container.xml", &container); err != nil || len(container.Rootfiles) == 0 {
		z.Close()
		return nil, "", nil, errors.New("epub: missing container.xml rootfile")
	}
	opf := container.Rootfiles[0].Path
	var pkg epubPackage
	if err := readZipXML(&z.Reader, opf, &pkg); err != nil {
		z.Close()
		return nil, "", nil, err
	}
	return z, path.Dir(opf), &pkg, nil
}

func readZipXML(z *zip.Reader, name string, v any) error {
	f, err := z.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := xml.NewDecoder(io.LimitReader(f, 8<<20))
	dec.Strict = false
	return dec.Decode(v)
}

func zipPath(dir, href string) string {
	if i := strings.IndexAny(href, "#?"); i >= 0 {
		href = href[:i]
	}
	if dir == "." {
		return path.Clean(href)
	}
	return path.Join(dir, href)
}

// epubCover returns the cover image embedded in an EPUB, with its extension.
func epubCover(file string) ([]byte, string, error) {
	z, dir, pkg, err := openEPUB(file)
	if err != nil {
		return nil, "", err
	}
	defer z.Close()

	coverID := ""
	for _, m := range pkg.Metas {
		if m.Name == "cover" {
			coverID = m.Content
		}
	}
	var candidates []string
	for _, it := range pkg.Items { // EPUB 3
		if strings.Contains(it.Properties, "cover-image") {
			candidates = append(candidates, it.Href)
		}
	}
	for _, it := range pkg.Items { // EPUB 2
		if coverID != "" && it.ID == coverID && strings.HasPrefix(it.MediaType, "image/") {
			candidates = append(candidates, it.Href)
		}
	}
	if coverID != "" { // non-standard: <meta name="cover"> holding a path instead of an id
		candidates = append(candidates, coverID)
	}
	for _, it := range pkg.Items { // heuristic
		if strings.HasPrefix(it.MediaType, "image/") &&
			(strings.Contains(strings.ToLower(it.ID), "cover") || strings.Contains(strings.ToLower(it.Href), "cover")) {
			candidates = append(candidates, it.Href)
		}
	}
	for _, href := range candidates {
		f, err := z.Open(zipPath(dir, href))
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 20<<20))
		f.Close()
		if err != nil {
			continue
		}
		if ext, err := coverExt(&upload{head: data}); err == nil {
			return data, ext, nil
		}
	}
	return nil, "", errors.New("epub: no JPEG/PNG cover found")
}

var (
	tagRe   = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// epubPages estimates the printed length of an EPUB from its text content.
func epubPages(file string) int {
	z, dir, pkg, err := openEPUB(file)
	if err != nil {
		return 0
	}
	defer z.Close()
	chars := 0
	for _, it := range pkg.Items {
		if it.MediaType != "application/xhtml+xml" && it.MediaType != "text/html" {
			continue
		}
		f, err := z.Open(zipPath(dir, it.Href))
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(f, 32<<20))
		f.Close()
		text := spaceRe.ReplaceAllString(tagRe.ReplaceAllString(string(data), " "), " ")
		chars += utf8.RuneCountInString(strings.TrimSpace(text))
	}
	if chars == 0 {
		return 0
	}
	return (chars + charsPerPage - 1) / charsPerPage
}

// isEPUB reports whether file is a zip containing an EPUB container.
func isEPUB(file string) bool {
	z, err := zip.OpenReader(file)
	if err != nil {
		return false
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == "META-INF/container.xml" {
			return true
		}
	}
	return false
}
