package main

import (
	"net/http"
	"sort"
	"time"
)

// Fleet status — a compact, READ-ONLY projection of the conductor fleet's health for the standalone
// "Fleet" HUD (macOS menu bar + iOS widget), so the operator can tell at a glance whether the fleet
// is working, idle, or stuck WITHOUT opening the editor. Aggregates projects + tasks + host
// heartbeats into per-project counts + the running/blocked tasks + an overall health verdict. It is
// exposed publicly (usage ingress /fleet path) but stays bearer-token gated and read-only.

const fleetHostStale = 120 * time.Second // a host quieter than this is treated as down (matches reconcile hostStale)

type fleetHostDTO struct {
	ID           string `json:"id"`
	Online       bool   `json:"online"`
	SecondsSince int64  `json:"seconds_since"`
}

type fleetTaskDTO struct {
	ID     string `json:"id"`
	Lane   string `json:"lane,omitempty"`
	Tier   string `json:"tier,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type fleetProjectDTO struct {
	ID      string         `json:"id"`
	Paused  bool           `json:"paused"`
	Health  string         `json:"health"` // working | idle | blocked
	Counts  map[string]int `json:"counts"`
	Running []fleetTaskDTO `json:"running,omitempty"`
	Blocked []fleetTaskDTO `json:"blocked,omitempty"`
}

type fleetStatusDTO struct {
	GeneratedAt string            `json:"generated_at"`
	Overall     string            `json:"overall"` // working | idle | blocked | down
	Hosts       []fleetHostDTO    `json:"hosts"`
	Projects    []fleetProjectDTO `json:"projects"`
}

// handleFleetStatus: GET /fleet/status — the aggregate fleet health for the Fleet HUD.
func (s *apiServer) handleFleetStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		s.serverError(w, "fleet: list projects", err)
		return
	}
	hosts, err := s.store.ListHosts(ctx)
	if err != nil {
		s.serverError(w, "fleet: list hosts", err)
		return
	}
	now := s.now()

	hostDTOs := make([]fleetHostDTO, 0, len(hosts))
	anyHostStale := false
	for _, h := range hosts {
		since := now.Sub(h.LastHeartbeat)
		online := since < fleetHostStale
		if !online {
			anyHostStale = true
		}
		hostDTOs = append(hostDTOs, fleetHostDTO{ID: h.ID, Online: online, SecondsSince: int64(since.Seconds())})
	}
	sort.Slice(hostDTOs, func(i, j int) bool { return hostDTOs[i].ID < hostDTOs[j].ID })

	projDTOs := make([]fleetProjectDTO, 0, len(projects))
	anyBlocked, anyRunning := false, false
	for _, p := range projects {
		tasks, err := s.store.ListTasks(ctx, p.ID)
		if err != nil {
			s.serverError(w, "fleet: list tasks", err)
			return
		}
		counts := map[string]int{}
		var running, blocked []fleetTaskDTO
		for _, t := range tasks {
			counts[t.Status]++
			switch t.Status {
			case "running":
				running = append(running, fleetTaskDTO{ID: t.ID, Lane: t.Lane, Tier: t.Tier})
			case "blocked":
				blocked = append(blocked, fleetTaskDTO{ID: t.ID, Lane: t.Lane, Tier: t.Tier, Reason: t.LastError})
			}
		}
		health := "idle"
		switch {
		case len(blocked) > 0:
			health = "blocked"
			anyBlocked = true
		case len(running) > 0:
			health = "working"
			anyRunning = true
		}
		// Only surface projects that actually have tasks (skip empty/registered-but-idle noise).
		if len(tasks) == 0 {
			continue
		}
		projDTOs = append(projDTOs, fleetProjectDTO{
			ID: p.ID, Paused: p.Paused, Health: health, Counts: counts, Running: running, Blocked: blocked,
		})
	}
	sort.Slice(projDTOs, func(i, j int) bool { return projDTOs[i].ID < projDTOs[j].ID })

	overall := "idle"
	switch {
	case anyBlocked:
		overall = "blocked"
	case anyHostStale && anyRunning:
		overall = "working" // still doing work; a stale host may just be a second idle host
	case anyHostStale:
		overall = "down"
	case anyRunning:
		overall = "working"
	}

	writeJSON(w, http.StatusOK, fleetStatusDTO{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Overall:     overall,
		Hosts:       hostDTOs,
		Projects:    projDTOs,
	})
}
