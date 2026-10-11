package bootstrap

import (
	"github.com/goravel/framework/contracts/schedule"

	"goravel/app/facades"
)

// Schedules returns the application schedule events.
func Schedules() []schedule.Event {
	return []schedule.Event{
		facades.Schedule().
			Command("goraveler:poll").
			EveryFiveMinutes().
			SkipIfStillRunning(),
	}
}
