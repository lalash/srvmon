package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func newTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// selfUpdate replaces this binary with the build the hub is serving and returns
// once the new file is in place; the caller exits so systemd starts it.
//
// The downloaded binary is run with -version before it is installed. That is a
// stronger check than a checksum: it proves the file is intact *and* that it
// actually executes on this host, so a build for the wrong libc or architecture
// is caught here instead of turning into a service that will not start.
func selfUpdate(client *http.Client, hub, want string) error {
	target := fmt.Sprintf("%s/download/agent/%s-%s", hub, runtime.GOOS, runtime.GOARCH)

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("resolve the running binary: %w", err)
	}

	// The staged file has to share a filesystem with the target: a running
	// binary cannot be written over, but it can be renamed over, and rename
	// does not cross devices.
	staged := filepath.Join(filepath.Dir(self), ".srvmon-agent.new")
	defer os.Remove(staged)

	if err := download(client, target, staged); err != nil {
		return err
	}
	if got, err := versionOf(staged); err != nil {
		return fmt.Errorf("the downloaded binary does not run: %w", err)
	} else if got != want {
		return fmt.Errorf("the hub serves agent %s but asked for %s", got, want)
	}

	backup := self + ".old"
	_ = os.Remove(backup)
	if err := os.Link(self, backup); err != nil && !errors.Is(err, os.ErrExist) {
		// A hard link is only a convenience for rolling back by hand; losing it
		// is not a reason to abort the update.
		backup = ""
	}
	if err := os.Rename(staged, self); err != nil {
		return fmt.Errorf("install the new binary: %w", err)
	}
	return nil
}

func download(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func versionOf(path string) (string, error) {
	ctx, cancel := newTimeout(10 * time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return "", err
	}
	// The binary prints "srvmon-agent 1.2.3".
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return "", fmt.Errorf("unexpected -version output %q", strings.TrimSpace(string(out)))
	}
	return fields[len(fields)-1], nil
}
