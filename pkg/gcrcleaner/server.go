// Copyright 2019 The GCR Cleaner Authors
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"

	"github.com/GoogleCloudPlatform/gcr-cleaner/internal/version"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iterator"
)

const (
	contentTypeHeader = "Content-Type"
	contentTypeJSON   = "application/json"
)

// Server is a cleaning server.
type Server struct {
	cleaner *Cleaner
	logger  *Logger
}

// NewServer creates a new server for handler functions.
func NewServer(cleaner *Cleaner) (*Server, error) {
	if cleaner == nil {
		return nil, fmt.Errorf("missing cleaner")
	}

	return &Server{
		cleaner: cleaner,
		logger:  cleaner.logger,
	}, nil
}

// PubSubHandler is an http handler that invokes the cleaner from a pubsub
// request. Unlike an HTTP request, the pubsub endpoint always returns a success
// unless the pubsub message is malformed.
func (s *Server) PubSubHandler(cache Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m pubsubMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			err = fmt.Errorf("failed to decode pubsub message: %w", err)
			s.handleError(w, err, 400)
			return
		}

		// PubSub is "at least once" delivery. The cleaner is idempotent, but
		// let's try to prevent unnecessary work by not processing messages we've
		// already received.
		msgID := m.Subscription + "/" + m.Message.ID
		if exists := cache.Insert(msgID); exists {
			s.logger.Info("already processed message", "id", msgID)
			w.WriteHeader(204)
			return
		}

		if len(m.Message.Data) == 0 {
			err := fmt.Errorf("missing data in pubsub payload")
			s.handleError(w, err, 400)
			return
		}

		// Start a goroutine to delete the images
		body := io.NopCloser(bytes.NewReader(m.Message.Data))
		go func() {
			// Intentionally don't use the request context, since it terminates but
			// the background job should still be processing.
			ctx := context.Background()
			if _, _, err := s.clean(ctx, body); err != nil {
				s.logger.Error("failed to clean", "error", err)
			}
		}()

		w.WriteHeader(204)
	}
}

// HTTPHandler is an http handler that invokes the cleaner with the given
// parameters.
func (s *Server) HTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		deleted, status, err := s.clean(ctx, r.Body)
		if err != nil {
			s.handleError(w, err, status)
			return
		}

		refs := make([]string, 0, 16)
		for _, v := range deleted {
			refs = append(refs, v...)
		}
		sort.Strings(refs)

		b, err := json.Marshal(&cleanResp{
			Count:      len(deleted),
			Refs:       refs,
			RefsByRepo: deleted,
		})
		if err != nil {
			err = fmt.Errorf("failed to marshal JSON errors: %w", err)
			s.handleError(w, err, 500)
			return
		}

		w.WriteHeader(200)
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		fmt.Fprint(w, string(b))
	}
}

// clean reads the given body as JSON and starts a cleaner instance.
func (s *Server) clean(ctx context.Context, r io.ReadCloser) (map[string][]string, int, error) {
	var p Payload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return nil, 500, fmt.Errorf("failed to decode payload as JSON: %w", err)
	}

	s.logger.Info("starting clean request",
		"version", version.HumanVersion,
		"payload", p)

	// Convert duration to a negative value, since we're about to "add" it to the
	// since time.
	sub := time.Duration(p.Grace)
	if p.Grace > 0 {
		sub = sub * -1
	}

	since := time.Now().UTC().Add(sub)
	repoKeepFilter, err := BuildItemFilter(p.RepoKeepFilterAny, "")
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("failed to build repo keep filter: %w", err)
	}
	s.logger.Debug("server: created repo keep filter", "filter", p.RepoKeepFilterAny)

	repoPrefixFilter, err := BuildItemFilter(p.RepoMatchPrefixFilter, "")
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("failed to build repo prefix filter: %w", err)
	}
	s.logger.Debug("server: created repo prefix filter", "filter", p.RepoMatchPrefixFilter)

	tagFilter, err := BuildItemFilter(p.TagFilterAny, p.TagFilterAll)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("failed to build tag filter: %w", err)
	}
	s.logger.Debug("server: created tag filter any", "filter", p.TagFilterAny)
	s.logger.Debug("server: created tag filter all", "filter", p.TagFilterAll)

	tagKeepFilter, err := BuildItemFilter(p.TagKeepAny, "")
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("failed to build tag keep filter: %w", err)
	}
	s.logger.Debug("server: created tag keep filter", "filter", p.TagKeepAny)

	// Get Project ID from Application Default Credentials
	// https://stackoverflow.com/a/50365313
	credentials, err := google.FindDefaultCredentials(ctx, cloudresourcemanager.CloudPlatformScope)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to get default credentials: %w", err)
	}

	// Gather all the repositories.
	repos := make([]string, 0, len(p.Repos))
	for _, v := range p.Repos {
		if t := strings.TrimSpace(v); t != "" {
			repos = append(repos, t)
		}
	}

	// List and collect container images from GKE pods and Cloud Run services that were seen in the past week.
	// We pull this from Cloud Asset Inventory data exported to BigQuery, because calling the CAI API directly is too slow.
	s.logger.Info("fetching recently seen container images from BigQuery...")

	podFilter := NewAssetPodFilter(repos)

	cloudAssetInventoryTableName := os.Getenv("CLOUD_ASSET_INVENTORY_TABLE_NAME")
	cloudAssetInventoryTableLocation := os.Getenv("CLOUD_ASSET_INVENTORY_TABLE_LOCATION")

	recentlySeenImagesQuery := fmt.Sprintf(`
SELECT DISTINCT JSON_VALUE(container, '$.image') as image
FROM (
  SELECT
    CASE asset_type
      WHEN "k8s.io/Pod" THEN ARRAY_CONCAT(
        JSON_QUERY_ARRAY(
          resource.data,'$.spec.containers'
        ),
        COALESCE(
          JSON_QUERY_ARRAY(
            resource.data,'$.spec.initContainers'
          ),
          []
        )
      )
      WHEN "batch.k8s.io/CronJob" THEN ARRAY_CONCAT(
        JSON_QUERY_ARRAY(
          resource.data,'$.spec.jobTemplate.spec.template.spec.containers'
        ),
        COALESCE(
          JSON_QUERY_ARRAY(
            resource.data,'$.spec.jobTemplate.spec.template.spec.initContainers'
          ),
          []
        )
      )
      WHEN "run.googleapis.com/Service" THEN JSON_QUERY_ARRAY(
        resource.data,'$.spec.template.spec.containers'
      )
      WHEN "run.googleapis.com/Job" THEN JSON_QUERY_ARRAY(
        resource.data,'$.spec.template.spec.template.spec.containers'
      )
    END
    AS containers
  FROM %s
  WHERE readTime >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 day)
), UNNEST(containers) AS container;`, cloudAssetInventoryTableName)

	bigQueryClient, err := bigquery.NewClient(ctx, credentials.ProjectID)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to create new BigQuery client: %w", err)
	}

	query := bigQueryClient.Query(recentlySeenImagesQuery)
	query.Location = cloudAssetInventoryTableLocation
	queryIterator, err := query.Read(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to get query results from BigQuery: %w", err)
	}

	for {
		var values []bigquery.Value
		err := queryIterator.Next(&values)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to read row from BigQuery: %w", err)
		}
		image, ok := values[0].(string)
		if !ok {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to parse row from BigQuery: %v", values[0])
		}
		err = podFilter.Add(image)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to parse container image: %w", err)
		}
	}

	bigQueryClient.Close()

	reposAdded := 0
	refsAdded := 0
	for _, refs := range podFilter.(*AssetPodFilter).images {
		reposAdded++
		refsAdded += len(refs)
	}
	s.logger.Info("added recently seen container images to filter", "repoCount", reposAdded, "imageRefCount", refsAdded)

	if p.Recursive {
		s.logger.Debug("gathering child repositories recursively")

		allRepos, err := s.cleaner.ListChildRepositories(ctx, repos)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("failed to list child repositories: %w", err)
		}
		s.logger.Debug("recursively listed child repositories",
			"in", repos,
			"out", allRepos)

		// This is safe because ListChildRepositories is guaranteed to include at
		// least the list repos given to it.
		repos = allRepos
	}

	s.logger.Info("deleting refs",
		"since", since,
		"repos", repos)

	// Do the deletion.
	deleted := make(map[string][]string, len(repos))
	for _, repo := range repos {
		s.logger.Info("deleting refs for repo", "repo", repo)

		childrenDeleted, err := s.cleaner.Clean(ctx, repo, since, p.Keep, repoKeepFilter, repoPrefixFilter, tagFilter, tagKeepFilter, podFilter, p.DryRun)
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("failed to clean repo %q: %w", repo, err)
		}

		if len(childrenDeleted) > 0 {
			s.logger.Info("deleted refs", "repo", repo, "refs", childrenDeleted)
			deleted[repo] = append(deleted[repo], childrenDeleted...)
		}
	}

	s.logger.Info("deleted refs", "refs", deleted, "dryRun", p.DryRun)

	return deleted, http.StatusOK, nil
}

// handleError returns a JSON-formatted error message
func (s *Server) handleError(w http.ResponseWriter, err error, status int) {
	s.logger.Error(err.Error(), "error", err)

	b, err := json.Marshal(&errorResp{Error: err.Error()})
	if err != nil {
		err = fmt.Errorf("failed to marshal JSON errors: %w", err)
		http.Error(w, err.Error(), 500)
		return
	}

	w.WriteHeader(status)
	w.Header().Set(contentTypeHeader, contentTypeJSON)
	fmt.Fprint(w, string(b))
}

// Payload is the expected incoming payload format.
type Payload struct {
	// Repos is the list of repositories to clean.
	Repos sortedStringSlice `json:"repos"`

	// Grace is a time.Duration value indicating how much grade period should be
	// given to new, untagged layers. The default is no grace.
	Grace duration `json:"grace"`

	// Keep is the minimum number of images to keep.
	Keep int64 `json:"keep"`

	// RepoKeepFilterAny is a repository pattern to keep images for. If given, any
	// image that matches this given regular expression will be kept. The image
	// will be kept even if it has other tags that do not match the given regular
	// expression.
	RepoKeepFilterAny string `json:"repo_keep_filter"`

	// RepoPrefixFilterAny is a repository pattern to delete images for. If given,
	// any image that matches this given regular expression will be deleted if the
	// tags are matched by the tag filter. This is used to target specific repositories
	// or groups of repositories for deletion.
	RepoMatchPrefixFilter string `json:"repository_match_prefix"`

	// TagFilterAny is the tags pattern to be allowed removing. If given, any
	// image with at least one tag that matches this given regular expression will
	// be deleted. The image will be deleted even if it has other tags that do not
	// match the given regular expression.
	TagFilterAny string `json:"tag_filter_any"`

	// TagFilterAll is the tags pattern to be allowed removing. If given, any
	// image where all tags match this given regular expression will be deleted.
	// The image will not be delete if it has other tags that do not match the
	// given regular expression.
	TagFilterAll string `json:"tag_filter_all"`

	//TagKeepAny is the tags pattern to be allowed keeping. If given, any
	// image with at least one tag that matches this given regular expression will
	// be kept. The image will be kept even if it has other tags that do not
	// match the given regular expression.
	TagKeepAny string `json:"tag_keep_any"`

	// DryRun instructs the server to not perform actual cleaning. The response
	// will include repositories that would have been deleted.
	DryRun bool `json:"dry_run"`

	// Recursive enables cleaning all child repositories.
	Recursive bool `json:"recursive"`
}

type pubsubMessage struct {
	Message struct {
		Data []byte `json:"data"`
		ID   string `json:"message_id"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

type cleanResp struct {
	Count      int                 `json:"count"`
	Refs       []string            `json:"refs"`
	RefsByRepo map[string][]string `json:"refs_by_repo"`
}

type errorResp struct {
	Error string `json:"error"`
}

type sortedStringSlice []string

func (s sortedStringSlice) MarshalJSON() ([]byte, error) {
	return json.Marshal([]string(s))
}

func (s *sortedStringSlice) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	m := make(map[string]struct{}, 4)

	switch val := v.(type) {
	case string:
		if t := strings.TrimSpace(val); t != "" {
			m[t] = struct{}{}
		}
	case []any:
		for i, v := range val {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("list must contain only strings (got %T at index %d)", v, i)
			}
			if t := strings.TrimSpace(s); t != "" {
				m[t] = struct{}{}
			}
		}
	case []string:
		for _, v := range val {
			if t := strings.TrimSpace(v); t != "" {
				m[t] = struct{}{}
			}
		}
	default:
		return fmt.Errorf("invalid list type %T", val)
	}

	list := make([]string, 0, len(m))
	for v := range m {
		list = append(list, v)
	}
	sort.Strings(list)
	*s = list

	return nil
}

type duration time.Duration

func (d duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	switch val := v.(type) {
	case float64:
		*d = duration(time.Duration(val))
		return nil
	case string:
		s, err := time.ParseDuration(val)
		if err != nil {
			return err
		}
		*d = duration(s)
		return nil
	default:
		return fmt.Errorf("invalid duration type %T", val)
	}
}
