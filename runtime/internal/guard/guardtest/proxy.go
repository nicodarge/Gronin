package guardtest

import (
	"context"
	"net"
	"sync"
	"testing"
)

// Proxy forwards a unix socket to the server's. It can hold — stop forwarding without
// closing anything, which is a backend that has stopped answering — and it can sever.
// The two are different failures and are tested separately: a severed connection fails
// at once, so only a held one shows whether a call is bounded (C4, SC-112).
type Proxy struct {
	path     string
	target   string
	listener net.Listener

	mu      sync.Mutex
	held    bool
	severed bool
	closed  bool
	resumed chan struct{}
	conns   map[net.Conn]struct{}
}

func startProxy(t *testing.T, path, target string) *Proxy {
	t.Helper()
	var config net.ListenConfig
	listener, err := config.Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("a proxy on %s: %v", path, err)
	}
	p := &Proxy{
		path: path, target: target, listener: listener,
		resumed: make(chan struct{}), conns: map[net.Conn]struct{}{},
	}
	go p.accept()
	t.Cleanup(p.close)
	return p
}

// Endpoint is the URL a client reaches the server through this proxy by.
func (p *Proxy) Endpoint() string { return "unix://" + p.path }

// Hold stops forwarding in both directions. Connections stay open, and what arrives
// meanwhile waits, as a backend that has frozen.
func (p *Proxy) Hold() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = true
}

// Sever closes every connection and every new one, as a backend that has gone.
func (p *Proxy) Sever() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.severed = true
	p.closeConnsLocked()
}

// Resume undoes Hold and Sever.
func (p *Proxy) Resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.severed = false
	if p.held {
		p.held = false
		close(p.resumed)
		p.resumed = make(chan struct{})
	}
}

func (p *Proxy) accept() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.severed || p.closed {
			p.mu.Unlock()
			_ = conn.Close()
			continue
		}
		p.mu.Unlock()

		var dialer net.Dialer
		upstream, err := dialer.DialContext(context.Background(), "unix", p.target)
		if err != nil {
			_ = conn.Close()
			continue
		}
		p.mu.Lock()
		p.conns[conn], p.conns[upstream] = struct{}{}, struct{}{}
		p.mu.Unlock()
		go p.pipe(upstream, conn)
		go p.pipe(conn, upstream)
	}
}

func (p *Proxy) pipe(dst, src net.Conn) {
	defer func() { _ = dst.Close(); _ = src.Close() }()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if !p.forwarding() {
				return
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// forwarding blocks while the proxy is held, and reports whether to carry on.
func (p *Proxy) forwarding() bool {
	p.mu.Lock()
	for p.held && !p.closed {
		wait := p.resumed
		p.mu.Unlock()
		<-wait
		p.mu.Lock()
	}
	ok := !p.severed && !p.closed
	p.mu.Unlock()
	return ok
}

func (p *Proxy) close() {
	p.mu.Lock()
	p.closed = true
	p.closeConnsLocked()
	if p.held {
		p.held = false
		close(p.resumed)
	}
	p.mu.Unlock()
	_ = p.listener.Close()
}

func (p *Proxy) closeConnsLocked() {
	for conn := range p.conns {
		_ = conn.Close()
	}
	p.conns = map[net.Conn]struct{}{}
}
