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
	"fmt"
	"regexp"
	"strings"

	gcrname "github.com/google/go-containerregistry/pkg/name"
)

type PodFilter interface {
	Add(image string) error
	Matches(repo string, digest string, tags []string) bool
}

var _ PodFilter = (*AssetPodFilter)(nil)

type AssetPodFilter struct {
	images map[string][]string
	repos  []string
}

func NewAssetPodFilter(repos []string) PodFilter {
	return &AssetPodFilter{
		images: map[string][]string{},
		repos:  repos,
	}
}

func (a *AssetPodFilter) Add(image string) error {
	// Filter in-use image references to repositories that we are currently cleaning
	repoMatches := false
	for _, repo := range a.repos {
		if strings.HasPrefix(image, repo) {
			repoMatches = true
			break
		}
	}
	if !repoMatches {
		return nil
	}
	ref, err := gcrname.ParseReference(image)
	if err != nil {
		return err
	}
	// Add in-use image reference to map with repo as string and digest/tag as values
	repo, exists := a.images[ref.Context().String()]
	if !exists {
		repo = []string{}
	}
	repo = append(repo, ref.Identifier())
	a.images[ref.Context().String()] = repo
	return nil
}

func (a *AssetPodFilter) Matches(repo string, digest string, tags []string) bool {
	if repoMatch, repoMatches := a.images[repo]; repoMatches {
		for _, identifier := range repoMatch {
			if identifier == "" {
				continue
			}
			if strings.HasPrefix(digest, identifier) {
				return true
			}
			for _, tag := range tags {
				if identifier == tag {
					return true
				}
			}
		}
	}
	return false
}

// ItemFilter is an interface which defines whether a a given string matches
// the filter.
type ItemFilter interface {
	Name() string
	Matches(s []string) bool
}

// BuildItemFilter builds and compiles a new filter for the given inputs. All
// inputs are strings to be compiled to regular expressions and are mutually
// exclusive.
func BuildItemFilter(any, all string) (ItemFilter, error) {
	// Ensure only one tag filter type is given.
	if any != "" && all != "" {
		return nil, fmt.Errorf("only one tag filter type may be specified")
	}

	switch {
	case any != "":
		re, err := regexp.Compile(any)
		fmt.Println(`regex expression any: `, re)
		if err != nil {
			return nil, fmt.Errorf("failed to compile 'any' item filter regular expression %q: %w", any, err)
		}
		return &ItemFilterAny{re}, nil
	case all != "":
		re, err := regexp.Compile(all)
		fmt.Println(`regex expression all: `, re)
		if err != nil {
			return nil, fmt.Errorf("failed to compile 'all' item filter regular expression %q: %w", all, err)
		}
		return &ItemFilterAll{re}, nil
	default:
		// If no filters were provided, return the null filter which just returns
		// false for all matches.
		return &ItemFilterNull{}, nil
	}
}

var _ ItemFilter = (*ItemFilterNull)(nil)

// ItemFilterNull always returns false.
type ItemFilterNull struct{}

func (f *ItemFilterNull) Matches(tags []string) bool {
	return false
}

func (f *ItemFilterNull) Name() string {
	return "(none)"
}

// ItemFilterAny filters based on the entire list. If any item in the list
// matches, it returns true. If no items match, it returns false.
type ItemFilterAny struct {
	re *regexp.Regexp
}

func (f *ItemFilterAny) Matches(tags []string) bool {
	if f.re == nil {
		return false
	}
	for _, t := range tags {
		if f.re.MatchString(t) {
			return true
		}
	}
	return false
}

func (f *ItemFilterAny) Name() string {
	return fmt.Sprintf("any(%s)", f.re.String())
}

var _ ItemFilter = (*ItemFilterAll)(nil)

// ItemFilterAll filters based on the entire list. If all items in the last match,
// it returns true. If one more more items do not match, it returns false.
type ItemFilterAll struct {
	re *regexp.Regexp
}

func (f *ItemFilterAll) Name() string {
	return fmt.Sprintf("all(%s)", f.re.String())
}

func (f *ItemFilterAll) Matches(tags []string) bool {
	if f.re == nil {
		return false
	}
	for _, t := range tags {
		if !f.re.MatchString(t) {
			return false
		}
	}
	return true
}
