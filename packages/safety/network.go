package safety

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ValidatePublicHTTPURL rejects hosts that are plainly local or private before
// a request is made. SafeHTTPClient repeats the check after DNS resolution so
// DNS rebinding cannot turn a public hostname into an internal request.
func ValidatePublicHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http and https URLs are allowed")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("url host is required")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("local addresses are not allowed")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !isPublicAddr(ip) {
		return fmt.Errorf("private or special-use addresses are not allowed")
	}
	return nil
}

// SafeHTTPClient is for user-controlled URLs. It deliberately ignores proxy
// environment variables: a proxy is an egress policy decision, not something a
// process environment should silently add to an untrusted fetch path.
func SafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ip := range ips {
				if !isPublicAddr(ip) {
					return nil, fmt.Errorf("blocked non-public destination %s", host)
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error { return ValidatePublicHTTPURL(req.URL.String()) },
	}
}

func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsPrivate() && !ip.IsMulticast() && !ip.IsUnspecified() && !ip.Is4In6()
}
