// Copyright 2021 The GCR Cleaner Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gcrcleaner

import (
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"

	gcrgoogle "github.com/google/go-containerregistry/pkg/v1/google"
)

func TestBuildTagFilter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		any, all string
		err      bool
		exp      reflect.Type
	}{
		{
			name: "empty",
			any:  "",
			all:  "",
			exp:  reflect.TypeOf(&ItemFilterNull{}),
		},
		{
			name: "any_all",
			any:  "b",
			all:  "c",
			err:  true,
		},
		{
			name: "any",
			any:  "a",
			all:  "",
			exp:  reflect.TypeOf(&ItemFilterAny{}),
		},
		{
			name: "all",
			any:  "",
			all:  "a",
			exp:  reflect.TypeOf(&ItemFilterAll{}),
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, err := BuildItemFilter(tc.any, tc.all)
			if (err != nil) != tc.err {
				t.Fatal(err)
			}
			if got, want := reflect.TypeOf(f), tc.exp; got != want {
				t.Errorf("expected %v to be %v", got, want)
			}
		})
	}
}

func TestTagFilterAny_Matches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		re   *regexp.Regexp
		tags []string
		exp  bool
	}{
		{
			name: "empty_re",
			re:   nil,
			tags: nil,
			exp:  false,
		},
		{
			name: "empty_tags",
			re:   regexp.MustCompile(`.*`),
			tags: nil,
			exp:  false,
		},
		{
			name: "matches_first",
			re:   regexp.MustCompile(`^tag1$`),
			tags: []string{"tag1", "tag2", "tag3"},
			exp:  true,
		},
		{
			name: "matches_middle",
			re:   regexp.MustCompile(`^tag2$`),
			tags: []string{"tag1", "tag2", "tag3"},
			exp:  true,
		},
		{
			name: "matches_end",
			re:   regexp.MustCompile(`^tag3$`),
			tags: []string{"tag1", "tag2", "tag3"},
			exp:  true,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &ItemFilterAny{re: tc.re}
			if got, want := f.Matches(tc.tags), tc.exp; got != want {
				t.Errorf("expected %q matches %q to be %t", tc.re, tc.tags, want)
			}
		})
	}
}

func TestTagFilterAll_Matches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		re   *regexp.Regexp
		tags []string
		exp  bool
	}{
		{
			name: "empty_re",
			re:   nil,
			tags: nil,
			exp:  false,
		},
		{
			name: "empty_tags",
			re:   regexp.MustCompile(`.*`),
			tags: nil,
			exp:  true,
		},
		{
			name: "matches_one",
			re:   regexp.MustCompile(`^tag1$`),
			tags: []string{"tag1"},
			exp:  true,
		},
		{
			name: "matches_two",
			re:   regexp.MustCompile(`^tag1|tag2$`),
			tags: []string{"tag1", "tag2"},
			exp:  true,
		},
		{
			name: "does_not_match_all",
			re:   regexp.MustCompile(`^tag1|tag2$`),
			tags: []string{"tag1", "tag2", "tag3"},
			exp:  false,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &ItemFilterAll{re: tc.re}
			if got, want := f.Matches(tc.tags), tc.exp; got != want {
				t.Errorf("expected %q matches %q to be %t", tc.re, tc.tags, want)
			}
		})
	}
}

func TestRepoSkipFilter_Matches(t *testing.T) {
	t.Parallel()
	repoPattern := "^sample-repo-name.*"

	// Create the filter using the BuildItemFilter function
	repoSkipFilter, err := BuildItemFilter(repoPattern, "")
	if err != nil {
		t.Fatalf("Error creating repoSkipFilter: %s", err)
	}

	// Verify that the filter is of the expected type (ItemFilter)
	filter, ok := repoSkipFilter.(ItemFilter)
	if !ok {
		t.Fatalf("Expected repoSkipFilter to be an ItemFilter")
	}

	cases := []struct {
		name     string
		input    []string
		expected bool
	}{
		{
			name:     "Matches with exact repo name",
			input:    []string{"sample-repo-name"},
			expected: true,
		},
		{
			name:     "Matches with repo name and additional characters",
			input:    []string{"sample-repo-name-extra"},
			expected: true,
		},
		{
			name:     "Does not match with a different repo name",
			input:    []string{"another-repo-name"},
			expected: false,
		},
		{
			name:     "Does not match with empty input",
			input:    []string{""},
			expected: false,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			actual := filter.Matches(test.input)
			if actual != test.expected {
				t.Errorf("Expected Matches(%v) to be %t, but got %t", test.input, test.expected, actual)
			}
		})
	}
}

type mockManifest struct {
	Repo   string
	Digest string
	Info   gcrgoogle.ManifestInfo
}

type mockFilter struct {
	str   string
	Items []string
}

func (f mockFilter) Name() string {
	return "mockFilter"
}

func (f mockFilter) Matches(items []string) bool {
	return false
}

type mockPodFilter struct{}

func (f mockPodFilter) Name() string {
	return "mockPodFilter"
}

func (f mockPodFilter) Matches(repo string, digest string, items []string) bool {
	return false
}

func (f mockPodFilter) Add(item string) error {
	return nil
}

func TestShouldDelete(t *testing.T) {
	since := time.Date(2023, time.November, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		description      string
		manifest         manifest
		expectedToDelete bool
	}{
		{
			description: "Should delete when manifest is older with matching repo and tag filter",
			manifest: manifest{
				Repo:   "gcr.io/example/repo",
				Digest: "digest1",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.October, 1, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"delete-1"},
				},
			},
			expectedToDelete: true,
		},
		{
			description: "Should not delete when manifest is too new",
			manifest: manifest{
				Repo:   "gcr.io/example/repo",
				Digest: "digest2",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.November, 5, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.November, 16, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"main2"},
				},
			},
			expectedToDelete: false,
		},
		{

			description: "Should not delete when manifest is too new with matching repo and tag filter",
			manifest: manifest{
				Repo:   "gcr.io/example/repo",
				Digest: "digest3",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.November, 5, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.November, 16, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"main3"},
				},
			},
			expectedToDelete: false,
		},
		{
			description: "Should not delete when manifest is older but does not match repo filter",
			manifest: manifest{
				Repo:   "gcr.io/other-repo",
				Digest: "digest4",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.October, 1, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"main4"},
				},
			},
			expectedToDelete: false,
		},
		{
			description: "Should not delete when manifest is older but does not match tag filter",
			manifest: manifest{
				Repo:   "gcr.io/example/repo",
				Digest: "digest5",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.October, 1, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"other-tag"},
				},
			},
			expectedToDelete: false,
		},
		{
			description: "Should not delete when manifest is older but does not match repo",
			manifest: manifest{
				Repo:   "gcr.io/other-repo",
				Digest: "digest6",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.October, 1, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"other-tag"},
				},
			},
			expectedToDelete: false,
		},
		{
			description: "Should delete when manifest is older with matching repo prefix and tag filter",
			manifest: manifest{
				Repo:   "gcr.io/example/repo",
				Digest: "digest7",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.October, 1, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"delete7"},
				},
			},
			expectedToDelete: true,
		},
		{
			description: "Should delete when manifest is older with matching repo and not tag keep filter",
			manifest: manifest{
				Repo:   "gcr.io/example/repo",
				Digest: "digest8",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.October, 1, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"delete8"},
				},
			},
			expectedToDelete: true,
		},
		{
			description: "Should not delete when manifest is older with matching repo and tag keep filter",
			manifest: manifest{
				Repo:   "gcr.io/example/repo",
				Digest: "digest9",
				Info: gcrgoogle.ManifestInfo{
					Created:  time.Date(2023, time.October, 1, 0, 0, 0, 0, time.UTC),
					Uploaded: time.Date(2023, time.October, 10, 0, 0, 0, 0, time.UTC),
					Tags:     []string{"main9"},
				},
			},
			expectedToDelete: false,
		},
	}

	repoPrefixFilter, err := BuildItemFilter(`^gcr.io.example.*`, "")
	if err != nil {
		t.Fatalf("Error creating repoPrefixFilter: %s", err)
	}

	tagFilter, err := BuildItemFilter("delete.*", "")
	if err != nil {
		t.Fatalf("Error creating tagFilter: %s", err)
	}

	repoSkipFilter, err := BuildItemFilter("other-repo", "")
	if err != nil {
		t.Fatalf("Error creating repoSkipFilter: %s", err)
	}

	tagKeepFilter, err := BuildItemFilter("^main.*", "")
	if err != nil {
		t.Fatalf("Error creating tagKeepFilter: %s", err)
	}

	mockPodFilter := mockPodFilter{}
	var (
		stdout = os.Stdout
		stderr = os.Stderr
	)

	for _, test := range tests {
		logger := NewLogger("debug", stderr, stdout)
		cleaner := &Cleaner{
			logger: logger,
		} // Initialize your Cleaner instance here
		actualToDelete := cleaner.shouldDelete(
			&test.manifest,
			since,
			repoSkipFilter,   // repoSkipFilter
			repoPrefixFilter, // repoPrefixFilter
			tagFilter,        // tagFilter
			tagKeepFilter,    // tagKeepFilter
			mockPodFilter,    // podFilter
		)

		if actualToDelete != test.expectedToDelete {
			t.Errorf("%s: Expected deletion=%v, but got deletion=%v", test.description, test.expectedToDelete, actualToDelete)
		}
	}
}
