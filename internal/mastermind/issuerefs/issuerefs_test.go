package issuerefs

import (
	"errors"
	"testing"
)

func TestValidateJira(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"cloud canonical", "https://acme.atlassian.net/browse/PROJ-123", nil},
		{"self-hosted", "http://jira.internal.example/browse/AB-1", nil},
		{"with query", "https://acme.atlassian.net/browse/PROJ-123?focusedCommentId=1", nil},
		{"with fragment", "https://acme.atlassian.net/browse/PROJ-123#comments", nil},
		{"missing scheme", "acme.atlassian.net/browse/PROJ-123", ErrInvalidJira},
		{"wrong path", "https://acme.atlassian.net/issues/PROJ-123", ErrInvalidJira},
		{"lowercase key", "https://acme.atlassian.net/browse/proj-123", ErrInvalidJira},
		{"no number", "https://acme.atlassian.net/browse/PROJ", ErrInvalidJira},
		{"empty", "", ErrInvalidJira},
		{"trailing slash", "https://acme.atlassian.net/browse/PROJ-123/", ErrInvalidJira},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateJira(tc.in)
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("ValidateJira(%q): got %v, want %v", tc.in, got, tc.wantErr)
			}
		})
	}
}

func TestJiraShort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://acme.atlassian.net/browse/PROJ-123", "PROJ-123"},
		{"https://acme.atlassian.net/browse/AB_C-9?x=1", "AB_C-9"},
		{"https://acme.atlassian.net/browse/PROJ-123#comments", "PROJ-123"},
		{"", ""},
		{"not a url", "not a url"}, // fallback
	}
	for _, tc := range cases {
		if got := JiraShort(tc.in); got != tc.want {
			t.Errorf("JiraShort(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePR(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"github.com", "https://github.com/acme/widget/pull/3213", nil},
		{"enterprise", "https://ghe.internal.example/acme/widget/pull/3213", nil},
		{"with query", "https://github.com/acme/widget/pull/3213?files=1", nil},
		{"with fragment", "https://github.com/acme/widget/pull/3213#discussion_r1", nil},
		{"missing scheme", "github.com/acme/widget/pull/3213", ErrInvalidPR},
		{"wrong verb", "https://github.com/acme/widget/issues/3213", ErrInvalidPR},
		{"no repo", "https://github.com/acme/pull/3213", ErrInvalidPR},
		{"non-numeric id", "https://github.com/acme/widget/pull/abc", ErrInvalidPR},
		{"trailing path", "https://github.com/acme/widget/pull/3213/files", ErrInvalidPR},
		{"empty", "", ErrInvalidPR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidatePR(tc.in)
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("ValidatePR(%q): got %v, want %v", tc.in, got, tc.wantErr)
			}
		})
	}
}

func TestPRShort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/acme/widget/pull/3213", "acme/widget#3213"},
		{"https://ghe.internal.example/acme/widget/pull/7", "acme/widget#7"},
		{"https://github.com/acme/widget/pull/3213?files=1", "acme/widget#3213"},
		{"", ""},
		{"not a url", "not a url"}, // fallback
	}
	for _, tc := range cases {
		if got := PRShort(tc.in); got != tc.want {
			t.Errorf("PRShort(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}
