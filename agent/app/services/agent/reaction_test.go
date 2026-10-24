package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordedRequest is one request captured by the test GitHub server.
type recordedRequest struct {
	method string
	path   string
	auth   string
	body   string
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
			method: req.Method,
			path:   req.URL.Path,
			auth:   req.Header.Get("Authorization"),
			body:   string(requestBody),
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

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL)
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

	reactor, err := newGitHubReactor("", server.Client(), server.URL)
	require.NoError(t, err)
	require.Nil(t, reactor.client)

	require.NoError(t, reactor.React(context.Background(), "goravel/goravel", reactionCollectionIssue, 24680))
	require.Empty(t, recorder.list())
}

// TestGitHubReactorAPIError pins that a non-2xx reply surfaces as an error so
// the caller can log it and continue.
func TestGitHubReactorAPIError(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusNotFound, `{"message":"Not Found"}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL)
	require.NoError(t, err)

	err = reactor.React(context.Background(), "goravel/goravel", reactionCollectionIssue, 24680)
	require.ErrorContains(t, err, "404")
	require.Len(t, recorder.list(), 1)
}

// TestGitHubReactorInvalidRepository pins that a malformed repository is
// rejected before any request is sent.
func TestGitHubReactorInvalidRepository(t *testing.T) {
	recorder, server := newReactionServer(t, http.StatusCreated, `{"id":1,"content":"eyes"}`)

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL)
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

	reactor, err := newGitHubReactor("test-token", server.Client(), server.URL)
	require.NoError(t, err)

	err = reactor.React(context.Background(), "goravel/goravel", "repos/comments", 24680)
	require.ErrorContains(t, err, `unknown collection "repos/comments"`)
	require.Empty(t, recorder.list())
}

// TestNewGitHubReactorRejectsInvalidBaseURL pins that a malformed base URL fails
// construction rather than an individual reaction.
func TestNewGitHubReactorRejectsInvalidBaseURL(t *testing.T) {
	_, server := newReactionServer(t, http.StatusCreated, `{"id":1,"content":"eyes"}`)

	reactor, err := newGitHubReactor("token", server.Client(), "://bad")
	require.Error(t, err)
	require.Nil(t, reactor)
}
