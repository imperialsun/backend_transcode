package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadingToSlugAndExtractAnchors(t *testing.T) {
	expected := "my-heading"
	if headingToSlug("My Heading!") != expected {
		t.Fatalf("expected %q, got %q", expected, headingToSlug("My Heading!"))
	}

	anchors := extractAnchors("# My Heading\n## My Heading\n# Other")
	if _, ok := anchors["my-heading"]; !ok {
		t.Fatal("expected anchor my-heading")
	}
	if _, ok := anchors["my-heading-1"]; !ok {
		t.Fatal("expected anchor my-heading-1")
	}
}

func TestParseMarkdownLinksAndNormalizeTarget(t *testing.T) {
	content := "[link](./path.md) [empty]() [angle](<./other.md>)"
	links := parseMarkdownLinks(content)
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if normalizeLinkTarget(" <  ./a.md  >") != "./a.md" {
		t.Fatalf("unexpected normalized target")
	}
}

func TestHasSkippableScheme(t *testing.T) {
	if !hasSkippableScheme("http://example.com") {
		t.Fatal("expected http scheme to be skippable")
	}
	if !hasSkippableScheme("//example.com") {
		t.Fatal("expected protocol-relative URL to be skippable")
	}
	// any scheme is treated as skippable unless it is empty.
	if !hasSkippableScheme("custom:foo") {
		t.Fatal("expected custom scheme to be skippable")
	}
}

func TestSplitPathAndAnchor(t *testing.T) {
	p, a := splitPathAndAnchor("docs/readme.md#section")
	if p != "docs/readme.md" || a != "section" {
		t.Fatalf("unexpected split: %q #%q", p, a)
	}
}

func TestResolveRelativeTarget(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	_ = os.Chdir(dir)

	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sub", "README.md"), []byte("hello"), 0o644)

	path, ok := resolveRelativeTarget(filepath.Join("sub", "file.md"), "../sub")
	if !ok {
		t.Fatal("expected to resolve existing target")
	}
	if filepath.Base(path) != "README.md" {
		t.Fatalf("expected README.md target, got %q", path)
	}
}

func TestRunDocsCheck_PassAndFailCases(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}

	writeFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("failed to create dir for %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}

	rootFiles := []string{"README.md", "CONTRIBUTING.md", "SECURITY.md", "docs/README.md", "docs/database.md"}
	frEnFiles := []string{
		"index.md", "getting-started.md", "architecture.md", "authentication-rbac.md", "api-reference.md",
		"settings-reference.md", "provider-demeter-sante.md", "activity-observability.md", "database.md",
		"security-privacy.md", "deployment-operations.md", "ci-quality-observability.md", "troubleshooting.md",
		"contributing.md", "glossary.md",
	}
	for _, path := range rootFiles {
		content := "# Root\n\nSee [FR](docs/fr/index.md)"
		if strings.HasPrefix(path, "docs/") {
			content = "# Root Doc\n"
		}
		writeFile(path, content)
	}
	for _, name := range frEnFiles {
		writeFile(filepath.Join("docs", "fr", name), "# Titre\n\nVoir [EN](../en/"+name+")\n")
		writeFile(filepath.Join("docs", "en", name), "# Title\n\nSee [FR](../fr/"+name+")\n")
	}

	if errors := runDocsCheck(); len(errors) != 0 {
		t.Fatalf("expected docs check to pass, got %v", errors)
	}

	if err := os.Remove(filepath.Join("docs", "en", "glossary.md")); err != nil {
		t.Fatalf("failed to remove glossary: %v", err)
	}
	errors := runDocsCheck()
	if len(errors) == 0 {
		t.Fatal("expected docs check to fail after parity break")
	}

	joined := strings.Join(errors, "\n")
	if !strings.Contains(joined, "missing required file: docs/en/glossary.md") && !strings.Contains(joined, "FR/EN parity mismatch") {
		t.Fatalf("expected missing file or parity mismatch error, got %v", errors)
	}
}
