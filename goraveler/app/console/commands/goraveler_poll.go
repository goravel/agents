package commands

import (
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"goravel/app/services/goraveler"
)

// GoravelerPoll polls the relay for @goraveler mentions and runs agents.
type GoravelerPoll struct{}

// Signature returns the command signature.
func (c *GoravelerPoll) Signature() string {
	return "goraveler:poll"
}

// Description returns the command description.
func (c *GoravelerPoll) Description() string {
	return "Poll the relay for @goraveler mentions and dispatch agents"
}

// Extend returns the command extensions.
func (c *GoravelerPoll) Extend() command.Extend {
	return command.Extend{}
}

// Handle runs one poll cycle.
func (c *GoravelerPoll) Handle(ctx console.Context) error {
	goraveler.NewService().Poll(ctx)

	return nil
}
