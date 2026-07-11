// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/console"
)

type Config struct {
	BrainURL string `json:"brain_url"`
	Token    string `json:"token"`
}

var (
	configDir  string
	configFile string
)

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	configDir = filepath.Join(home, ".config", "corral-desktop")
	configFile = filepath.Join(configDir, "config.json")
}

func loadConfig() (Config, error) {
	var cfg Config
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		return cfg, nil
	}
	// #nosec G304
	b, err := os.ReadFile(configFile)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(b, &cfg)
	return cfg, err
}

func saveConfig(cfg Config) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, b, 0600)
}

func findBrowser() string {
	switch runtime.GOOS {
	case "windows":
		paths := []string{
			os.Getenv("ProgramFiles") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("ProgramFiles(x86)") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("LocalAppData") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`,
			os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`,
		}
		for _, p := range paths {
			// #nosec G703
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	case "darwin":
		paths := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	case "linux":
		browsers := []string{
			"google-chrome",
			"google-chrome-stable",
			"chromium-browser",
			"chromium",
			"brave-browser",
			"microsoft-edge",
		}
		for _, b := range browsers {
			p, err := exec.LookPath(b)
			if err == nil {
				return p
			}
		}
	}
	return ""
}

const configHTML = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Corralai Desktop Client Setup</title>
    <style>
        :root {
            --bg: #09090b;
            --card-bg: #18181b;
            --primary: #8b5cf6;
            --primary-hover: #7c3aed;
            --text: #fafafa;
            --text-muted: #a1a1aa;
            --border: #27272a;
        }

        body {
            background-color: var(--bg);
            color: var(--text);
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            height: 100vh;
            margin: 0;
            overflow: hidden;
        }

        .container {
            background: var(--card-bg);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 2.5rem;
            width: 100%;
            max-width: 440px;
            box-shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.5), 0 8px 10px -6px rgba(0, 0, 0, 0.5);
            animation: fadeIn 0.6s cubic-bezier(0.16, 1, 0.3, 1);
        }

        @keyframes fadeIn {
            from { opacity: 0; transform: translateY(10px); }
            to { opacity: 1; transform: translateY(0); }
        }

        h2 {
            margin-top: 0;
            margin-bottom: 0.5rem;
            font-weight: 700;
            letter-spacing: -0.025em;
            background: linear-gradient(to right, #a78bfa, #f472b6);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }

        p {
            color: var(--text-muted);
            font-size: 0.9rem;
            margin-bottom: 2rem;
            line-height: 1.5;
        }

        .form-group {
            margin-bottom: 1.5rem;
        }

        label {
            display: block;
            font-size: 0.8rem;
            font-weight: 500;
            margin-bottom: 0.5rem;
            color: var(--text-muted);
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }

        input {
            width: 100%;
            padding: 0.75rem 1rem;
            background: #09090b;
            border: 1px solid var(--border);
            border-radius: 6px;
            color: var(--text);
            font-size: 0.95rem;
            box-sizing: border-box;
            transition: all 0.2s ease;
        }

        input:focus {
            outline: none;
            border-color: var(--primary);
            box-shadow: 0 0 0 2px rgba(139, 92, 246, 0.2);
        }

        button {
            width: 100%;
            padding: 0.75rem;
            background: var(--primary);
            color: white;
            border: none;
            border-radius: 6px;
            font-weight: 600;
            font-size: 0.95rem;
            cursor: pointer;
            transition: background 0.2s ease, transform 0.1s ease;
        }

        button:hover {
            background: var(--primary-hover);
        }

        button:active {
            transform: scale(0.98);
        }

        .footer {
            margin-top: 2rem;
            text-align: center;
            font-size: 0.75rem;
            color: var(--text-muted);
        }
    </style>
</head>
<body>
    <div class="container">
        <h2>Connect to Corralai Brain</h2>
        <p>Enter the URL and authentication token of your running Corralai brain instance to begin.</p>
        
        <form action="/save" method="POST">
            <div class="form-group">
                <label for="brain_url">Brain Server URL</label>
                <input type="url" id="brain_url" name="brain_url" placeholder="e.g. http://localhost:8080" value="{{.BrainURL}}" required>
            </div>
            
            <div class="form-group">
                <label for="token">Bearer Token / API Key</label>
                <input type="password" id="token" name="token" placeholder="Enter authorization token" value="{{.Token}}">
            </div>
            
            <button type="submit">Connect</button>
        </form>
        
        <div class="footer">
            Corralai • visibility & accountability enhance performance
        </div>
    </div>
</body>
</html>
`

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("Warning: failed to load config: %v", err)
	}

	browser := findBrowser()
	if browser == "" {
		log.Fatal("Error: no compatible web browser (Chrome, Chromium, Brave, Edge) found on this system. Please install Google Chrome or Microsoft Edge.")
	}

	// If configuration is present, launch the desktop app directly!
	if cfg.BrainURL != "" {
		launchApp(browser, cfg.BrainURL, cfg.Token)
		return
	}

	// Start local server to gather configurations
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to bind local configuration server: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://127.0.0.1:%d", port)

	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t, err := template.New("config").Parse(configHTML)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = t.Execute(w, cfg)
	})

	mux.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method Not Allowed", 405)
			return
		}
		newCfg := Config{
			BrainURL: strings.TrimSuffix(r.FormValue("brain_url"), "/"),
			Token:    r.FormValue("token"),
		}
		if err := saveConfig(newCfg); err != nil {
			http.Error(w, fmt.Sprintf("Failed to save config: %v", err), 500)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `
			<script>
				window.location.href = "/done";
			</script>
		`)
	})

	mux.HandleFunc("/done", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `
			<div style="background:#09090b;color:#fafafa;font-family:sans-serif;height:100vh;display:flex;justify-content:center;align-items:center;flex-direction:column;">
				<h2>Connecting…</h2>
				<p style="color:#a1a1aa;">The application is launching. You can close this tab.</p>
			</div>
		`)
		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = server.Close()
			launchApp(browser, cfg.BrainURL, cfg.Token)
		}()
	})

	go func() {
		_ = server.Serve(listener)
	}()

	// Launch Chrome in application mode targeting our local server
	// #nosec G204
	cmd := exec.Command(browser, fmt.Sprintf("--app=%s", url))
	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to launch desktop application window: %v", err)
	}

	_ = cmd.Wait()
}

// desktopBrowserURL is the address the browser is pointed at: always the
// LOCAL console host, never the daemon, and never carrying the bearer token.
func desktopBrowserURL(localAddr string) string { return "http://" + localAddr }

// launchApp renders the daemon's UI the same way corral-admin does: it runs
// the thin bundle-host console.New on a local loopback listener (mirrors
// cmd/corral-admin/main.go's cmdUI) and points the browser at THAT local
// address — never at the daemon directly. The bearer token stays server-side
// inside the console process; it never enters a URL, browser history, or a
// daemon access log (the defect this replaces: the old launchApp appended
// "?token=" + token to a daemon URL).
func launchApp(browser, brainURL, token string) {
	h, err := console.New(brainURL, token, false) // read-write: desktop action controls work
	if err != nil {
		// Fail loudly: an unreachable daemon or an unsigned/forged bundle is
		// exactly what should stop desktop, not silently degrade.
		log.Fatalf("Failed to start local console for %s: %v", brainURL, err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to bind local console listener: %v", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		_ = srv.Serve(listener)
	}()

	localURL := desktopBrowserURL(listener.Addr().String())
	// #nosec G204
	cmd := exec.Command(browser, fmt.Sprintf("--app=%s", localURL))
	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to launch application window: %v", err)
	}
	_ = cmd.Wait()
}
