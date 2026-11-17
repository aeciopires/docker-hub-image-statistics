package web

import (
	"io/fs"
	"regexp"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	for _, name := range []string{
		"index.html", "app.css", "app.js", "favicon.svg",
		"fonts/roboto-latin-wght-normal.woff2", "fonts/LICENSE-Roboto-OFL.txt",
	} {
		if _, err := fs.Stat(FS(), name); err != nil {
			t.Errorf("missing embedded asset %s: %v", name, err)
		}
	}
}

// TestNoExternalResources guarantees the UI never downloads anything from the Internet.
func TestNoExternalResources(t *testing.T) {
	external := regexp.MustCompile(`(?i)(src|href)\s*=\s*["']?(https?:)?//|@import\s+url\(\s*["']?https?:|url\(\s*["']?https?:|fetch\(\s*["']https?:`)
	for _, name := range []string{"index.html", "app.css", "app.js"} {
		data, err := fs.ReadFile(FS(), name)
		if err != nil {
			t.Fatal(err)
		}
		if loc := external.FindIndex(data); loc != nil {
			t.Errorf("%s references an external resource: %q", name, data[loc[0]:min(loc[1]+40, len(data))])
		}
	}
}
