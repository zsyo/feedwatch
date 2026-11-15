//go:build webui

package presets

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestValidateBaseURL(t *testing.T) {
	good := []string{
		DefaultBaseURLForTest,
		"https://raw.githubusercontent.com/zsyo/feedwatch/main/presets/",
		"https://cdn.example.com:443/presets",
		"https://presets.example.com",
	}
	for _, raw := range good {
		if err := ValidateBaseURL(raw); err != nil {
			t.Errorf("valid base URL %q rejected: %v", raw, err)
		}
	}
	bad := []struct{ raw, want string }{
		{"http://example.com/presets", "only https"},
		{"ftp://example.com/presets", "only https"},
		{"https://user:pass@example.com/presets", "userinfo"},
		{"https://example.com/presets?ref=main", "query strings"},
		{"https://example.com/presets#x", "fragments"},
		{"https://example.com:8443/presets", "only port 443"},
		{"https:///presets", "no host"},
		{"https://127.0.0.1/presets", "private address"},
		{"https://10.0.0.5/presets", "private address"},
		{"https://[::1]/presets", "private address"},
		{"https://169.254.169.254/latest/meta-data", "private address"},
		{"not a url at all", "only https"},
		{"%zz", "invalid URL"},
	}
	for _, tc := range bad {
		t.Run(tc.raw, func(t *testing.T) {
			err := ValidateBaseURL(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// A stand-in for the official default; keeping it here avoids making the test
// depend on the production constant used by the api package.
const DefaultBaseURLForTest = "https://raw.githubusercontent.com/zsyo/feedwatch/main/presets"

func TestIsForbiddenIP(t *testing.T) {
	forbidden := []string{
		"127.0.0.1", "127.1.2.3", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0",
		"::1", "::", "fc00::1", "fd00:ec2::254", "fe80::1", "ff02::1",
		"::ffff:127.0.0.1",
	}
	for _, raw := range forbidden {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("test bug: %q is not an IP", raw)
		}
		if !isForbiddenIP(ip) {
			t.Errorf("%s should be forbidden", raw)
		}
	}
	allowed := []string{
		"8.8.8.8", "1.1.1.1", "140.82.114.4", "172.32.0.1", "2606:4700::1111",
	}
	for _, raw := range allowed {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("test bug: %q is not an IP", raw)
		}
		if isForbiddenIP(ip) {
			t.Errorf("%s should be allowed", raw)
		}
	}
}

type fakeResolver struct {
	addrs []net.IPAddr
	err   error
}

func (f fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return f.addrs, f.err
}

func TestSSRFGuardChecksResolvedAddresses(t *testing.T) {
	u := mustURL(t, "https://presets.example.com/presets")
	ips := func(raw ...string) []net.IPAddr {
		out := make([]net.IPAddr, 0, len(raw))
		for _, s := range raw {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out
	}

	// A public hostname that resolves into private space is exactly the
	// rebinding case the static settings validator cannot catch.
	g := ssrfGuard{resolver: fakeResolver{addrs: ips("185.199.108.133", "10.0.0.9")}}
	if err := g.Check(context.Background(), u); err == nil || !strings.Contains(err.Error(), "forbidden address") {
		t.Fatalf("mixed public/private answer was allowed: %v", err)
	}
	g = ssrfGuard{resolver: fakeResolver{addrs: ips("185.199.108.133", "2606:4700::1111")}}
	if err := g.Check(context.Background(), u); err != nil {
		t.Fatalf("public hostname rejected: %v", err)
	}
	g = ssrfGuard{resolver: fakeResolver{err: fmt.Errorf("dns down")}}
	if err := g.Check(context.Background(), u); err == nil || !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("resolver failure was swallowed: %v", err)
	}

	// Literals never need a DNS lookup, and every forbidden URL shape is
	// rejected before any request is built.
	literal := ssrfGuard{resolver: fakeResolver{}}
	for _, raw := range []string{
		"http://presets.example.com/presets",
		"https://user@presets.example.com/presets",
		"https://presets.example.com:8443/presets",
		"https://presets.example.com/presets?x=1",
		"https://127.0.0.1/presets",
	} {
		if err := literal.Check(context.Background(), mustURL(t, raw)); err == nil {
			t.Errorf("%q was allowed by the guard", raw)
		}
	}
	if err := literal.Check(context.Background(), mustURL(t, "https://8.8.8.8/presets")); err != nil {
		t.Fatalf("public literal rejected: %v", err)
	}
}

func TestGuardedDialRejectsForbiddenLiterals(t *testing.T) {
	dial := guardedDial(&net.Dialer{})
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:443"); err == nil ||
		!strings.Contains(err.Error(), "forbidden address") {
		t.Fatalf("dial to loopback was allowed: %v", err)
	}
	if _, err := dial(context.Background(), "tcp", "[fd00:ec2::254]:443"); err == nil ||
		!strings.Contains(err.Error(), "forbidden address") {
		t.Fatalf("dial to metadata address was allowed: %v", err)
	}
}
