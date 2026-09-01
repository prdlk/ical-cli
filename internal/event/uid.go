package event

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// UIDSuffix tags UIDs minted by this tool.
const UIDSuffix = "@ical-cli"

// NewUID mints a globally unique event identifier of the form
// "<uuid>@ical-cli", as required by RFC 5545's uniqueness guarantee.
func NewUID() string {
	return uuid.NewString() + UIDSuffix
}

// ErrNotFound reports that no candidate matched a UID query.
var ErrNotFound = errors.New("event not found")

// AmbiguousUIDError reports that a UID prefix matched more than one event. It
// carries the matches so the caller can show the user what to disambiguate
// between.
type AmbiguousUIDError struct {
	Query   string
	Matches []string
}

func (e *AmbiguousUIDError) Error() string {
	return fmt.Sprintf("uid %q is ambiguous: matches %d events: %s",
		e.Query, len(e.Matches), strings.Join(e.Matches, ", "))
}

// MatchUID resolves query against candidates. An exact match always wins over
// prefix matching, so a UID that happens to prefix another is still reachable.
// Matching is case-insensitive because UID comparison in the wild is.
//
// Returns ErrNotFound when nothing matches and *AmbiguousUIDError when a prefix
// matches several distinct UIDs.
func MatchUID(candidates []string, query string) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", fmt.Errorf("match uid: empty query")
	}

	seen := make(map[string]struct{}, len(candidates))
	uniq := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		uniq = append(uniq, c)
	}

	for _, c := range uniq {
		if strings.EqualFold(c, q) {
			return c, nil
		}
	}

	lower := strings.ToLower(q)
	var matches []string
	for _, c := range uniq {
		if strings.HasPrefix(strings.ToLower(c), lower) {
			matches = append(matches, c)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: no event with uid or prefix %q", ErrNotFound, query)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", &AmbiguousUIDError{Query: query, Matches: matches}
	}
}

// ShortUID truncates a UID for table display, preferring the portion before an
// "@" so that generated UIDs stay recognisable.
func ShortUID(uid string, width int) string {
	if width <= 0 {
		return uid
	}
	s := uid
	if i := strings.IndexByte(s, '@'); i > 0 {
		s = s[:i]
	}
	if len(s) <= width {
		return s
	}
	return s[:width]
}
