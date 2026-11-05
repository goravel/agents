package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-github/v89/github"
)

const (
	// reactionCollectionIssue is the REST collection for issue and pull request
	// conversation comments.
	reactionCollectionIssue = "issues/comments"
	// reactionCollectionReview is the REST collection for inline review comments.
	reactionCollectionReview = "pulls/comments"
	// reactionContentEyes is the GitHub reaction content for the 👀 reaction.
	reactionContentEyes = "eyes"
	// reactionContentEyesGraphQL is the same 👀 reaction as a GraphQL
	// ReactionContent enum value; the REST spelling is rejected there.
	reactionContentEyesGraphQL = "EYES"
	// defaultGraphQLURL is GitHub's GraphQL endpoint, the only API where a pull
	// request review is Reactable.
	defaultGraphQLURL = "https://api.github.com/graphql"
	// addReactionMutation adds one reaction to a Reactable subject. The content
	// is a variable so the mutation can add any ReactionContent.
	addReactionMutation = `mutation($subjectId: ID!, $content: ReactionContent!) {
  addReaction(input: {subjectId: $subjectId, content: $content}) {
    reaction { content }
  }
}`
)

// Reactor acknowledges a mention by reacting to the text that carried it.
type Reactor interface {
	// React adds an eyes reaction to a comment. repository is a bare owner/name,
	// collection is reactionCollectionIssue or reactionCollectionReview, and
	// commentID is the GitHub comment id.
	React(ctx context.Context, repository, collection string, commentID int64) error
	// ReactToReview adds an eyes reaction to a review body. GitHub exposes
	// PullRequestReview as Reactable in GraphQL only — the REST API has no review
	// reaction endpoint — so nodeID is the review's GraphQL node id (review
	// .node_id in the webhook payload).
	ReactToReview(ctx context.Context, repository, nodeID string) error
}

// githubReactor adds reactions through the typed GitHub REST client.
type githubReactor struct {
	// client is the GitHub REST client; nil when reacting is disabled.
	client *github.Client
	// graphqlURL is the GraphQL endpoint; empty keeps the public default.
	graphqlURL string
}

// newGitHubReactor returns a GitHub-API-backed reactor. A blank token disables
// reacting (nil client) so the poller still runs agents without credentials.
//
// baseURL and graphqlURL are for tests only: an empty string keeps go-github's
// built-in default (https://api.github.com/) and GitHub's public GraphQL
// endpoint, which is what production uses. Tests pass an httptest server URL to
// capture the request.
func newGitHubReactor(token string, httpClient *http.Client, baseURL, graphqlURL string) (*githubReactor, error) {
	if token == "" {
		return &githubReactor{}, nil
	}

	options := []github.ClientOptionsFunc{github.WithAuthToken(token)}
	if httpClient != nil {
		options = append(options, github.WithHTTPClient(httpClient))
	}
	if baseURL != "" {
		// go-github resolves every request path against the base URL and needs a
		// trailing slash; WithURLs normalizes it through parseURL.
		options = append(options, github.WithURLs(&baseURL, nil))
	}

	client, err := github.NewClient(options...)
	if err != nil {
		return nil, fmt.Errorf("agent: create GitHub client: %w", err)
	}

	if graphqlURL == "" {
		graphqlURL = defaultGraphQLURL
	}

	return &githubReactor{client: client, graphqlURL: graphqlURL}, nil
}

// repositoryParts validates a bare owner/name and returns its parts.
func repositoryParts(repository string) (owner, repo string, ok bool) {
	owner, repo, ok = strings.Cut(repository, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}

	return owner, repo, true
}

// React adds an eyes reaction to the comment. Adding a reaction that already
// exists is a no-op on GitHub's side, so a replayed mention is harmless.
func (r *githubReactor) React(ctx context.Context, repository, collection string, commentID int64) error {
	if r.client == nil {
		return nil
	}

	owner, repo, ok := repositoryParts(repository)
	if !ok {
		return fmt.Errorf("react to comment %d: invalid repository %q", commentID, repository)
	}

	var err error
	switch collection {
	case reactionCollectionIssue:
		// Conversation comment: repos/{owner}/{repo}/issues/comments/{id}/reactions.
		// CreateCommentReaction is NOT this call: it targets commit comments.
		_, _, err = r.client.Reactions.CreateIssueCommentReaction(
			ctx, owner, repo, commentID, reactionContentEyes)
	case reactionCollectionReview:
		// Inline review comment: repos/{owner}/{repo}/pulls/comments/{id}/reactions.
		_, _, err = r.client.Reactions.CreatePullRequestCommentReaction(
			ctx, owner, repo, commentID, reactionContentEyes)
	default:
		// Reject rather than guess: a wrong collection silently posts a reaction
		// to the wrong REST endpoint, which only shows up as a 404 in the logs.
		return fmt.Errorf("react to comment %d: unknown collection %q", commentID, collection)
	}
	if err != nil {
		return fmt.Errorf("react to %s comment %d: %w", repository, commentID, err)
	}

	return nil
}

// ReactToReview adds the eyes reaction to a review body. GitHub exposes
// PullRequestReview as Reactable in GraphQL only, so the call is one POST to the
// GraphQL endpoint through the client's authenticated HTTP client — go-github
// speaks REST only, and this needs no new dependency. The repository is carried
// for the error message; the node id alone identifies the subject. Adding a
// reaction that already exists returns the existing reaction, so a replayed
// mention is harmless.
func (r *githubReactor) ReactToReview(ctx context.Context, repository, nodeID string) error {
	if r.client == nil {
		return nil
	}
	if _, _, ok := repositoryParts(repository); !ok {
		return fmt.Errorf("react to review %s: invalid repository %q", nodeID, repository)
	}
	if nodeID == "" {
		return fmt.Errorf("react to review in %q: no node id", repository)
	}

	prefix := fmt.Sprintf("react to %s review %s", repository, nodeID)

	body, err := json.Marshal(graphqlRequest{
		Query: addReactionMutation,
		Variables: map[string]any{
			"subjectId": nodeID,
			"content":   reactionContentEyesGraphQL,
		},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := r.client.Client().Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	defer func() { _ = response.Body.Close() }()

	// GitHub answers a rejected mutation with 200 and a non-empty errors array,
	// so the body is decoded, not only the status read.
	var result graphqlResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("%s: decode response (status %d): %w", prefix, response.StatusCode, err)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("%s: status %d", prefix, response.StatusCode)
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("%s: %s", prefix, result.Errors[0].Message)
	}

	return nil
}

// graphqlRequest is the body of a GitHub GraphQL call.
type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// graphqlResponse is the part of an addReaction reply the reactor reads: the
// errors GitHub returns for a rejection. Success is a 2xx with no errors, which
// is also what a replayed reaction answers.
type graphqlResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}
