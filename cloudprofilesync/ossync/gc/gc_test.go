// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package gc

import (
	"testing"
)

func TestSplitKeppelRepository(t *testing.T) {
	tests := []struct {
		name        string
		repository  string
		wantAccount string
		wantRepo    string
		wantErr     bool
	}{
		{
			name:        "valid repository",
			repository:  "myaccount/myrepo",
			wantAccount: "myaccount",
			wantRepo:    "myrepo",
		},
		{
			name:        "valid repository with nested path",
			repository:  "myaccount/my/nested/repo",
			wantAccount: "myaccount",
			wantRepo:    "my/nested/repo",
		},
		{
			name:       "missing separator",
			repository: "noslash",
			wantErr:    true,
		},
		{
			name:       "empty account",
			repository: "/myrepo",
			wantErr:    true,
		},
		{
			name:       "empty repo",
			repository: "myaccount/",
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			account, repo, err := splitKeppelRepository(tc.repository)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got account=%q repo=%q", account, repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if account != tc.wantAccount {
				t.Errorf("account: got %q, want %q", account, tc.wantAccount)
			}
			if repo != tc.wantRepo {
				t.Errorf("repo: got %q, want %q", repo, tc.wantRepo)
			}
		})
	}
}

func TestNormalizeTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1.0.0", "1.0.0"},
		{"1.0.0_rc1", "1.0.0+rc1"},
		{"no_underscores_here", "no+underscores+here"},
	}
	for _, tc := range tests {
		got := NormalizeTag(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeTag(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
