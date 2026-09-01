# ical-cli

Read, write and edit calendar events at a remote iCalendar URL, from the command line.

`ical-cli` speaks two protocols behind one interface:

| Mode | URL shape | Capabilities |
| --- | --- | --- |
| **ICS** | `https://host/cal.ics`, `webcal://host/cal.ics` | read-only: `list`, `get`, `search`, `export` |
| **CalDAV** | a CalDAV collection, or `--caldav` | full read/write: everything, plus `add`, `edit`, `delete`, `import` |

A plain `.ics` URL is a single opaque document as far as HTTP is concerned: there is
no per-event addressing and no write verb. Write commands against one fail with an
error naming CalDAV as the requirement, rather than pretending to succeed.

## Install

```sh
go install github.com/prdlk/ical-cli@latest
```

Or from a checkout:

```sh
make build      # ./bin/ical-cli
make install    # $HOME/.local/bin/ical-cli (override with PREFIX=)
```

Requires Go 1.22 or newer.

## Configuration

Settings resolve with the precedence **flag → environment → config file**.

| Flag | Environment | Config key | Meaning |
| --- | --- | --- | --- |
| `--url` | `ICAL_CLI_URL` | `url` | calendar URL |
| `--user` | `ICAL_CLI_USER` | `user` | basic auth username |
| `--pass` | `ICAL_CLI_PASS` | `pass` | basic auth password |
| `--caldav` | `ICAL_CLI_CALDAV` | `caldav` | force CalDAV, skipping auto-detection |
| `--json` | `ICAL_CLI_JSON` | `json` | machine-readable output |
| `--tz` | `ICAL_CLI_TZ` | `tz` | display timezone (default: local) |

`~/.config/ical-cli/config.yaml`:

```yaml
url: https://dav.example.com/calendars/ada/work/
user: ada
pass: app-specific-token
tz: Europe/Lisbon
```

Point `--config` at a different file to override the location. A missing file at the
default path is fine; a missing file you asked for by name is an error.

Prefer `ICAL_CLI_PASS` over `--pass`, which is visible in the process list.

### Protocol detection

Without `--caldav`, a URL whose path ends in `.ics` is treated as a read-only
document with no network probe. Any other URL is probed with `OPTIONS` (looking for
the `calendar-access` compliance class) and then `PROPFIND` (looking for a CalDAV
`resourcetype`). If neither matches, the URL falls back to read-only ICS mode.

When the URL is a CalDAV server but not itself a collection, RFC 6764 discovery
runs: `current-user-principal` → `calendar-home-set` → the first calendar that
accepts `VEVENT`.

## Dates

Every date flag accepts:

- RFC3339 — `2026-03-05T14:00:00Z`, `2026-03-05T14:00:00+02:00`
- calendar date — `2026-03-05` (implies an all-day event)
- iCalendar basic — `20260305T140000Z`, `20260305`
- keywords — `now`, `today`, `tomorrow`, `yesterday`
- relative offsets — `+3d`, `-2w`, `+6h`, `2mo`, `+1y`

A bare date carries no time of day, so it implies `--all-day` — unless an explicit
`--end` or `--duration` contradicts that. `--start 2026-04-10 --duration 45m` is a
timed event, not a broken all-day one.

All-day events use `DATE` values. `DTEND` is exclusive on the wire, but `--end` and
the displayed end date are the **inclusive** last day, which is what a human means
by "the offsite runs the 10th to the 12th".

## Commands

### list

Events ordered by start time, with recurring series expanded into occurrences.
Defaults to a window starting today and covering one year.

```sh
ical-cli list --from 2026-03-01 --to 2026-03-31 --limit 20
```

```
UID           START             END               SUMMARY          LOCATION
2635e0d6-88f  2026-03-02 09:00  2026-03-02 09:15  Standup
608c584b-b73  2026-03-10 09:00  2026-03-10 10:30  Sprint planning  Room 9
3852d993-00f  2026-04-10        2026-04-12        Offsite          Lisbon
```

A bare `--from`/`--to` date is inclusive of that whole day, so
`--to 2026-06-08` covers events starting on the 8th.

`--all` lists the whole calendar with no window. Because expansion needs a bounded
window — an `RRULE` with neither `COUNT` nor `UNTIL` recurs forever — `--all` reports
recurring series as their stored masters rather than expanding them. It cannot be
combined with `--from` or `--to`.

### get

Every property of one event, including those this tool does not model.

```sh
ical-cli get 608c584b
```

The UID may be abbreviated to any unambiguous prefix. An ambiguous prefix lists the
candidates and exits `1`; an unknown one exits `2`.

### add

```sh
ical-cli add --summary "Sprint planning" \
             --start 2026-03-10T09:00:00Z \
             --duration 1h30m \
             --location "Room 5"
```

The UID is generated as `<uuid>@ical-cli`. With no `--start` the event begins now.
With neither `--end` nor `--duration`, a timed event lasts one hour and an
`--all-day` event covers one day. A create sends `If-None-Match: *`, so a UID
collision cannot clobber an existing object.

### edit

```sh
ical-cli edit 608c584b --location "Room 9"
```

Edit is a read-modify-write. The event is fetched, only the flags you passed are
replaced, and everything else is preserved: attendees, alarms, `ORGANIZER`, and
custom `X-` properties. `SEQUENCE` is bumped and `LAST-MODIFIED` is set.

The stored ETag is sent as `If-Match`. If another client wrote in the meantime the
server answers `412` and the command reports a conflict and exits `3`, rather than
overwriting the other change.

### delete

```sh
ical-cli delete 608c584b --yes
```

Prompts unless `--yes`. A non-interactive stdin counts as a refusal, so a piped
invocation never deletes silently.

### search

Case-insensitive substring match over `SUMMARY`, `DESCRIPTION` and `LOCATION`.
Covers the whole calendar unless `--from`/`--to` narrow it.

```sh
ical-cli search "sprint"
```

### export

```sh
ical-cli export --output backup.ics
```

In ICS mode the upstream document is emitted byte for byte. In CalDAV mode every
object is merged into one document, de-duplicating `VTIMEZONE` components. Written
files are mode `0600`, since calendars carry private information.

### import

```sh
ical-cli import team.ics
```

Each UID becomes one calendar object, carrying its recurrence overrides and the
`VTIMEZONE` components it needs. An existing UID is skipped unless `--replace`.
Reports created / replaced / skipped counts; `--dry-run` reports without writing.

## Recurring events

`list` expands `RRULE` within the query window, honouring `EXDATE` and `RDATE`. A
`RECURRENCE-ID` override replaces the generated instance it targets, and a
`CANCELLED` override removes it.

`edit` and `delete` act on the whole series by default. `--occurrence` targets a
single instance:

```sh
# Move just the 9 March standup.
ical-cli edit 2635e0d6 --occurrence 2026-03-09T09:00:00Z \
                       --start 2026-03-09T11:00:00Z \
                       --summary "Standup (moved)"

# Drop just the 16 March standup.
ical-cli delete 2635e0d6 --occurrence 2026-03-16T09:00:00Z --yes
```

Editing an occurrence creates a `RECURRENCE-ID` override that inherits the master's
properties minus the recurrence rule. Deleting one adds an `EXDATE` to the master,
which keeps the series and its other overrides intact.

An `RRULE` with neither `COUNT` nor `UNTIL` recurs forever. Expansion is bounded by
the query window and a hard per-series occurrence budget, so an endless rule cannot
hang the command.

## Output

Default output is an aligned table. `--json` emits full event objects with stable,
script-safe field names:

```sh
ical-cli list --json | jq -r '.[] | "\(.start)  \(.summary)"'
```

```json
{
  "uid": "review@example.com",
  "summary": "Q1 review",
  "location": "Boardroom",
  "start": "2026-03-05T14:00:00Z",
  "end": "2026-03-05T15:00:00Z",
  "all_day": false,
  "duration_seconds": 3600,
  "recurring": false,
  "occurrence": false,
  "sequence": 2,
  "attendees": ["mailto:bob@example.com"],
  "extra": { "X-CUSTOM-TAG": ["keep-me"] }
}
```

All-day events report dates (`2026-04-10`) rather than timestamps. Properties the
tool does not model appear under `extra`, so nothing in the event is invisible.
Diagnostics go to stderr, keeping stdout parseable.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | success |
| `1` | error (including an ambiguous UID prefix and read-only rejection) |
| `2` | event or calendar not found |
| `3` | conflict: the event changed on the server since it was read |

## Networking

One shared transport handles every request: a 30-second timeout, and three attempts
with exponential backoff on transport failures, `5xx`, and `429`. Request bodies are
buffered so a retry can replay them.

## Development

```sh
make test     # go test -race ./...
make lint     # go vet + golangci-lint
make cover    # coverage summary
make tidy     # go mod tidy + verify
```

Layout:

```
cmd/                 one file per command, plus root.go and shared event flags
internal/client/     CalendarClient interface; ICS and CalDAV implementations
internal/event/      event model, date parsing, UID matching, RRULE expansion
internal/output/     table and JSON renderers
main.go
```

Commands depend only on the `CalendarClient` interface (`List`, `Get`, `Put`,
`Delete`, `Raw`, `Mode`), so neither protocol leaks into the command layer.

### Notes on the CalDAV layer

Reads go through `go-webdav`'s `REPORT` helper. Writes and single-object reads use
raw HTTP requests on the same transport, for two reasons:

- `caldav.Client.PutCalendarObject` does not send `If-Match` or `If-None-Match`
  (upstream carries a `TODO` to that effect), so conditional writes are impossible
  through it and lost updates would go undetected.
- `go-webdav` wraps every non-2xx response in `internal.HTTPError`, an unexported
  type in an internal package. Callers cannot read the status code from it, and this
  tool must tell `412 Precondition Failed` apart from `404 Not Found`.

`go-webdav` also runs `strconv.Unquote` on ETags it parses, so a server emitting a
weak validator (`W/"abc"`) will fail its `REPORT` decoding. Raw reads here keep the
header verbatim and re-quote only when needed, so weak ETags round-trip correctly
through `If-Match`.

Remote calendar data is untrusted, and `go-ical`'s decoder panics on some malformed
input; every decode path here converts that into an error.
