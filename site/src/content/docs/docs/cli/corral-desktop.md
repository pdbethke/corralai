---
title: corral-desktop
description: Local desktop companion app wrapper for the Swarm Cockpit.
---

{/* SPDX-License-Identifier: Elastic-2.0 */}

`corral-desktop` is a desktop application wrapper for the Corral Cockpit. It automatically manages local configuration and launches the Cockpit in Google Chrome's standalone **application mode** (an chromeless window frame without tabs or URL bars), giving you a native desktop experience.

---

## How It Works

1. **Auto-Configuration:** On startup, it looks for the configuration file at `~/.config/corral-desktop/config.json`.
2. **Local Setup Server:** If the configuration is missing or incomplete, it starts a lightweight, temporary web server on a random local port.
3. **Application Mode Detection:** It searches your system for Google Chrome, Brave Browser, or Microsoft Edge.
4. **Interactive Setup:** It launches the browser pointing to the local setup page. Once you input your Corral Brain URL and Token, the configuration is saved, the temporary server shuts down, and the desktop client automatically opens the Swarm Cockpit.

---

## Build and Run

To compile and launch the desktop client:

```bash
# Build the binary
go build -o bin/corral-desktop ./cmd/corral-desktop

# Run the app
./bin/corral-desktop
```

---

## Configuration Location

The configuration details are persistently stored in:
- **Linux/macOS:** `~/.config/corral-desktop/config.json`
- **Windows:** `%USERPROFILE%\.config\corral-desktop\config.json`
