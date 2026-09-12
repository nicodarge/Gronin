package guardtest

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
	"go.uber.org/zap"
)

// Server is one etcd member embedded in the test process. Its client and peer URLs are
// unix sockets the test created, which specs/002-guard/research.md §1 establishes is not
// network in Principle VI's sense: the socket exists identically on every machine that
// runs the suite. A fixed port would make the outcome depend on what else is running, and
// a server binary found on PATH on what is installed.
type Server struct {
	etcd *embed.Etcd
	dir  string

	mu      sync.Mutex
	proxies int
}

// StartServer starts a member and stops it at cleanup.
//
// Its sockets live under a directory of its own rather than t.TempDir(): a socket path
// longer than the kernel's sun_path limit fails to bind, and t.TempDir() names grow
// with the test's name.
func StartServer(t *testing.T) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("", "etcd") //nolint:usetesting // see above: the path must stay short
	if err != nil {
		t.Fatal(err)
	}

	cfg := embed.NewConfig()
	cfg.Name = "guardtest"
	cfg.Dir = filepath.Join(dir, "data")
	client := socketURL(dir, "client.sock")
	peer := socketURL(dir, "peer.sock")
	cfg.ListenClientUrls, cfg.AdvertiseClientUrls = []url.URL{client}, []url.URL{client}
	cfg.ListenPeerUrls, cfg.AdvertisePeerUrls = []url.URL{peer}, []url.URL{peer}
	cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)
	// Pinned rather than defaulted: the shortest lease the server grants follows from
	// them, and the contract's C12 case asks for less than that on purpose.
	cfg.TickMs, cfg.ElectionMs = 100, 1000
	cfg.ZapLoggerBuilder = embed.NewZapLoggerBuilder(zap.NewNop())

	started, err := embed.StartEtcd(cfg)
	if err != nil {
		_ = os.RemoveAll(dir)
		t.Fatalf("starting the embedded etcd server: %v", err)
	}
	server := &Server{etcd: started, dir: dir}
	t.Cleanup(func() {
		started.Close()
		_ = os.RemoveAll(dir)
	})

	select {
	case <-started.Server.ReadyNotify():
	case err := <-started.Err():
		t.Fatalf("the embedded etcd server failed while starting: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the embedded etcd server was not ready within 30s")
	}
	return server
}

// MinimumTTL is the shortest lease this server grants, whatever is asked: one and a half
// election timeouts, rounded up to a whole second.
const MinimumTTL = 2 * time.Second

func socketURL(dir, name string) url.URL {
	return url.URL{Scheme: "unix", Path: filepath.Join(dir, name)}
}

// Endpoint is the server's own client URL.
func (s *Server) Endpoint() string {
	u := socketURL(s.dir, "client.sock")
	return u.String()
}

// Unreachable is an endpoint where nothing listens.
func (s *Server) Unreachable() string {
	u := socketURL(s.dir, "nothing.sock")
	return u.String()
}

// Listeners is every address the server opened, read from the server itself.
func (s *Server) Listeners() []net.Addr {
	var addrs []net.Addr
	for _, listener := range s.etcd.Clients {
		addrs = append(addrs, listener.Addr())
	}
	for _, peer := range s.etcd.Peers {
		addrs = append(addrs, peer.Addr())
	}
	return addrs
}

// Proxy starts a proxy of its own in front of the server, closed at cleanup.
func (s *Server) Proxy(t *testing.T) *Proxy {
	t.Helper()
	s.mu.Lock()
	s.proxies++
	name := fmt.Sprintf("proxy-%d.sock", s.proxies)
	s.mu.Unlock()
	return startProxy(t, filepath.Join(s.dir, name), filepath.Join(s.dir, "client.sock"))
}

// NewClient returns a client of the endpoints, closed at cleanup. It logs nothing: the
// suite's output is scanned for secrets, and a client's own log would be noise in it.
func NewClient(t *testing.T, endpoints ...string) *clientv3.Client {
	t.Helper()
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("a client of %v: %v", endpoints, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
