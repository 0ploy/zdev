package agent

// Credentials bound to hosts (PLAN_2 section 12, 4b): the agent container
// holds a placeholder; for the credential's hosts the egress proxy
// terminates TLS with a certificate from a CA that exists only in this
// process for the session, swaps the placeholder for the real token in the
// request headers and forwards over its own TLS. The token never enters the
// container; sent anywhere else, the placeholder is a worthless string.
// Every other host is tunneled untouched.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Credential is a token bound to hosts, with the placeholder the agent sees.
type Credential struct {
	Name        string
	Hosts       []string // exact, or *.example.com
	Placeholder string
	Value       string // only in this process
}

// NewPlaceholder returns a placeholder for a credential: recognizable in
// logs, unique per session.
func NewPlaceholder(name string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "zdev-placeholder-" + name + "-" + hex.EncodeToString(b)
}

func hostMatches(host string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSuffix(p, "."))
		if suffix, ok := strings.CutPrefix(p, "*."); ok {
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == p {
			return true
		}
	}
	return false
}

// credentialFor returns the credential bound to host, if any.
func (e *Egress) credentialFor(host string) *Credential {
	for i := range e.Credentials {
		if hostMatches(host, e.Credentials[i].Hosts) {
			return &e.Credentials[i]
		}
	}
	return nil
}

// SessionCA signs the certificates the proxy presents for credential hosts.
// Its key never leaves the process; the agent container trusts its
// certificate for the session.
type SessionCA struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	pem    []byte
	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewSessionCA creates the CA for one session.
func NewSessionCA() (*SessionCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "zdev agent session CA", Organization: []string{"zdev"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(7 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &SessionCA{cert: cert, key: key, leaves: map[string]*tls.Certificate{},
		pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// PEM is the CA certificate, for the container's trust store.
func (c *SessionCA) PEM() []byte { return c.pem }

func (c *SessionCA) leaf(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leaves[host]; ok {
		return l, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	l := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
	c.leaves[host] = l
	return l, nil
}

// swapCredential replaces the placeholder with the token in the request's
// headers (Basic auth decoded and encoded again). Returns whether it did.
func swapCredential(h http.Header, cred *Credential) bool {
	swapped := false
	for k, vs := range h {
		for i, v := range vs {
			if strings.EqualFold(k, "Authorization") && len(v) > 6 && strings.EqualFold(v[:6], "basic ") {
				if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v[6:])); err == nil && strings.Contains(string(raw), cred.Placeholder) {
					vs[i] = "Basic " + base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(string(raw), cred.Placeholder, cred.Value)))
					swapped = true
					continue
				}
			}
			if strings.Contains(v, cred.Placeholder) {
				vs[i] = strings.ReplaceAll(v, cred.Placeholder, cred.Value)
				swapped = true
			}
		}
	}
	return swapped
}

// bufConn reads through the proxy's request reader, which may hold bytes the
// client sent right after CONNECT (the TLS hello).
type bufConn struct {
	net.Conn
	r io.Reader
}

func (c bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// intercept serves a CONNECT to a credential host: TLS with the session CA's
// certificate towards the agent, the requests forwarded one by one with the
// token swapped in, to addr (the address the proxy checked).
func (e *Egress) intercept(ctx context.Context, client net.Conn, host, addr string, cred *Credential, entry *EgressEntry) {
	tc := tls.Server(client, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return e.CA.leaf(host)
		},
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		entry.Refused, entry.Detail = "tls", "TLS with the agent failed (does it trust the session CA?): "+err.Error()
		return
	}
	defer tc.Close()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig:     &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
		DisableCompression:  true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if e.dial != nil { // tests
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) { return e.dial(ctx, addr) }
		transport.TLSClientConfig = e.upstreamTLS
	}
	defer transport.CloseIdleConnections()
	entry.Credential = cred.Name
	br := bufio.NewReader(tc)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		if h, _, err := net.SplitHostPort(req.Host); (err == nil && !strings.EqualFold(h, host)) || (err != nil && req.Host != "" && !strings.EqualFold(req.Host, host)) {
			writeRefusal(tc, http.StatusBadRequest, &refusal{"host-mismatch", "request for " + req.Host + " in a tunnel to " + host})
			return
		}
		req.URL.Scheme, req.URL.Host, req.RequestURI = "https", host, ""
		swapped := swapCredential(req.Header, cred)
		res, err := transport.RoundTrip(req.WithContext(ctx))
		line := req.Method + " " + req.URL.Path
		if err != nil {
			e.logRequest(host, entry.Port, cred.Name, line+" failed: "+err.Error(), swapped)
			writeRefusal(tc, http.StatusBadGateway, &refusal{"unreachable", host + ": " + err.Error()})
			return
		}
		e.logRequest(host, entry.Port, cred.Name, fmt.Sprintf("%s %d", line, res.StatusCode), swapped)
		res.Proto, res.ProtoMajor, res.ProtoMinor = "HTTP/1.1", 1, 1
		if res.ContentLength < 0 && len(res.TransferEncoding) == 0 {
			res.TransferEncoding = []string{"chunked"} // HTTP/2 upstream: keep the connection usable
		}
		werr := res.Write(tc)
		res.Body.Close()
		if werr != nil || req.Close || res.Close {
			return
		}
	}
}

func (e *Egress) logRequest(host string, port int, credential, request string, swapped bool) {
	if e.Log == nil {
		return
	}
	if !swapped {
		request += " (no placeholder)"
	}
	e.Log(EgressEntry{Time: time.Now(), Host: host, Port: port, Credential: credential, Request: request})
}

// PEMBundle appends the session CA to a trust bundle.
func (c *SessionCA) PEMBundle(base []byte) []byte {
	out := append([]byte(nil), base...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, c.pem...)
}
