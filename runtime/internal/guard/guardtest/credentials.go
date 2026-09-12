package guardtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.etcd.io/etcd/client/pkg/v3/transport"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// Credentials the authenticated server below expects. Documentation values: nothing here
// is a credential of any deployment.
const (
	Username = "gronin"
	Password = "REPLACE_ME"
	// Prefix is what the user above may read and write, and nothing else.
	Prefix = "gronin/"
)

// StartAuthenticatedServer starts a member with authentication enabled and a user that
// may read and write everything. A client reaching it with no credentials, or the wrong
// ones, is refused by the server rather than by the adapter.
func StartAuthenticatedServer(t *testing.T) *Server {
	t.Helper()
	server := StartServer(t)

	root := NewClient(t, server.Endpoint())
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	// A root user has to exist before authentication can be enabled, and it is granted
	// the built-in root role by name.
	if _, err := root.UserAdd(ctx, "root", Password); err != nil {
		t.Fatalf("adding the root user: %v", err)
	}
	if _, err := root.UserGrantRole(ctx, "root", "root"); err != nil {
		t.Fatalf("granting the root role: %v", err)
	}
	if _, err := root.UserAdd(ctx, Username, Password); err != nil {
		t.Fatalf("adding %s: %v", Username, err)
	}
	if _, err := root.RoleAdd(ctx, "runtime"); err != nil {
		t.Fatalf("adding the runtime role: %v", err)
	}
	readWrite := clientv3.PermissionType(clientv3.PermReadWrite)
	if _, err := root.RoleGrantPermission(ctx, "runtime",
		Prefix, clientv3.GetPrefixRangeEnd(Prefix), readWrite); err != nil {
		t.Fatalf("granting the runtime role: %v", err)
	}
	if _, err := root.UserGrantRole(ctx, Username, "runtime"); err != nil {
		t.Fatalf("granting %s the runtime role: %v", Username, err)
	}
	if _, err := root.AuthEnable(ctx); err != nil {
		t.Fatalf("enabling authentication: %v", err)
	}
	return server
}

// StartTLSServer starts a member whose client listener is TLS over a unix socket and
// which requires a client certificate. The files it returns are what a deployment's
// coordination configuration would name; all three are generated here and none is
// committed.
//
// Over a unix socket the client verifies the server certificate against the socket's
// file name rather than a host name (specs/002-guard/research.md §2), so the server
// certificate names both the file and the path it is at.
func StartTLSServer(t *testing.T) (*Server, guard.TLSFiles) {
	t.Helper()
	dir := t.TempDir()
	authority, authorityKey := selfSigned(t)

	// The socket the member will listen on is not known until it starts, and the name it
	// is verified under is the file's, which is fixed.
	files := guard.TLSFiles{
		CA:   filepath.Join(dir, "ca.pem"),
		Cert: filepath.Join(dir, "client.pem"),
		Key:  filepath.Join(dir, "client-key.pem"),
	}
	serverCert := filepath.Join(dir, "server.pem")
	serverKey := filepath.Join(dir, "server-key.pem")

	writePEM(t, files.CA, "CERTIFICATE", authority.Raw)
	issue(t, authority, authorityKey, []string{"client.sock", "localhost"}, files.Cert, files.Key)
	issue(t, authority, authorityKey, []string{"client.sock", "localhost"}, serverCert, serverKey)

	server := startServer(t, serverOptions{
		scheme: "unixs",
		tls: transport.TLSInfo{
			CertFile:       serverCert,
			KeyFile:        serverKey,
			TrustedCAFile:  files.CA,
			ClientCertAuth: true,
		},
	})
	return server, files
}

// selfSigned is the test's own certificate authority.
func selfSigned(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "guardtest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, key
}

// issue writes one certificate and its key, signed by the authority above.
func issue(
	t *testing.T, authority *x509.Certificate, authorityKey *ecdsa.PrivateKey,
	names []string, certPath, keyPath string,
) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 96))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, authority, &key.PublicKey, authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, certPath, "CERTIFICATE", der)

	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyPath, "EC PRIVATE KEY", encoded)
}

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
