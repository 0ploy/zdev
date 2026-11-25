package agent

// The relay between an agent container and `zdev claude` on the host
// (PLAN_2 section 8). The SSH protocol is used for its channels, separate
// stdout/stderr, exit status and signals, not for encryption or login: the
// transport is the stdio of a `docker exec -i`, already private and
// authenticated by Docker, so the host key is made per session and clients
// don't authenticate.
//
//	in-container zdev ──unix socket──▶ zdev sandbox relay ══ docker exec pipe ══▶ zdev claude (host)
//	(SSH client)                       forwards each local      (SSH, host is        (SSH server per forwarded
//	                                   connection as a          the server)           channel: validates and
//	                                   "zdev-client" channel                          runs each request)
//
// The relay is a plain forwarder: every check runs on the host, where the
// container can't reach it.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// RelaySocket is the relay's socket in the container (ZDEV_RELAY).
const RelaySocket = "/tmp/zdev-relay.sock"

const clientChannel = "zdev-client"

// Request is one call from the container: a zdev command (Op empty), or one
// of the two operations `zdev run` needs from the runtime (OpRunning,
// OpExecStream) for its container steps.
type Request struct {
	Op   string   `json:"op,omitempty"`
	Args []string `json:"args,omitempty"`
	Cwd  string   `json:"cwd,omitempty"`

	// OpRunning and OpExecStream
	Container string            `json:"container,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	User      string            `json:"user,omitempty"`
	Workdir   string            `json:"workdir,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

const (
	OpRunning    = "running"     // exit 0 when the container runs, 1 when not
	OpExecStream = "exec-stream" // run Argv in Container with the caller's streams
)

// pipeConn is a net.Conn over a reader and a writer (a process's stdio).
type pipeConn struct {
	io.Reader
	io.WriteCloser
}

func (pipeConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (pipeConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (pipeConn) SetDeadline(time.Time) error      { return nil }
func (pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (pipeConn) SetWriteDeadline(time.Time) error { return nil }
func (c pipeConn) Close() error                   { return c.WriteCloser.Close() }
func (pipeAddr) Network() string                  { return "pipe" }
func (pipeAddr) String() string                   { return "pipe" }

type pipeAddr struct{}

// PipeConn wraps a reader and writer (e.g. a process's stdout and stdin) as a net.Conn.
func PipeConn(r io.Reader, w io.WriteCloser) net.Conn { return pipeConn{r, w} }

// channelConn makes an SSH channel a net.Conn, for an SSH connection inside it.
type channelConn struct{ ssh.Channel }

func (channelConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (channelConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (channelConn) SetDeadline(time.Time) error      { return nil }
func (channelConn) SetReadDeadline(time.Time) error  { return nil }
func (channelConn) SetWriteDeadline(time.Time) error { return nil }

func serverConfig() (*ssh.ServerConfig, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	return cfg, nil
}

func clientConfig() *ssh.ClientConfig {
	return &ssh.ClientConfig{User: "zdev", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 10 * time.Second}
}

// Handler runs one validated request: stdin from the channel, stdout to it,
// stderr to stderr, signals from sigs. It returns the exit code.
type Handler func(ctx context.Context, req Request, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan string) int

// Serve runs the host side over conn (the relay's stdio) until it closes.
func Serve(ctx context.Context, conn net.Conn, handle Handler) error {
	cfg, err := serverConfig()
	if err != nil {
		return err
	}
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return fmt.Errorf("relay handshake: %w", err)
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != clientChannel {
			_ = nc.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(creqs)
		go serveClient(ctx, ch, cfg, handle)
	}
	return nil
}

// serveClient is the SSH server for one in-container zdev, inside a channel.
func serveClient(ctx context.Context, ch ssh.Channel, cfg *ssh.ServerConfig, handle Handler) {
	defer ch.Close()
	sc, chans, reqs, err := ssh.NewServerConn(channelConn{ch}, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		sess, sreqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go serveSession(ctx, sess, sreqs, handle)
	}
}

func serveSession(ctx context.Context, sess ssh.Channel, reqs <-chan *ssh.Request, handle Handler) {
	defer sess.Close()
	sigs := make(chan string, 4)
	started := false
	for r := range reqs {
		switch r.Type {
		case "exec":
			if started {
				_ = r.Reply(false, nil)
				continue
			}
			var payload struct{ Command string }
			var req Request
			if err := ssh.Unmarshal(r.Payload, &payload); err != nil || json.Unmarshal([]byte(payload.Command), &req) != nil {
				_ = r.Reply(false, nil)
				return
			}
			started = true
			_ = r.Reply(true, nil)
			go func() {
				code := handle(ctx, req, sess, sess, sess.Stderr(), sigs)
				_ = sess.CloseWrite()
				_, _ = sess.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				_ = sess.Close()
			}()
		case "signal":
			var s struct{ Signal string }
			if ssh.Unmarshal(r.Payload, &s) == nil {
				select {
				case sigs <- s.Signal:
				default:
				}
			}
			if r.WantReply {
				_ = r.Reply(true, nil)
			}
		default:
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}
}

// RunRelay is `zdev sandbox relay` in the container: an SSH client on its own
// stdio, forwarding each connection on the socket as a channel. When stdin
// closes (`zdev claude` on the host is gone), it stops the container: a
// killed `docker exec -it` leaves Claude running otherwise (spike 2).
func RunRelay(socket string) error {
	conn := PipeConn(os.Stdin, os.Stdout)
	c, chans, reqs, err := ssh.NewClientConn(conn, "relay", clientConfig())
	if err != nil {
		stopContainer()
		return fmt.Errorf("relay handshake: %w", err)
	}
	client := ssh.NewClient(c, chans, reqs)
	_ = os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		stopContainer()
		return err
	}
	_ = os.Chmod(socket, 0o666) // processes of other users in the container (a shell as root)
	go func() {
		for {
			lc, err := l.Accept()
			if err != nil {
				return
			}
			go forward(client, lc)
		}
	}()
	_ = client.Wait()
	_ = l.Close()
	_ = os.Remove(socket)
	stopContainer()
	return nil
}

func forward(client *ssh.Client, lc net.Conn) {
	defer lc.Close()
	ch, reqs, err := client.OpenChannel(clientChannel, nil)
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch, lc)
		_ = ch.CloseWrite()
	}()
	_, _ = io.Copy(lc, ch)
	wg.Wait()
}

// stopContainer ends the container: PID 1 is Docker's init, which passes the
// signal on to the main process (zdev sandbox idle).
func stopContainer() { _ = syscall.Kill(1, syscall.SIGTERM) }

// ErrNotConnected means the in-container zdev found no relay.
var ErrNotConnected = errors.New("host side not connected: zdev commands work in this sandbox only while its `zdev claude` session runs on the host")

// Call runs a zdev command on the host through the relay socket and returns
// its exit code. Interrupts are passed on as signals.
func Call(socket string, req Request, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	lc, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return 0, ErrNotConnected
	}
	c, chans, reqs, err := ssh.NewClientConn(lc, "client", clientConfig())
	if err != nil {
		return 0, ErrNotConnected
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	sess.Stdout, sess.Stderr = stdout, stderr
	// Stdin through our own copy: the session's Wait would otherwise wait
	// for stdin to end, and a terminal never does.
	if stdin != nil {
		w, err := sess.StdinPipe()
		if err != nil {
			return 0, err
		}
		go func() { _, _ = io.Copy(w, stdin); _ = w.Close() }()
	}
	payload, _ := json.Marshal(req)
	if err := sess.Start(string(payload)); err != nil {
		return 0, err
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	for {
		select {
		case s := <-sigs:
			name := ssh.SIGTERM
			if s == syscall.SIGINT {
				name = ssh.SIGINT
			}
			_ = sess.Signal(name)
		case err := <-done:
			var exit *ssh.ExitError
			if errors.As(err, &exit) {
				return exit.ExitStatus(), nil
			}
			if err != nil {
				return 1, err
			}
			return 0, nil
		}
	}
}
