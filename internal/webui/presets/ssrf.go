//go:build webui

package presets

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
)

// ValidateBaseURL is the static half of the SSRF guard. It is used when an
// administrator saves presets.base_url, where a DNS lookup would be
// surprising; the resolver-based half is TargetGuard.Check at fetch time.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("presets: invalid URL: %w", err)
	}
	if err := validateTargetURL(u); err != nil {
		return err
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isForbiddenIP(ip) {
		return fmt.Errorf("presets: base URL must not point at a private address")
	}
	return nil
}

func validateTargetURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("presets: only https URLs are allowed, got %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("presets: userinfo is not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("presets: query strings and fragments are not allowed")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("presets: URL has no host")
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("presets: only port 443 is allowed, got %q", p)
	}
	return nil
}

// TargetGuard validates every URL before a request is sent, including each
// redirect hop. The api package substitutes a permissive guard in tests.
type TargetGuard interface {
	Check(ctx context.Context, u *url.URL) error
}

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type ssrfGuard struct{ resolver ipResolver }

func newSSRFGuard() ssrfGuard { return ssrfGuard{resolver: net.DefaultResolver} }

func (g ssrfGuard) Check(ctx context.Context, u *url.URL) error {
	if err := validateTargetURL(u); err != nil {
		return err
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isForbiddenIP(ip) {
			return fmt.Errorf("presets: %q points at a forbidden address", host)
		}
		return nil
	}
	ips, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("presets: resolve %q: %w", host, err)
	}
	for _, a := range ips {
		if isForbiddenIP(a.IP) {
			return fmt.Errorf("presets: %q resolves to a forbidden address", host)
		}
	}
	return nil
}

func isForbiddenIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsUnspecified() {
		return true
	}
	for _, blocked := range []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254"} {
		if a == netip.MustParseAddr(blocked) {
			return true
		}
	}
	return false
}

// guardedDial is a second line of defence for the non-proxy case: if DNS
// changes between the preflight check and the actual connect, reject the
// resolved address when it is an IP literal in a forbidden range.
func guardedDial(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			if ip := net.ParseIP(host); ip != nil && isForbiddenIP(ip) {
				return nil, fmt.Errorf("presets: dial to forbidden address %q", addr)
			}
		}
		return base.DialContext(ctx, network, addr)
	}
}
