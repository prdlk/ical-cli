package event

import (
	"errors"
	"strings"
	"testing"
)

func TestMatchUID(t *testing.T) {
	t.Parallel()

	candidates := []string{
		"aaa11111-2222-3333-4444-555555555555@ical-cli",
		"aaa99999-8888-7777-6666-555555555555@ical-cli",
		"bbb11111-2222-3333-4444-555555555555@ical-cli",
		"standup@example.com",
	}

	tests := []struct {
		name          string
		candidates    []string
		query         string
		want          string
		wantErr       bool
		wantAmbiguous int // expected match count when ambiguous
		wantNotFound  bool
	}{
		{
			name:       "exact match",
			candidates: candidates,
			query:      "standup@example.com",
			want:       "standup@example.com",
		},
		{
			name:       "exact match is case insensitive",
			candidates: candidates,
			query:      "STANDUP@EXAMPLE.COM",
			want:       "standup@example.com",
		},
		{
			name:       "unique prefix",
			candidates: candidates,
			query:      "bbb",
			want:       "bbb11111-2222-3333-4444-555555555555@ical-cli",
		},
		{
			name:       "unique longer prefix",
			candidates: candidates,
			query:      "aaa99",
			want:       "aaa99999-8888-7777-6666-555555555555@ical-cli",
		},
		{
			name:       "unique prefix case insensitive",
			candidates: candidates,
			query:      "BBB",
			want:       "bbb11111-2222-3333-4444-555555555555@ical-cli",
		},
		{
			name:          "ambiguous prefix",
			candidates:    candidates,
			query:         "aaa",
			wantErr:       true,
			wantAmbiguous: 2,
		},
		{
			name:         "no match",
			candidates:   candidates,
			query:        "zzz",
			wantErr:      true,
			wantNotFound: true,
		},
		{
			name:         "empty candidate set",
			candidates:   nil,
			query:        "anything",
			wantErr:      true,
			wantNotFound: true,
		},
		{
			name:       "empty query is rejected",
			candidates: candidates,
			query:      "",
			wantErr:    true,
		},
		{
			// A UID that is a strict prefix of another must stay reachable, so
			// an exact hit has to beat prefix matching.
			name:       "exact match wins over longer candidate",
			candidates: []string{"abc", "abcdef"},
			query:      "abc",
			want:       "abc",
		},
		{
			name:       "duplicate candidates collapse",
			candidates: []string{"dup@x", "dup@x"},
			query:      "dup",
			want:       "dup@x",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := MatchUID(tc.candidates, tc.query)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("MatchUID(%q) = %q, want error", tc.query, got)
				}
				if tc.wantNotFound && !errors.Is(err, ErrNotFound) {
					t.Errorf("MatchUID(%q) error = %v, want ErrNotFound", tc.query, err)
				}
				if tc.wantAmbiguous > 0 {
					var ambiguous *AmbiguousUIDError
					if !errors.As(err, &ambiguous) {
						t.Fatalf("MatchUID(%q) error = %v, want *AmbiguousUIDError", tc.query, err)
					}
					if len(ambiguous.Matches) != tc.wantAmbiguous {
						t.Errorf("MatchUID(%q) matched %d candidates, want %d",
							tc.query, len(ambiguous.Matches), tc.wantAmbiguous)
					}
					// The message must name the candidates so the user can act.
					for _, m := range ambiguous.Matches {
						if !strings.Contains(ambiguous.Error(), m) {
							t.Errorf("error message omits candidate %q: %s", m, ambiguous.Error())
						}
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("MatchUID(%q) returned unexpected error: %v", tc.query, err)
			}
			if got != tc.want {
				t.Errorf("MatchUID(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

func TestNewUID(t *testing.T) {
	t.Parallel()

	first, second := NewUID(), NewUID()

	if !strings.HasSuffix(first, UIDSuffix) {
		t.Errorf("NewUID() = %q, want the %q suffix", first, UIDSuffix)
	}
	if first == second {
		t.Error("NewUID() returned the same value twice; UIDs must be unique")
	}
	// A UUID is 36 characters.
	if got := len(strings.TrimSuffix(first, UIDSuffix)); got != 36 {
		t.Errorf("NewUID() uuid part length = %d, want 36 (%q)", got, first)
	}
}

func TestShortUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		uid   string
		width int
		want  string
	}{
		{name: "truncates at the at sign", uid: "standup@example.com", width: 12, want: "standup"},
		{name: "truncates long local part", uid: "abcdefghijklmnop@x", width: 8, want: "abcdefgh"},
		{name: "keeps short uid whole", uid: "abc", width: 12, want: "abc"},
		{name: "no at sign", uid: "abcdefghijklmnop", width: 6, want: "abcdef"},
		{name: "zero width returns input", uid: "standup@example.com", width: 0, want: "standup@example.com"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := ShortUID(tc.uid, tc.width); got != tc.want {
				t.Errorf("ShortUID(%q, %d) = %q, want %q", tc.uid, tc.width, got, tc.want)
			}
		})
	}
}
