package client

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
)

// caldavNS is the CalDAV XML namespace from RFC 4791.
const caldavNS = "urn:ietf:params:xml:ns:caldav"

// capabilityCalendar is the DAV compliance class a CalDAV server advertises.
const capabilityCalendar = "calendar-access"

// propfindResourceType asks only for DAV:resourcetype, the cheapest PROPFIND
// that distinguishes a calendar collection from an ordinary resource.
const propfindResourceType = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`

// maxProbeBytes bounds how much of a probe response is parsed.
const maxProbeBytes = 1 << 20

// multistatus is the minimal shape needed to spot a calendar resourcetype.
type multistatus struct {
	XMLName   xml.Name `xml:"DAV: multistatus"`
	Responses []struct {
		PropStats []struct {
			Prop struct {
				ResourceType struct {
					Calendar *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
					Raw      []byte    `xml:",innerxml"`
				} `xml:"DAV: resourcetype"`
			} `xml:"DAV: prop"`
		} `xml:"DAV: propstat"`
	} `xml:"DAV: response"`
}

// detectCalDAV probes endpoint to decide whether it speaks CalDAV.
//
// A URL whose path names a .ics document is taken as a static file without a
// round trip: that is the read-only shape the tool documents. Otherwise the
// endpoint is probed with OPTIONS (looking for the calendar-access compliance
// class in the DAV header) and then PROPFIND (looking for a CalDAV
// resourcetype).
//
// Probe failures are not fatal. They resolve to ICS mode so that the real
// network or authentication error surfaces once, from the actual fetch, with
// full context.
func detectCalDAV(ctx context.Context, hc *retryClient, endpoint string) (bool, error) {
	if looksLikeICS(endpoint) {
		return false, nil
	}
	if probeOptions(ctx, hc, endpoint) {
		return true, nil
	}
	return probePropFind(ctx, hc, endpoint), nil
}

// probeOptions reports whether the DAV response header advertises CalDAV.
func probeOptions(ctx context.Context, hc *retryClient, endpoint string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, endpoint, http.NoBody)
	if err != nil {
		return false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBytes))
		resp.Body.Close()
	}()

	if resp.StatusCode/100 != 2 {
		return false
	}
	for _, header := range resp.Header.Values("Dav") {
		for _, class := range strings.Split(header, ",") {
			if strings.EqualFold(strings.TrimSpace(class), capabilityCalendar) {
				return true
			}
		}
	}
	return false
}

// probePropFind reports whether a Depth:0 PROPFIND shows a calendar
// resourcetype, which also covers servers that omit the DAV header.
func probePropFind(ctx context.Context, hc *retryClient, endpoint string) bool {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", endpoint,
		strings.NewReader(propfindResourceType))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("Depth", "0")
	req.ContentLength = int64(len(propfindResourceType))

	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBytes))
	if err != nil {
		return false
	}

	var ms multistatus
	if err := xml.Unmarshal(body, &ms); err != nil {
		return false
	}
	for _, r := range ms.Responses {
		for _, ps := range r.PropStats {
			rt := ps.Prop.ResourceType
			if rt.Calendar != nil {
				return true
			}
			// Namespace prefixes vary; fall back to a literal scan of the
			// resourcetype payload.
			if strings.Contains(string(rt.Raw), caldavNS) ||
				strings.Contains(strings.ToLower(string(rt.Raw)), ":calendar") {
				return true
			}
		}
	}
	return false
}
