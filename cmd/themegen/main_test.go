package main

import (
	"strings"
	"testing"

	"github.com/daios-ai/juice-ui/server"
)

// TestGeneratesNonEmptyCSS is a smoke test: themegen's output is the package's
// generator output, non-empty and containing the generated-file banner.
func TestGeneratesNonEmptyCSS(t *testing.T) {
	css := server.GenerateThemesCSS()
	if len(css) == 0 {
		t.Fatal("GenerateThemesCSS returned empty output")
	}
	if !strings.Contains(css, "GENERATED") {
		t.Error("generated CSS missing the GENERATED banner")
	}
	if !strings.Contains(css, ":root {") {
		t.Error("generated CSS missing :root block")
	}
}
