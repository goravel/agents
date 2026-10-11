package goraveler

import (
	"strconv"
	"strings"
)

// prompt builds the instruction passed to the agent, including the trusted
// context the agent needs to act on the event. The fixed context lines are
// emitted before the untrusted instruction so a commenter cannot spoof them.
// This is the event-to-agent contract: the poller decides when to use it and
// the dispatcher decides how to run it.
func (e *event) prompt(instruction string) string {
	payload, _ := e.payload()

	var builder strings.Builder
	builder.WriteString("Repository: ")
	builder.WriteString(e.Repository)
	builder.WriteString("\n")
	builder.WriteString("Event: ")
	builder.WriteString(e.Event)
	builder.WriteString("\n")
	if payload.Comment.ID != 0 {
		builder.WriteString("Comment ID: ")
		builder.WriteString(strconv.FormatInt(payload.Comment.ID, 10))
		builder.WriteString("\n")
	}
	builder.WriteString("Number: #")
	builder.WriteString(strconv.Itoa(e.target()))
	builder.WriteString("\n\nInstruction:\n")
	builder.WriteString(instruction)

	return builder.String()
}
