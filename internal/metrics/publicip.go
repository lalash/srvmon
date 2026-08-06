package metrics

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const publicIPTTL = 30 * time.Minute

var ipv4Sources = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com"}
var ipv6Sources = []string{"https://api6.ipify.org", "https://ipv6.icanhazip.com"}

// refreshPublicIP re-resolves the host's outside addresses at most every
// publicIPTTL; the lookup leaves the box, so it must not run on every tick.
func (c *Collector) refreshPublicIP(now time.Time) {
	if !c.publicIPAt.IsZero() && now.Sub(c.publicIPAt) < publicIPTTL {
		return
	}
	c.publicIPAt = now
	c.publicIP[0] = fetchFirst("tcp4", ipv4Sources)
	c.publicIP[1] = fetchFirst("tcp6", ipv6Sources)
}

func fetchFirst(network string, urls []string) string {
	client := &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
			},
		},
	}
	for _, url := range urls {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
		resp.Body.Close()
		if err != nil {
			continue
		}
		if ip := strings.TrimSpace(string(body)); net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}
