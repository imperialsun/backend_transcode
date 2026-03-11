package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	requiredRootFiles = []string{
		"README.md",
		"CONTRIBUTING.md",
		"SECURITY.md",
		"docs/README.md",
		"docs/database.md",
	}
	requiredFRFiles = []string{
		"docs/fr/index.md",
		"docs/fr/getting-started.md",
		"docs/fr/architecture.md",
		"docs/fr/authentication-rbac.md",
		"docs/fr/api-reference.md",
		"docs/fr/settings-reference.md",
		"docs/fr/provider-demeter-sante.md",
		"docs/fr/activity-observability.md",
		"docs/fr/database.md",
		"docs/fr/security-privacy.md",
		"docs/fr/deployment-operations.md",
		"docs/fr/ci-quality-observability.md",
		"docs/fr/troubleshooting.md",
		"docs/fr/contributing.md",
		"docs/fr/glossary.md",
	}
	requiredENFiles = []string{
		"docs/en/index.md",
		"docs/en/getting-started.md",
		"docs/en/architecture.md",
		"docs/en/authentication-rbac.md",
		"docs/en/api-reference.md",
		"docs/en/settings-reference.md",
		"docs/en/provider-demeter-sante.md",
		"docs/en/activity-observability.md",
		"docs/en/database.md",
		"docs/en/security-privacy.md",
		"docs/en/deployment-operations.md",
		"docs/en/ci-quality-observability.md",
		"docs/en/troubleshooting.md",
		"docs/en/contributing.md",
		"docs/en/glossary.md",
	}
	skipSchemes = map[string]struct{}{
		"http":   {},
		"https":  {},
		"mailto": {},
		"tel":    {},
		"data":   {},
	}
	headingPunctuationRE = regexp.MustCompile(`[!"#$%&'()*+,./:;<=>?@[\\\]^_{|}~]`)
	linkRE               = regexp.MustCompile(`(!)?\[[^\]]*\]\(([^)]+)\)`)
	schemeRE             = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*):`)
)

type markdownLink struct {
	rawTarget string
	line      int
}

func main() {
	errors := []string{}

	for _, path := range requiredRootFiles {
		assertFilePresentAndNonEmpty(path, &errors)
	}
	for _, path := range requiredFRFiles {
		assertFilePresentAndNonEmpty(path, &errors)
	}
	for _, path := range requiredENFiles {
		assertFilePresentAndNonEmpty(path, &errors)
	}

	validateFrEnParity(&errors)

	markdownFiles, err := listMarkdownFiles("docs")
	if err != nil {
		errors = append(errors, fmt.Sprintf("failed to list docs markdown files: %v", err))
	}
	markdownFiles = append([]string{"README.md", "CONTRIBUTING.md", "SECURITY.md"}, markdownFiles...)
	validateLinks(markdownFiles, &errors)

	if len(errors) > 0 {
		fmt.Fprintln(os.Stderr, "[docs-check] FAIL")
		for _, err := range errors {
			fmt.Fprintf(os.Stderr, "- %s\n", err)
		}
		os.Exit(1)
	}

	fmt.Println("[docs-check] PASS")
}

func assertFilePresentAndNonEmpty(relativePath string, errors *[]string) {
	absolutePath := filepath.Clean(relativePath)
	info, err := os.Stat(absolutePath)
	if err != nil {
		if os.IsNotExist(err) {
			*errors = append(*errors, fmt.Sprintf("missing required file: %s", relativePath))
			return
		}
		*errors = append(*errors, fmt.Sprintf("failed to stat %s: %v", relativePath, err))
		return
	}
	if info.IsDir() {
		*errors = append(*errors, fmt.Sprintf("required file is a directory: %s", relativePath))
		return
	}

	content, err := os.ReadFile(absolutePath)
	if err != nil {
		*errors = append(*errors, fmt.Sprintf("failed to read %s: %v", relativePath, err))
		return
	}
	if strings.TrimSpace(string(content)) == "" {
		*errors = append(*errors, fmt.Sprintf("required file is empty: %s", relativePath))
	}
}

func listMarkdownFiles(rootRelativeDir string) ([]string, error) {
	absoluteRoot := filepath.Clean(rootRelativeDir)
	entries := []string{}

	if _, err := os.Stat(absoluteRoot); err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return nil, err
	}

	err := filepath.WalkDir(absoluteRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".md") {
			entries = append(entries, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(entries)
	return entries, nil
}

func validateFrEnParity(errors *[]string) {
	frFiles, err := listMarkdownFiles("docs/fr")
	if err != nil {
		*errors = append(*errors, fmt.Sprintf("failed to list docs/fr: %v", err))
		return
	}
	enFiles, err := listMarkdownFiles("docs/en")
	if err != nil {
		*errors = append(*errors, fmt.Sprintf("failed to list docs/en: %v", err))
		return
	}

	frRelative := map[string]struct{}{}
	enRelative := map[string]struct{}{}

	for _, file := range frFiles {
		frRelative[strings.TrimPrefix(file, "docs/fr/")] = struct{}{}
	}
	for _, file := range enFiles {
		enRelative[strings.TrimPrefix(file, "docs/en/")] = struct{}{}
	}

	for file := range frRelative {
		if _, ok := enRelative[file]; !ok {
			*errors = append(*errors, fmt.Sprintf("FR/EN parity mismatch: missing docs/en/%s", file))
		}
	}
	for file := range enRelative {
		if _, ok := frRelative[file]; !ok {
			*errors = append(*errors, fmt.Sprintf("FR/EN parity mismatch: missing docs/fr/%s", file))
		}
	}
}

func validateLinks(markdownFiles []string, errors *[]string) {
	contentCache := map[string]string{}
	anchorCache := map[string]map[string]struct{}{}

	getContent := func(path string) (string, error) {
		if content, ok := contentCache[path]; ok {
			return content, nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		content := string(raw)
		contentCache[path] = content
		return content, nil
	}

	getAnchors := func(path string) (map[string]struct{}, error) {
		if anchors, ok := anchorCache[path]; ok {
			return anchors, nil
		}
		content, err := getContent(path)
		if err != nil {
			return nil, err
		}
		anchors := extractAnchors(content)
		anchorCache[path] = anchors
		return anchors, nil
	}

	for _, relativeFile := range markdownFiles {
		absoluteFile := filepath.Clean(relativeFile)
		content, err := getContent(absoluteFile)
		if err != nil {
			*errors = append(*errors, fmt.Sprintf("failed to read %s: %v", relativeFile, err))
			continue
		}

		for _, link := range parseMarkdownLinks(content) {
			normalizedTarget := normalizeLinkTarget(link.rawTarget)
			if normalizedTarget == "" {
				continue
			}
			if hasSkippableScheme(normalizedTarget) {
				continue
			}

			if strings.HasPrefix(normalizedTarget, "#") {
				localAnchor := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(normalizedTarget, "#")))
				if localAnchor == "" {
					continue
				}
				anchors, err := getAnchors(absoluteFile)
				if err != nil {
					*errors = append(*errors, fmt.Sprintf("failed to read anchors for %s: %v", relativeFile, err))
					continue
				}
				if _, ok := anchors[localAnchor]; !ok {
					*errors = append(*errors, fmt.Sprintf("%s:%d -> missing local anchor #%s", relativeFile, link.line, localAnchor))
				}
				continue
			}

			targetPath, anchor := splitPathAndAnchor(normalizedTarget)
			resolvedPath, ok := resolveRelativeTarget(relativeFile, targetPath)
			if !ok {
				*errors = append(*errors, fmt.Sprintf("%s:%d -> broken relative link: %s", relativeFile, link.line, normalizedTarget))
				continue
			}

			if anchor == "" {
				continue
			}

			anchors, err := getAnchors(resolvedPath)
			if err != nil {
				*errors = append(*errors, fmt.Sprintf("failed to read anchors for %s: %v", filepath.ToSlash(resolvedPath), err))
				continue
			}
			decodedAnchor, err := url.PathUnescape(anchor)
			if err != nil {
				decodedAnchor = anchor
			}
			normalizedAnchor := strings.ToLower(strings.TrimSpace(decodedAnchor))
			if normalizedAnchor == "" {
				continue
			}
			if _, ok := anchors[normalizedAnchor]; !ok {
				*errors = append(*errors, fmt.Sprintf("%s:%d -> missing anchor #%s in %s", relativeFile, link.line, normalizedAnchor, filepath.ToSlash(resolvedPath)))
			}
		}
	}
}

func extractAnchors(markdownContent string) map[string]struct{} {
	anchors := map[string]struct{}{}
	duplicateCounter := map[string]int{}

	for _, line := range strings.Split(markdownContent, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}

		heading := strings.TrimLeft(trimmed, "#")
		heading = strings.TrimSpace(heading)
		heading = strings.TrimRight(heading, "#")
		heading = strings.TrimSpace(heading)
		base := headingToSlug(heading)
		if base == "" {
			continue
		}

		seen := duplicateCounter[base]
		duplicateCounter[base] = seen + 1
		finalSlug := base
		if seen > 0 {
			finalSlug = fmt.Sprintf("%s-%d", base, seen)
		}
		anchors[finalSlug] = struct{}{}
	}

	return anchors
}

func headingToSlug(rawHeading string) string {
	cleaned := strings.TrimSpace(rawHeading)
	cleaned = strings.ReplaceAll(cleaned, "`", "")
	cleaned = headingPunctuationRE.ReplaceAllString(cleaned, "")
	cleaned = strings.Join(strings.Fields(cleaned), "-")
	cleaned = strings.Trim(cleaned, "-")
	cleaned = strings.ToLower(cleaned)
	return cleaned
}

func parseMarkdownLinks(markdownContent string) []markdownLink {
	links := []markdownLink{}
	matches := linkRE.FindAllStringSubmatchIndex(markdownContent, -1)
	for _, match := range matches {
		if len(match) < 6 {
			continue
		}
		isImage := match[2] != -1
		if isImage {
			continue
		}
		rawTarget := strings.TrimSpace(markdownContent[match[4]:match[5]])
		if rawTarget == "" {
			continue
		}
		line := 1 + strings.Count(markdownContent[:match[0]], "\n")
		links = append(links, markdownLink{rawTarget: rawTarget, line: line})
	}
	return links
}

func normalizeLinkTarget(rawTarget string) string {
	trimmed := strings.TrimSpace(rawTarget)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "<") {
		if end := strings.Index(trimmed, ">"); end > 1 {
			return strings.TrimSpace(trimmed[1:end])
		}
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func hasSkippableScheme(target string) bool {
	if strings.HasPrefix(target, "//") {
		return true
	}
	match := schemeRE.FindStringSubmatch(target)
	if len(match) != 2 {
		return false
	}
	_, ok := skipSchemes[strings.ToLower(match[1])]
	return ok || match[1] != ""
}

func splitPathAndAnchor(target string) (string, string) {
	index := strings.Index(target, "#")
	if index == -1 {
		return target, ""
	}
	return target[:index], target[index+1:]
}

func resolveRelativeTarget(sourceFile string, targetPath string) (string, bool) {
	sourceDir := filepath.Dir(filepath.Join(".", filepath.FromSlash(sourceFile)))
	absoluteTarget := filepath.Clean(filepath.Join(sourceDir, filepath.FromSlash(targetPath)))

	candidates := []string{absoluteTarget}
	if filepath.Ext(absoluteTarget) == "" {
		candidates = append(candidates, absoluteTarget+".md")
		candidates = append(candidates, filepath.Join(absoluteTarget, "README.md"))
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return filepath.ToSlash(candidate), true
		}
	}

	return "", false
}
