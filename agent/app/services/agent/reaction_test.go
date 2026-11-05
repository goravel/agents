package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordedRequest is one request captured by the test GitHub server.
type recordedRequest struct {
	method      string
	path        string
	auth        string
	contentType string
	body        string
}

// reactionRecorder collects the requests sent to the test GitHub server.
type reactionRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (r *reactionRecorder) add(request recordedRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
}

func (r *reactionRecorder) list() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.requests...)
}

// newReactionServer starts an httptest server that records every request and
// replies with status and body. Its URL deliberately carries no trailing slash:
// the reactor must rely on go-github to normalize the base URL.
func newReactionServer(t *testing.T, status int, body string) (*reactionRecorder, *httptest.Server) {
	t.Helper()

	recorder := &reactionRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requestBody, _ := io.ReadAll(req.Body)
		recorder.add(recordedRequest{
			method:      req.Method,
			path:        req.URL.Path,
			auth:        req.Header.Get("Authorization"),
			contentType: req.Header.Get("Content-Type"),
			body:        string(requestBody),
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return recorder, server
}

// TestGitHubReactorReacts pins the REST endpoint, the bearer token and the
// request body for both comment kinds. The test server URL has no trailing
// slash, so the exact path also proves the base URL is normalized.
func TestGitHubReactorReacts(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusCreated, `{"id":1,"content":"eyes"}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL, server.URL+"/graphql")
	require.NoError(t, err)
	require.NotNil(t, reactor.client)

	tests := []struct {
		name       string
		repository string
		collection string
		commentID  int64
		wantPath   string
	}{
		{
			name:       "conversation comment",
			repository: "goravel/goravel",
			collection: reactionCollectionIssue,
			commentID:  24680,
			wantPath:   "/repos/goravel/goravel/issues/comments/24680/reactions",
		},
		{
			name:       "inline review comment",
			repository: "goravel/framework",
			collection: reactionCollectionReview,
			commentID:  98765,
			wantPath:   "/repos/goravel/framework/pulls/comments/98765/reactions",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(recorder.list())

			require.NoError(t, reactor.React(context.Background(), test.repository, test.collection, test.commentID))

			requests := recorder.list()
			require.Len(t, requests, before+1)

			got := requests[before]
			require.Equal(t, http.MethodPost, got.method)
			require.Equal(t, test.wantPath, got.path)
			require.Equal(t, "Bearer test-token", got.auth)
			require.JSONEq(t, `{"content":"eyes"}`, got.body)
		})
	}
}

// TestGitHubReactorBlankToken pins that a blank GITHUB_TOKEN disables reacting
// without failing: no client is built and no request is sent.
func TestGitHubReactorBlankToken(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusCreated, `{"id":1,"content":"eyes"}`)

	reactor, err := newGitHubReactor("", server.Client(), server.URL, server.URL+"/graphql")
	require.NoError(t, err)
	require.Nil(t, reactor.client)

	require.NoError(t, reactor.React(context.Background(), "goravel/goravel", reactionCollectionIssue, 24680))
	require.NoError(t, reactor.ReactToReview(context.Background(), "goravel/example", "MDE3OlB1bGxSZXF1ZXN0UmV2aWV3ODA="))
	require.Empty(t, recorder.list())
}

// TestGitHubReactorAPIError pins that a non-2xx reply surfaces as an error so
// the caller can log it and continue.
func TestGitHubReactorAPIError(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusNotFound, `{"message":"Not Found"}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL, server.URL+"/graphql")
	require.NoError(t, err)

	err = reactor.React(context.Background(), "goravel/goravel", reactionCollectionIssue, 24680)
	require.ErrorContains(t, err, "404")
	require.Len(t, recorder.list(), 1)
}

// TestGitHubReactorInvalidRepository pins that a malformed repository is
// rejected before any request is sent.
func TestGitHubReactorInvalidRepository(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusCreated, `{"id":1,"content":"eyes"}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL, server.URL+"/graphql")
	require.NoError(t, err)

	tests := []struct {
		name       string
		repository string
	}{
		{name: "no owner", repository: "goravel"},
		{name: "empty owner", repository: "/goravel"},
		{name: "empty name", repository: "goravel/"},
		{name: "extra segment", repository: "goravel/goravel/extra"},
		{name: "blank", repository: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := reactor.React(context.Background(), test.repository, reactionCollectionIssue, 1)
			require.ErrorContains(t, err, "invalid repository")
		})
	}

	require.Empty(t, recorder.list())
}

// TestGitHubReactorUnknownCollection pins that an unrecognised collection is
// rejected instead of silently posting to the issue-comment endpoint.
func TestGitHubReactorUnknownCollection(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusCreated, `{"id":1,"content":"eyes"}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL, server.URL+"/graphql")
	require.NoError(t, err)

	err = reactor.React(context.Background(), "goravel/goravel", "repos/comments", 24680)
	require.ErrorContains(t, err, `unknown collection "repos/comments"`)
	require.Empty(t, recorder.list())
}

// TestNewGitHubReactorRejectsInvalidBaseURL pins that a malformed base URL fails
// construction rather than an individual reaction.
func TestNewGitHubReactorRejectsInvalidBaseURL(t *testing.T) {
	_, server := newReactionServer(t, http.StatusCreated, `{"id":1,"content":"eyes"}`)

	reactor, err := newGitHubReactor("token", server.Client(), "://bad", "")
	require.Error(t, err)
	require.Nil(t, reactor)
}

// TestGitHubReactorReactsToReview pins the GraphQL call a review body needs:
// POST to /graphql, bearer auth, and the addReaction mutation with the review's
// node id as subjectId and the EYES enum value. REST has no review reaction
// endpoint, so a REST request here would be a bug.
func TestGitHubReactorReactsToReview(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusOK,
		`{"data":{"addReaction":{"reaction":{"content":"EYES"}}}}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL, server.URL+"/graphql")
	require.NoError(t, err)

	require.NoError(t, reactor.ReactToReview(
		context.Background(), "goravel/example", "MDE3OlB1bGxSZXF1ZXN0UmV2aWV3ODA="))

	requests := recorder.list()
	require.Len(t, requests, 1)
	require.Equal(t, http.MethodPost, requests[0].method)
	require.Equal(t, "/graphql", requests[0].path)
	require.Equal(t, "Bearer test-token", requests[0].auth)
	require.Equal(t, "application/json", requests[0].contentType)

	var request struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	require.NoError(t, json.Unmarshal([]byte(requests[0].body), &request))
	require.Equal(t, addReactionMutation, request.Query)
	require.Equal(t, map[string]any{
		"subjectId": "MDE3OlB1bGxSZXF1ZXN0UmV2aWV3ODA=",
		"content":   "EYES",
	}, request.Variables)
}

// TestNewGitHubReactorDefaultsGraphQLURL pins the production endpoint: an empty
// GraphQL URL must fall back to GitHub's public one, or every review reaction
// would post nowhere while the injected-URL tests stay green.
func TestNewGitHubReactorDefaultsGraphQLURL(t *testing.T) {
	reactor, err := newGitHubReactor("token", nil, "", "")
	require.NoError(t, err)
	require.Equal(t, defaultGraphQLURL, reactor.graphqlURL)
}

// TestGitHubReactorReviewErrors pins that both failure shapes surface: a GraphQL
// rejection, which arrives as HTTP 200 with an errors array, and a transport
// level status.
func TestGitHubReactorReviewErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			name:    "graphql errors",
			status:  http.StatusOK,
			body:    `{"errors":[{"message":"Could not resolve to a node"}]}`,
			wantErr: "Could not resolve to a node",
		},
		{
			name:    "http error",
			status:  http.StatusUnauthorized,
			body:    `{"message":"Bad credentials"}`,
			wantErr: "401",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, server := newReactionServer(t, test.status, test.body)

			reactor, err := newGitHubReactor("test-token", server.Client(), server.URL, server.URL+"/graphql")
			require.NoError(t, err)

			err = reactor.ReactToReview(context.Background(), "goravel/example", "MDE3OlB1bGxSZXF1ZXN0UmV2aWV3ODA=")
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

// TestGitHubReactorReviewInvalidRepository pins that a malformed repository is
// rejected before any request is sent.
func TestGitHubReactorReviewInvalidRepository(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusOK,
		`{"data":{"addReaction":{"reaction":{"content":"EYES"}}}}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL, server.URL+"/graphql")
	require.NoError(t, err)

	tests := []struct {
		name       string
		repository string
	}{
		{name: "no owner", repository: "goravel"},
		{name: "empty owner", repository: "/goravel"},
		{name: "empty name", repository: "goravel/"},
		{name: "extra segment", repository: "goravel/goravel/extra"},
		{name: "blank", repository: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := reactor.ReactToReview(context.Background(), test.repository, "MDE3OlB1bGxSZXF1ZXN0UmV2aWV3ODA=")
			require.ErrorContains(t, err, "invalid repository")
		})
	}

	err = reactor.ReactToReview(context.Background(), "goravel/example", "")
	require.ErrorContains(t, err, "no node id")
	require.Empty(t, recorder.list())
}
