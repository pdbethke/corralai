---
name: 2026-07-07-swarm-refinements
type: note
shared: true
description: Swarm UI improvements, concurrency protection, and agent self-healing connection loop
---

# Swarm Refinements (2026-07-07)

Today we implemented several key enhancements to improve the reliability, safety, and UX of the Corralai Swarm:

## 1. Philosophical Tagline & Reflex Loop Control
* **Tagline:** Added the swarm core philosophy to `CORRAL.md`: *"Visibility and accountability enhance, rather than restrict, performance."*
* **Telemetry & Loop Protection:** Built-in safeguards automatically pause a mission and record diagnostic events to the telemetry mixtape and console logs when task limits are hit.

## 2. Launch Tab Priority & Live Progress Indicator
* **Tab Priority:** The `launch` (Mission Composer) tab is now the default landing tab and the first tab in the header HUD.
* **Build indicator:** Added an "Active Swarm Builds" panel at the top of the launch tab to show running/paused/operator-review builds in real-time.
* **Cursor Preservation:** Separated form rendering from dynamic updates to ensure user text input is never lost or blinks during active swarm heartbeats.

## 3. Dual-Layer Concurrency Safety Guards
* **Client-side Warning:** Clicking "Launch Mission" while a build is already active triggers an explicit confirmation dialog warning the user of potential file clobbering.
* **Backend hard-gate:** The `/api/mission/create` endpoint returns an HTTP 409 Conflict if a mission is launched while another is running.

## 4. Self-Healing Connection Loop
* **Crash-to-Recover:** If an agent encounters a transport/session loss (e.g. `session not found`, `client is closing`, etc.), it immediately exits (`os.Exit(2)`).
* **Auto-Restart:** Since agent containers are run with `restart: unless-stopped`, Docker Daemon automatically restarts the container, enabling the agent to re-register with the brain and obtain a fresh valid session.
