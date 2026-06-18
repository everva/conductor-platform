package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// hostRow is one host's projection in the registry view, in a stable shape for
// both the table and the --json output.
type hostRow struct {
	ID           string   `json:"id"`
	Capabilities []string `json:"capabilities"`
	Heartbeat    string   `json:"heartbeat"`
}

// hosts renders the host registry read from the SHARED StateStore (ADR-0024
// self-registration): per-host id, comma-joined capabilities, and heartbeat
// freshness (the age of host.LastHeartbeat), in stable host-id order. asJSON
// switches to machine-readable output. It reads only through the shared
// StateStore, mirroring status.
func (a *app) hosts(ctx context.Context, asJSON bool) error {
	hosts, err := a.store.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("hosts: list hosts: %w", err)
	}
	// Stable ordering by host id regardless of store insertion order.
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })

	rows := make([]hostRow, 0, len(hosts))
	for _, h := range hosts {
		rows = append(rows, hostRow{
			ID:           h.ID,
			Capabilities: h.Capabilities,
			Heartbeat:    heartbeatAge(h.LastHeartbeat),
		})
	}

	if asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Hosts []hostRow `json:"hosts"`
		}{Hosts: rows})
	}

	// Render into an in-memory tabwriter, then write the aligned table to a.out in
	// a single checked write (the tabwriter buffer itself never errors).
	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "HOST\tCAPABILITIES\tHEARTBEAT")
	for _, r := range rows {
		caps := "-"
		if len(r.Capabilities) > 0 {
			caps = strings.Join(r.Capabilities, ",")
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", r.ID, caps, r.Heartbeat)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("hosts: render table: %w", err)
	}
	if _, err := io.WriteString(a.out, buf.String()); err != nil {
		return fmt.Errorf("hosts: write: %w", err)
	}
	return nil
}

// heartbeatAge renders a host's last-heartbeat freshness as a short relative age
// (e.g. "5m ago" or "2h3m ago"), or "-" for a zero heartbeat (never seen).
func heartbeatAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	return shortDuration(d) + " ago"
}

// shortDuration formats d as a compact age string at second/minute/hour
// granularity: "3s", "5m", "2h3m". It rounds to the nearest second so a fresh
// heartbeat reads cleanly.
func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		m := int(d / time.Minute)
		if s := int((d % time.Minute) / time.Second); s > 0 {
			return fmt.Sprintf("%dm%ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	default:
		h := int(d / time.Hour)
		if m := int((d % time.Hour) / time.Minute); m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
}
