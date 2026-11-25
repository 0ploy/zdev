package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// The proxy runs on the host: every address of the host's own networks must
// stay out of reach, whatever form it takes.
func TestPublicAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.65.254",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255",
		"::1", "::", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "ff02::1",
	}
	for _, s := range blocked {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s must be refused", s)
		}
	}
	for _, s := range []string{"140.82.121.4", "1.1.1.1", "2606:4700:4700::1111", "172.32.0.1"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s is public", s)
		}
	}
}

// testEgress resolves every name to the given address and dials target
// instead of the real address (recording what it was asked to dial).
func testEgress(t *testing.T, resolveTo, target string) (*Egress, *[]EgressEntry, *string) {
	t.Helper()
	var mu sync.Mutex
	var entries []EgressEntry
	dialed := new(string)
	e := &Egress{
		Domain: "0ploy.dev",
		Log: func(en EgressEntry) {
			mu.Lock()
			entries = append(entries, en)
			mu.Unlock()
		},
		resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(resolveTo)}, nil
		},
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			*dialed = addr
			return (&net.Dialer{}).DialContext(ctx, "tcp", target)
		},
	}
	return e, &entries, dialed
}

// roundTrip sends raw to the proxy and returns the proxy's response head and
// what follows.
func roundTrip(t *testing.T, e *Egress, raw string) (*http.Response, string) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { e.Serve(context.Background(), server); close(done) }()
	go func() { _, _ = io.WriteString(client, raw) }()
	br := bufio.NewReader(client)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no response: %v", err)
	}
	rest, _ := io.ReadAll(br)
	client.Close()
	<-done
	return res, string(rest)
}

// upstream is a server that answers each connection with one line and closes.
func upstream(t *testing.T, reply string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				_, _ = io.WriteString(c, reply+"|"+strings.TrimSpace(line))
			}()
		}
	}()
	return l.Addr().String()
}

func TestEgressConnectTunnels(t *testing.T) {
	e, entries, dialed := testEgress(t, "140.82.121.4", upstream(t, "hello"))
	res, rest := roundTrip(t, e, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\nping\n")
	if res.StatusCode != 200 || rest != "hello|ping" {
		t.Fatalf("status %d, tunnel %q", res.StatusCode, rest)
	}
	if *dialed != "140.82.121.4:443" {
		t.Errorf("dialed %s: must connect to the checked address, not the name", *dialed)
	}
	if len(*entries) != 1 || (*entries)[0].Host != "github.com" || (*entries)[0].Refused != "" {
		t.Errorf("log = %+v", *entries)
	}
}

func TestEgressRefusals(t *testing.T) {
	cases := []struct {
		name, resolveTo, request, code string
		enforce                        []string
	}{
		{"name resolving to loopback", "127.0.0.1", "CONNECT evil.example:443 HTTP/1.1\r\n\r\n", "private-address", nil},
		{"name resolving to the Docker VM", "192.168.65.254", "CONNECT x.example:80 HTTP/1.1\r\n\r\n", "private-address", nil},
		{"IP literal", "1.1.1.1", "CONNECT 10.0.0.5:5432 HTTP/1.1\r\n\r\n", "private-address", nil},
		{"IPv6 literal", "1.1.1.1", "CONNECT [::1]:5432 HTTP/1.1\r\n\r\n", "private-address", nil},
		{"metadata service", "1.1.1.1", "GET http://169.254.169.254/latest HTTP/1.1\r\nHost: 169.254.169.254\r\n\r\n", "private-address", nil},
		{"project domain", "127.0.0.1", "CONNECT shop.0ploy.dev:443 HTTP/1.1\r\n\r\n", "project-domain", nil},
		{"zdev domain itself", "127.0.0.1", "CONNECT 0ploy.dev:443 HTTP/1.1\r\n\r\n", "project-domain", nil},
		{"enforce", "140.82.121.4", "CONNECT example.com:443 HTTP/1.1\r\n\r\n", "not-allowed", []string{"*.github.com"}},
		{"not a proxy request", "1.1.1.1", "GET / HTTP/1.1\r\nHost: x\r\n\r\n", "bad-request", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, entries, dialed := testEgress(t, c.resolveTo, upstream(t, "x"))
			if c.enforce != nil {
				e.Mode, e.Allow = EgressEnforce, c.enforce
			}
			res, _ := roundTrip(t, e, c.request)
			if res.StatusCode < 400 || res.Header.Get("X-Zdev-Refused") != c.code {
				t.Errorf("status %d, reason %q, want %s", res.StatusCode, res.Header.Get("X-Zdev-Refused"), c.code)
			}
			if *dialed != "" {
				t.Errorf("dialed %s for a refused request", *dialed)
			}
			if len(*entries) != 1 || (*entries)[0].Refused != c.code {
				t.Errorf("log = %+v", *entries)
			}
		})
	}
}

func TestEgressEnforceAllows(t *testing.T) {
	e, _, _ := testEgress(t, "140.82.121.4", upstream(t, "ok"))
	e.Mode, e.Allow = EgressEnforce, []string{"*.github.com", "registry.npmjs.org"}
	for _, host := range []string{"api.github.com", "github.com", "registry.npmjs.org", "api.anthropic.com", "claude.ai"} {
		res, _ := roundTrip(t, e, "CONNECT "+host+":443 HTTP/1.1\r\n\r\nx\n")
		if res.StatusCode != 200 {
			t.Errorf("%s: status %d", host, res.StatusCode)
		}
	}
}

// Plain HTTP: the request goes on in origin form, one per connection.
func TestEgressPlainHTTP(t *testing.T) {
	e, _, dialed := testEgress(t, "93.184.216.34", upstream(t, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
	client, server := net.Pipe()
	go e.Serve(context.Background(), server)
	go func() {
		_, _ = io.WriteString(client, "GET http://example.com/path?q=1 HTTP/1.1\r\nHost: example.com\r\nProxy-Connection: keep-alive\r\n\r\n")
	}()
	all, _ := io.ReadAll(client)
	if !strings.HasSuffix(string(all), "|GET /path?q=1 HTTP/1.1") {
		t.Errorf("upstream saw %q", all)
	}
	if *dialed != "93.184.216.34:80" {
		t.Errorf("dialed %s", *dialed)
	}
}

// Proxy only: loopback (the proxy, Docker's DNS) first, everything else
// rejected last, so the project subnet stays reachable and the internet isn't.
func TestFenceProxyOnly(t *testing.T) {
	s := Fence{AllowSubnets: []string{"172.29.0.0/16"}, ProxyOnly: true}.Script()
	lo, subnet, all := strings.Index(s, "r -o lo -j ACCEPT"), strings.Index(s, "r -d 172.29.0.0/16 -j ACCEPT"), strings.Index(s, "r -j REJECT\n")
	if lo < 0 || subnet < 0 || all < 0 || !(lo < subnet && subnet < all) {
		t.Errorf("order lo=%d subnet=%d reject-all=%d:\n%s", lo, subnet, all, s)
	}
	if !strings.Contains(s, "ip6tables -A zdev-new -j REJECT") {
		t.Error("IPv6 must be closed as well")
	}
	if strings.Contains(Fence{}.Script(), "r -j REJECT\n") {
		t.Error("without ProxyOnly the internet stays open")
	}
}

// A credential host: the proxy terminates TLS with the session CA, swaps the
// placeholder for the token (Bearer and Basic) and forwards; the agent side
// never sees the token.
func TestEgressInterceptSwapsCredential(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Method+" "+r.Host+r.URL.Path+" auth="+r.Header.Get("Authorization"))
	}))
	defer up.Close()
	ca, err := NewSessionCA()
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{Name: "github", Hosts: []string{"*.github.com", "github.com"}, Placeholder: NewPlaceholder("github"), Value: "ghp_REAL"}
	e, entries, _ := testEgress(t, "140.82.121.4", up.Listener.Addr().String())
	e.Credentials, e.CA = []Credential{cred}, ca
	e.upstreamTLS = &tls.Config{InsecureSkipVerify: true}

	client, server := net.Pipe()
	go e.Serve(context.Background(), server)
	go io.WriteString(client, "CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n")
	br := bufio.NewReader(client)
	if res, err := http.ReadResponse(br, nil); err != nil || res.StatusCode != 200 {
		t.Fatalf("CONNECT: %v %v", res, err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.PEM())
	tc := tls.Client(bufConn{client, br}, &tls.Config{RootCAs: pool, ServerName: "api.github.com"})
	tbr := bufio.NewReader(tc)
	get := func(auth string) string {
		req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
		req.Header.Set("Authorization", auth)
		if err := req.Write(tc); err != nil {
			t.Fatal(err)
		}
		res, err := http.ReadResponse(tbr, req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	if got := get("Bearer " + cred.Placeholder); got != "GET api.github.com/user auth=Bearer ghp_REAL" {
		t.Errorf("bearer: %q", got)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+cred.Placeholder))
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_REAL"))
	if got := get(basic); got != "GET api.github.com/user auth="+want {
		t.Errorf("basic: %q", got)
	}
	tc.Close()
	time.Sleep(50 * time.Millisecond)
	for _, en := range *entries {
		if strings.Contains(fmt.Sprint(en), "ghp_REAL") {
			t.Errorf("the token reached the log: %+v", en)
		}
	}
}

func TestEgressRefusesSSH(t *testing.T) {
	e, _, dialed := testEgress(t, "140.82.121.4", upstream(t, "x"))
	res, _ := roundTrip(t, e, "CONNECT github.com:22 HTTP/1.1\r\n\r\n")
	if res.Header.Get("X-Zdev-Refused") != "ssh" || *dialed != "" {
		t.Errorf("status %d reason %q dialed %q", res.StatusCode, res.Header.Get("X-Zdev-Refused"), *dialed)
	}
}
