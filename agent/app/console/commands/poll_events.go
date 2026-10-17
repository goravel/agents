package commands

import (
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"goravel/app/services/agent"
)

// PollEvents polls the relay for @goravel-agent mentions and runs agents.
type PollEvents struct{}

// Signature returns the command signature.
func (c *PollEvents) Signature() string {
	return "poll-events"
}

// Description returns the command description.
func (c *PollEvents) Description() string {
	return "Poll the relay for @goravel-agent mentions and dispatch agents"
}

// Extend returns the command extensions.
func (c *PollEvents) Extend() command.Extend {
	return command.Extend{}
}

// Handle runs one poll cycle.
func (c *PollEvents) Handle(ctx console.Context) error {
	service, err := agent.NewService()
	if err != nil {
		return err
	}

	service.Poll(ctx)

	return nil
}
