// Package issuerefs validates and shortens JIRA and GitHub PR URLs used as
// per-task reference metadata. The validation is strict — URLs that don't
// match the expected shapes are rejected at the admin write path — so the
// Short functions only have to handle the canonical forms. They also degrade
// gracefully on surprise inputs by returning the raw URL.
package issuerefs

import (
	"errors"
	"regexp"
)

var (
	ErrInvalidJira = errors.New("invalid JIRA URL")

	jiraRE = regexp.MustCompile(`^https?://[^/]+/browse/([A-Z][A-Z0-9_]+-\d+)(?:[?#].*)?$`)
)

func ValidateJira(s string) error {
	if !jiraRE.MatchString(s) {
		return ErrInvalidJira
	}
	return nil
}

func JiraShort(url string) string {
	if url == "" {
		return ""
	}
	m := jiraRE.FindStringSubmatch(url)
	if len(m) < 2 {
		return url
	}
	return m[1]
}
