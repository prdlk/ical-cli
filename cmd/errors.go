package cmd

import (
	"fmt"
	"time"
)

// errFlagConflict reports two flags that cannot be combined.
func errFlagConflict(a, b string) error {
	return fmt.Errorf("%s cannot be combined with %s", a, b)
}

// errWindow reports an empty or inverted query window.
func errWindow(from, to time.Time) error {
	return fmt.Errorf("window end %s is not after window start %s",
		to.Format(time.RFC3339), from.Format(time.RFC3339))
}
