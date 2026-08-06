package hub

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed web
var webFS embed.FS

// allowedAgentTargets bounds what /download/agent can hand out, so the path
// segment can never walk outside the binary directory.
var allowedAgentTargets = map[string]bool{
	"linux-amd64":   true,
	"linux-arm64":   true,
	"linux-arm":     true,
	"darwin-amd64":  true,
	"darwin-arm64":  true,
	"windows-amd64": true,
}

func (h *Hub) staticHandler() http.Handler {
	assets, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if h.currentUser(r) == 0 {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			serveFile(w, r, assets, "index.html")
			return
		case "/login":
			if h.currentUser(r) != 0 {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			serveFile(w, r, assets, "login.html")
			return
		case "/install-agent.sh":
			w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
			serveFile(w, r, assets, "install-agent.sh")
			return
		}

		if target, ok := strings.CutPrefix(r.URL.Path, "/download/agent/"); ok {
			h.serveAgentBinary(w, r, target)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			files.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// serveAgentBinary hands the agent build to an installing host. It is public
// on purpose: the binary is not a secret and the install one-liner runs before
// any credential exists on that machine.
func (h *Hub) serveAgentBinary(w http.ResponseWriter, r *http.Request, target string) {
	if !allowedAgentTargets[target] || h.cfg.BinDir == "" {
		http.NotFound(w, r)
		return
	}
	name := "srvmon-agent-" + target
	if strings.HasPrefix(target, "windows-") {
		name += ".exe"
	}

	path := filepath.Join(h.cfg.BinDir, name)
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "agent build for "+target+" is not available on this hub", http.StatusNotFound)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.Error(w, "cannot stat agent binary", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func serveFile(w http.ResponseWriter, r *http.Request, assets fs.FS, name string) {
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
