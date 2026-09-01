// Command ical-cli reads, writes and edits calendar events at a remote
// iCalendar or CalDAV URL.
package main

import (
	"os"

	"github.com/prdlk/ical-cli/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
