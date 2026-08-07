// Command srvmon-hub is the central server: it receives snapshots from every
// agent, keeps history, raises alerts and serves the dashboard.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"srvmon/internal/hub"
)

func main() {
	addr := flag.String("addr", envOr("SRVMON_ADDR", ":8080"), "listen address")
	dbPath := flag.String("db", envOr("SRVMON_DB", "/var/lib/srvmon/srvmon.db"), "SQLite database path")
	certFile := flag.String("cert", os.Getenv("SRVMON_CERT"), "TLS certificate (enables HTTPS)")
	keyFile := flag.String("key", os.Getenv("SRVMON_KEY"), "TLS private key")
	baseURL := flag.String("base-url", os.Getenv("SRVMON_BASE_URL"), "public URL used in generated install commands")
	binDir := flag.String("bin-dir", envOr("SRVMON_BIN_DIR", "/var/lib/srvmon/bin"), "directory holding agent builds to serve")
	interval := flag.Int("interval", 2, "seconds between agent pushes")
	sampleEvery := flag.Int("sample-every", 30, "seconds between persisted history points")
	retention := flag.Int("retention-days", 8, "days of history to keep")
	admin := flag.String("admin", "", "create or reset an operator as user:password, then exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("srvmon-hub", hub.Version)
		return
	}

	if dir := filepath.Dir(*dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			log.Fatalf("create data directory %s: %v", dir, err)
		}
	}

	store, err := hub.OpenStore(*dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer store.Close()

	if *admin != "" {
		if err := setAdmin(store, *admin); err != nil {
			log.Fatalf("set operator: %v", err)
		}
		return
	}
	if err := ensureAdmin(store); err != nil {
		log.Fatalf("create first operator: %v", err)
	}

	server := hub.New(hub.Config{
		Addr:          *addr,
		DBPath:        *dbPath,
		CertFile:      *certFile,
		KeyFile:       *keyFile,
		BaseURL:       *baseURL,
		BinDir:        *binDir,
		PushInterval:  *interval,
		SampleEvery:   *sampleEvery,
		RetentionDays: *retention,
	}, store)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx); err != nil {
		log.Fatalf("hub stopped: %v", err)
	}
}

// ensureAdmin creates the first operator on a fresh database. The password
// comes from the environment when the installer supplied one; otherwise it is
// generated and printed once — this line is the only place it ever appears.
func ensureAdmin(store *hub.Store) error {
	count, err := store.CountUsers()
	if err != nil || count > 0 {
		return err
	}

	username := envOr("SRVMON_ADMIN_USER", "admin")
	password := os.Getenv("SRVMON_ADMIN_PASSWORD")
	generated := password == ""
	if generated {
		password, err = randomPassword()
		if err != nil {
			return err
		}
	}
	if err := store.CreateUser(username, password); err != nil {
		return err
	}

	if generated {
		log.Printf("=====================================================")
		log.Printf(" first run — dashboard login created")
		log.Printf("   username: %s", username)
		log.Printf("   password: %s", password)
		log.Printf(" store it now; it will not be shown again")
		log.Printf("=====================================================")
	} else {
		log.Printf("first run — created operator %q from the environment", username)
	}
	return nil
}

func setAdmin(store *hub.Store, spec string) error {
	username, password, ok := strings.Cut(spec, ":")
	if !ok || username == "" || len(password) < 8 {
		return fmt.Errorf("expected -admin user:password with at least 8 password characters")
	}

	user, err := store.UserByName(username)
	if err == nil {
		if err := store.SetCredentials(user.ID, username, password); err != nil {
			return err
		}
		_ = store.DeleteUserSessions(user.ID)
		log.Printf("password for %q updated", username)
		return nil
	}
	if err := store.CreateUser(username, password); err != nil {
		return err
	}
	log.Printf("operator %q created", username)
	return nil
}

func randomPassword() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
