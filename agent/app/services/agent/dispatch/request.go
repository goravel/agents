// Package dispatch defines the request handed to an agent dispatcher.
//
// The type lives in its own package so the generated mocks, which live outside
// the service package, do not have to import the service package: the service's
// in-package tests import those mocks, so that import would be a cycle.
package dispatch

// Request is one agent invocation: the directory to run in, which paseo
// project the workspace belongs to, which agent, the prompt, and the run title.
type Request struct {
	// Dir is the paseo project's folder: the workspace points at it and paseo
	// starts the primary agent there. It holds no checkout; the run's `setup`
	// subagent clones the repository inside it and creates the run's worktree.
	Dir string
	// Project is the paseo project id the run workspace is created in. Required:
	// the dispatcher refuses a run without one, because `--project` is always
	// sent.
	Project string
	Agent   string
	Prompt  string
	// Title is the paseo title for the run: the agent title in `paseo ls` and,
	// through the workspace the dispatcher creates, the session name in
	// `paseo workspace ls`. The service always supplies one.
	Title string
}
