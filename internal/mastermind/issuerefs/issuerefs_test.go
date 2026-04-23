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
