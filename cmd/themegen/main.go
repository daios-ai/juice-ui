// Command themegen renders the terminal theme stylesheet from the embedded
// Ghostty palettes to stdout. It is the source of server/static/themes.css:
//
//	make themes   # go run ./cmd/themegen > server/static/themes.css
//
// server/themes_test.go asserts the committed file matches this output, so a
// forgotten regeneration fails the build rather than shipping stale colors.
package main

import (
	"fmt"
	"os"

	"github.com/daios-ai/juice-ui/server"
)

func main() {
	if _, err := fmt.Fprint(os.Stdout, server.GenerateThemesCSS()); err != nil {
		fmt.Fprintln(os.Stderr, "themegen:", err)
		os.Exit(1)
	}
}
