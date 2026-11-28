package agent

// The egress proxy (PLAN_2 section 12): the agent container's only way to the
// internet. Its relay listens on 127.0.0.1:ProxyPort and forwards every proxy
// connection to `zdev claude` on the host, which resolves and connects from
// the host itself. Running on the host is what makes the address rules
// critical: without them the proxy would carry the agent past the fence into
// the host's own loopback services and private networks.

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// ProxyPort is where the relay in the container takes proxy connections.
const ProxyPort = 3128

// ProxyURL is the agent container's HTTP(S)_PROXY.
var ProxyURL = fmt.Sprintf("http://127.0.0.1:%d", ProxyPort)

// Egress modes (agent.projects.<p>.egress).
const (
	EgressLog     = "log"     // every public host, logged
	EgressEnforce = "enforce" // only allow_hosts (and later: credential hosts)
)

// EgressEntry is one proxied connection, or a refusal.
type EgressEntry struct {
	Time    time.Time
	Host    string
	Port    int
	Up      int64  // bytes from the agent
	Down    int64  // bytes to the agent
	Refused string // reason code, empty when allowed
	Detail  string
	// Credential names the token swapped in on this connection; Request is
	// one request on an intercepted connection ("GET /path 200").
	Credential string
	Request    string
}

// Egress is the host side of the proxy for one session.
type Egress struct {
	Domain string   // the zdev domain: reached directly, never through here
	Mode   string   // EgressLog (default) or EgressEnforce
	Allow  []string // allowed hosts under enforce: exact, or *.example.com
	// AgentHosts are the agent's own (its API and login), allowed under
	// enforce whatever Allow says.
	AgentHosts []string
	Log        func(EgressEntry)
	// Credentials are swapped in for their hosts, with certificates from CA.
	Credentials []Credential
	CA          *SessionCA

	// resolve and dial, replaceable in tests.
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	dial    func(ctx context.Context, addr string) (net.Conn, error)
	// upstreamTLS is the client TLS config towards credential hosts in tests.
	upstreamTLS *tls.Config
}

// refusal is why a destination is refused.
type refusal struct{ code, detail string }

func (r *refusal) Error() string { return "zdev: refused (" + r.code + "): " + r.detail }

// blockedPrefixes are addresses the proxy never connects to: loopback,
// private, link-local, CGNAT, unique-local and the like (the host's own
// services and networks). netip's predicates cover most; these add the rest.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// publicAddr reports whether the proxy may connect to ip.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ClaudeHosts are Claude Code's own hosts (API, login refresh, its own
// services): its Egress.AgentHosts.
var ClaudeHosts = []string{"*.anthropic.com", "claude.ai", "*.claude.ai", "claude.com", "*.claude.com"}

// allowed reports whether host matches the enforce list.
func (e *Egress) allowed(host string) bool {
	return hostMatches(host, e.AgentHosts) || hostMatches(host, e.Allow) || e.credentialFor(host) != nil
}

// destination checks host and resolves it to the address to connect to. The
// connection goes to that address, never to the name again, so a second DNS
// answer can't swap in a private one (DNS rebinding).
func (e *Egress) destination(ctx context.Context, host string, port int) (string, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || port <= 0 || port > 65535 {
		return "", &refusal{"bad-request", "no host or port"}
	}
	if port == 22 {
		return "", &refusal{"ssh", "SSH is off in agent sandboxes: use HTTPS (git remotes on git@ and ssh:// URLs are rewritten to https:// already)"}
	}
	if d := strings.ToLower(e.Domain); d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
		return "", &refusal{"project-domain", host + " is a zdev project domain: reach it directly (it is in NO_PROXY), not through the proxy"}
	}
	if e.Mode == EgressEnforce && !e.allowed(host) {
		return "", &refusal{"not-allowed", host + " is not in this project's agent allow_hosts; the developer can add it in ~/.zdev/global-config.yaml"}
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		resolve := e.resolve
		if resolve == nil {
			resolve = func(ctx context.Context, h string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
			}
		}
		var err error
		if addrs, err = resolve(ctx, host); err != nil || len(addrs) == 0 {
			return "", &refusal{"dns", "can't resolve " + host}
		}
	}
	for _, ip := range addrs {
		if !publicAddr(ip) {
			return "", &refusal{"private-address", host + " resolves to " + ip.String() + ", a loopback, private or link-local address of this machine's networks"}
		}
	}
	return net.JoinHostPort(addrs[0].Unmap().String(), strconv.Itoa(port)), nil
}

// Serve handles one proxy connection from the agent: CONNECT for HTTPS (a
// tunnel), or a plain HTTP request with an absolute URL.
func (e *Egress) Serve(ctx context.Context, client net.Conn) {
	defer client.Close()
	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	host, portStr := req.URL.Hostname(), req.URL.Port()
	if req.Method == http.MethodConnect {
		host, portStr, _ = net.SplitHostPort(req.Host)
	} else if portStr == "" {
		portStr = "80"
	}
	port, _ := strconv.Atoi(portStr)
	entry := EgressEntry{Time: time.Now(), Host: strings.ToLower(host), Port: port}
	defer func() {
		if e.Log != nil {
			e.Log(entry)
		}
	}()

	if req.Method != http.MethodConnect && req.URL.Scheme != "http" {
		entry.Refused, entry.Detail = "bad-request", "not a proxy request"
		writeRefusal(client, http.StatusBadRequest, &refusal{entry.Refused, entry.Detail})
		return
	}
	addr, err := e.destination(ctx, host, port)
	if err != nil {
		r := err.(*refusal)
		entry.Refused, entry.Detail = r.code, r.detail
		writeRefusal(client, http.StatusForbidden, r)
		return
	}
	dial := e.dial
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
		}
	}
	upstream, err := dial(ctx, addr)
	if err != nil {
		entry.Refused, entry.Detail = "unreachable", err.Error()
		writeRefusal(client, http.StatusBadGateway, &refusal{entry.Refused, host + ": " + err.Error()})
		return
	}
	defer upstream.Close()

	if req.Method == http.MethodConnect {
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if cred := e.credentialFor(entry.Host); cred != nil && e.CA != nil {
			upstream.Close() // intercept dials per request, to the checked address
			e.intercept(ctx, bufConn{client, br}, entry.Host, addr, cred, &entry)
			return
		}
	} else {
		// One request per connection: a keep-alive client could send the
		// next one for another host down the same tunnel.
		req.Header.Del("Proxy-Connection")
		req.Header.Del("Proxy-Authorization")
		req.Header.Set("Connection", "close")
		req.Close = true
		req.RequestURI = ""
		if err := req.Write(upstream); err != nil {
			return
		}
	}
	entry.Up, entry.Down = splice(client, br, upstream)
}

// splice copies both ways until both sides are done; bytes the request
// reader already buffered go first. Returns the bytes sent each way.
func splice(client net.Conn, buffered *bufio.Reader, upstream net.Conn) (up, down int64) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		up, _ = io.Copy(upstream, buffered)
		closeWrite(upstream)
	}()
	down, _ = io.Copy(client, upstream)
	closeWrite(client)
	select {
	case <-done:
	case <-time.After(5 * time.Second): // the server is done; a client that keeps the tunnel open doesn't hold it
		_ = client.Close()
		<-done
	}
	return up, down
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

func writeRefusal(w io.Writer, status int, r *refusal) {
	body := r.Error() + "\n"
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nX-Zdev-Refused: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), r.code, len(body), body)
}
