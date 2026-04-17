package main

import (
	"regexp"
	"strings"
)

// projectID derives a stable project identifier from a git remote URL
// (or fallback string). The result is slugified to [a-z0-9-] so it is safe
// as a filesystem path segment and room name prefix.
func projectID(repoURL, fallback string) string {
	raw := ""
	if repoURL != "" {
		u := strings.TrimSuffix(repoURL, ".git")
		u = strings.TrimSuffix(u, "/")
		// split on both / and : to handle https and ssh (git@host:org/repo) URLs
		parts := strings.FieldsFunc(u, func(r rune) bool {
			return r == '/' || r == ':'
		})
		if len(parts) > 0 {
			raw = parts[len(parts)-1]
		}
	}
	if raw == "" {
		raw = fallback
	}
	return slugify(raw)
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "default"
	}
	return s
}
