package etcd

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// dialBound is how long establishing a connection may take. Every call the adapter makes
// afterwards is bounded by the context it is handed; this bounds only the dial, which
// happens before any of them.
const dialBound = 5 * time.Second

// NewClient is an etcd client from a deployment's coordination configuration. A
// deployment authenticates with a username and password, with a TLS client certificate,
// or with both; which one is the etcd deployment's choice
// (specs/002-guard/contracts/cli.md).
//
// It logs nothing of its own: the deployment's log passes the redactor, and a client
// writing beside it would not.
func NewClient(cfg guard.EtcdConfig) (*clientv3.Client, error) {
	options := clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: dialBound,
		Username:    cfg.Username,
		Password:    cfg.Password,
		Logger:      zap.NewNop(),
	}
	if cfg.TLS != nil {
		transport, err := clientTLS(*cfg.TLS)
		if err != nil {
			return nil, err
		}
		options.TLS = transport
	}
	client, err := clientv3.New(options)
	if err != nil {
		return nil, fmt.Errorf("%w: connecting to etcd at %v: %w", guard.ErrUnavailable, cfg.Endpoints, err)
	}
	return client, nil
}

func clientTLS(files guard.TLSFiles) (*tls.Config, error) {
	transport := &tls.Config{MinVersion: tls.VersionTLS12}
	if files.CA != "" {
		authority, err := os.ReadFile(files.CA) //nolint:gosec // a path the deployment configured
		if err != nil {
			return nil, fmt.Errorf("reading the etcd certificate authority: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(authority) {
			return nil, fmt.Errorf("%s holds no certificate", files.CA)
		}
		transport.RootCAs = pool
	}
	if files.Cert == "" && files.Key == "" {
		return transport, nil
	}
	pair, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		return nil, fmt.Errorf("reading the etcd client certificate: %w", err)
	}
	transport.Certificates = []tls.Certificate{pair}
	return transport, nil
}
