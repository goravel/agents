// Package dispatch defines the request handed to an agent dispatcher.
//
// The type lives in its own package so the generated mocks, which live outside
// the service package, do not have to import the service package: the service's
// in-package tests import those mocks, so that import would be a cycle.
package dispatch

// Request is one agent invocation: which directory to run in, which agent, the
// prompt, and the title paseo shows for the run.
type Request struct {
	Dir    string
	Agent  string
	Prompt string
	// Title is the paseo agent title. Empty means paseo names the run itself.
	Title string
}
