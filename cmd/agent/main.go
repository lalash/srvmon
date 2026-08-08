// Command srvmon-agent samples the local host and pushes each snapshot to the
// central hub. It only ever makes outbound HTTPS calls, so it works behind NAT
// and a closed firewall.
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"srvmon/internal/metrics"
)

const agentVersion = "1.1.0"

// updateRetryAfter bounds how often a failing self-update pulls the binary.
const updateRetryAfter = 5 * time.Minute

type config struct {
	hub      string
	token    string
	name     string
	interval time.Duration
	diskPath string
	insecure bool
}

type pushBody struct {
	Name     string            `json:"name"`
	Snapshot *metrics.Snapshot `json:"snapshot"`
}

type pushReply struct {
	Interval int    `json:"interval"`
	Update   string `json:"update,omitempty"`
}

func main() {
	confPath := flag.String("config", defaultConfigPath(), "path to a KEY=VALUE config file")
	hub := flag.String("hub", "", "hub base URL, e.g. https://monitor.example.com")
	token := flag.String("token", "", "agent token issued by the hub")
	name := flag.String("name", "", "display name reported to the hub (defaults to hostname)")
	interval := flag.Int("interval", 0, "seconds between pushes (default 2)")
	diskPath := flag.String("disk", "", "filesystem reported as storage (default /)")
	insecure := flag.Bool("insecure", false, "skip TLS verification (self-signed hub certificate)")
	once := flag.Bool("once", false, "push a single snapshot and exit; useful to test the setup")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("srvmon-agent", agentVersion)
		return
	}

	cfg := loadConfig(*confPath)
	overrideString(&cfg.hub, *hub)
	overrideString(&cfg.token, *token)
	overrideString(&cfg.name, *name)
	overrideString(&cfg.diskPath, *diskPath)
	if *interval > 0 {
		cfg.interval = time.Duration(*interval) * time.Second
	}
	if *insecure {
		cfg.insecure = true
	}
	if cfg.interval <= 0 {
		cfg.interval = 2 * time.Second
	}
	cfg.hub = strings.TrimRight(cfg.hub, "/")

	if cfg.hub == "" || cfg.token == "" {
		log.Fatal("hub URL and token are required (use -hub/-token, the config file, or SRVMON_HUB/SRVMON_TOKEN)")
	}
	if cfg.name == "" {
		if host, err := os.Hostname(); err == nil {
			cfg.name = host
		}
	}

	client := &http.Client{Timeout: 15 * time.Second}
	if cfg.insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	collector := metrics.NewCollector(agentVersion, cfg.diskPath)
	collector.Collect() // prime the CPU and network deltas

	if *once {
		time.Sleep(time.Second)
		if _, err := push(client, cfg, collector.Collect()); err != nil {
			log.Fatalf("push failed: %v", err)
		}
		log.Printf("pushed one snapshot to %s as %q", cfg.hub, cfg.name)
		return
	}

	log.Printf("srvmon-agent %s reporting to %s every %s as %q", agentVersion, cfg.hub, cfg.interval, cfg.name)
	run(client, cfg, collector)
}

func run(client *http.Client, cfg config, collector *metrics.Collector) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	failures := 0
	var lastUpdateTry time.Time
	for {
		select {
		case <-stop:
			log.Println("shutting down")
			return
		case <-ticker.C:
			reply, err := push(client, cfg, collector.Collect())
			if err != nil {
				failures++
				// Log the first failure and then every ~minute, so a hub outage
				// does not fill the journal with one line per tick.
				if failures == 1 || failures%30 == 0 {
					log.Printf("push failed (%d in a row): %v", failures, err)
				}
				continue
			}
			if failures > 0 {
				log.Printf("hub reachable again after %d failed pushes", failures)
				failures = 0
			}
			if want := time.Duration(reply.Interval) * time.Second; want > 0 && want != cfg.interval {
				cfg.interval = want
				ticker.Reset(want)
				log.Printf("hub asked for a %s interval", want)
			}
			// The hub keeps asking until the new version reports in, so a failed
			// update retries rather than stranding the host — but not on every
			// push, or a hub serving a broken build would have every agent
			// pulling the binary every couple of seconds.
			if reply.Update != "" && reply.Update != agentVersion && time.Since(lastUpdateTry) > updateRetryAfter {
				lastUpdateTry = time.Now()
				log.Printf("hub asked for agent %s (running %s)", reply.Update, agentVersion)
				if err := selfUpdate(client, cfg.hub, reply.Update); err != nil {
					log.Printf("self-update failed, retrying in %s: %v", updateRetryAfter, err)
					continue
				}
				log.Printf("updated to %s, restarting", reply.Update)
				return
			}
		}
	}
}

func push(client *http.Client, cfg config, snap *metrics.Snapshot) (pushReply, error) {
	var reply pushReply

	body, err := json.Marshal(pushBody{Name: cfg.name, Snapshot: snap})
	if err != nil {
		return reply, err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.hub+"/api/agent/push", bytes.NewReader(body))
	if err != nil {
		return reply, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.token)
	req.Header.Set("User-Agent", "srvmon-agent/"+agentVersion)

	resp, err := client.Do(req)
	if err != nil {
		return reply, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return reply, fmt.Errorf("hub returned %s", resp.Status)
	}
	_ = json.NewDecoder(resp.Body).Decode(&reply)
	return reply, nil
}

func defaultConfigPath() string {
	if path := os.Getenv("SRVMON_AGENT_CONFIG"); path != "" {
		return path
	}
	return "/etc/srvmon/agent.conf"
}

// loadConfig reads the config file first, then lets SRVMON_* environment
// variables win; command-line flags override both.
func loadConfig(path string) config {
	cfg := config{}
	values := map[string]string{}

	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			values[strings.ToUpper(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
		f.Close()
	}
	for _, key := range []string{"HUB", "TOKEN", "NAME", "INTERVAL", "DISK", "INSECURE"} {
		if env := os.Getenv("SRVMON_" + key); env != "" {
			values[key] = env
		}
	}

	cfg.hub = values["HUB"]
	cfg.token = values["TOKEN"]
	cfg.name = values["NAME"]
	cfg.diskPath = values["DISK"]
	if secs, err := strconv.Atoi(values["INTERVAL"]); err == nil && secs > 0 {
		cfg.interval = time.Duration(secs) * time.Second
	}
	cfg.insecure = values["INSECURE"] == "1" || strings.EqualFold(values["INSECURE"], "true")
	return cfg
}

func overrideString(dst *string, value string) {
	if value != "" {
		*dst = value
	}
}




