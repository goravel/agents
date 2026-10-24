package agent

import (
	"context"
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
)

// Reactor acknowledges a mention by reacting to the comment that carried it.
type Reactor interface {
	// React adds an eyes reaction to a comment. repository is a bare owner/name,
	// collection is reactionCollectionIssue or reactionCollectionReview, and
	// commentID is the GitHub comment id.
	React(ctx context.Context, repository, collection string, commentID int64) error
}

// githubReactor adds reactions through the typed GitHub REST client.
type githubReactor struct {
	// client is the GitHub REST client; nil when reacting is disabled.
	client *github.Client
}

// newGitHubReactor returns a GitHub-API-backed reactor. A blank token disables
// reacting (nil client) so the poller still runs agents without credentials.
//
// baseURL is for tests only: an empty string keeps go-github's built-in default
// (https://api.github.com/), which is what production uses. Tests pass an
// httptest server URL to capture the request.
func newGitHubReactor(token string, httpClient *http.Client, baseURL string) (*githubReactor, error) {
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

	return &githubReactor{client: client}, nil
}

// React adds an eyes reaction to the comment. Adding a reaction that already
// exists is a no-op on GitHub's side, so a replayed mention is harmless.
func (r *githubReactor) React(ctx context.Context, repository, collection string, commentID int64) error {
	if r.client == nil {
		return nil
	}

	owner, repo, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
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
