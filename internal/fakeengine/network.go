package fakeengine

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/fardani235/docker-desk/internal/testguard"
)

// StartTCP serves the API over plain TCP, as a daemon started with
// -H tcp://… does. addr is host:port; port 0 lets the system choose, and
// Host then reports the port that was chosen.
//
// The address is registered with the test guard: a TCP listener the fake
// opened itself is the only network endpoint tests may mutate.
func (s *Server) StartTCP(addr string) error {
	s.tlsConf = nil
	return s.listenTCP(addr)
}

// StartTLS serves the API over TCP with TLS on a loopback port chosen by the
// system, and accepts only clients that present a certificate signed by its
// own CA — a daemon started with --tlsverify. It writes what such a client
// needs into dir under the names Docker uses: ca.pem, cert.pem and key.pem.
func (s *Server) StartTLS(dir string) error {
	conf, err := writeTLSMaterial(dir)
	if err != nil {
		return err
	}
	s.tlsConf = conf
	return s.listenTCP("127.0.0.1:0")
}

func (s *Server) listenTCP(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.tcpAddr = l.Addr().String()
	testguard.Allow("tcp://" + s.tcpAddr)
	if s.tlsConf != nil {
		l = tls.NewListener(l, s.tlsConf)
	}
	return s.serve(l, "")
}

// writeTLSMaterial creates a throwaway CA, a server certificate for the
// loopback addresses and a client certificate, and returns the server side.
func writeTLSMaterial(dir string) (*tls.Config, error) {
	caKey, caCert, caDER, err := newCert(nil, nil, &x509.Certificate{
		Subject:               pkix.Name{CommonName: "docker-desk fake CA"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	})
	if err != nil {
		return nil, err
	}
	srvKey, _, srvDER, err := newCert(caCert, caKey, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "fake-dockerd"},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return nil, err
	}
	cliKey, _, cliDER, err := newCert(caCert, caKey, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "docker-desk test client"},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return nil, err
	}
	cliKeyDER, err := x509.MarshalECPrivateKey(cliKey)
	if err != nil {
		return nil, err
	}
	for name, block := range map[string]*pem.Block{
		"ca.pem":   {Type: "CERTIFICATE", Bytes: caDER},
		"cert.pem": {Type: "CERTIFICATE", Bytes: cliDER},
		"key.pem":  {Type: "EC PRIVATE KEY", Bytes: cliKeyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			return nil, err
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}

var certSerial int64

// newCert issues a certificate from tmpl, signed by the given parent or by
// itself when parent is nil.
func newCert(parent *x509.Certificate, parentKey *ecdsa.PrivateKey, tmpl *x509.Certificate) (*ecdsa.PrivateKey, *x509.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	certSerial++
	tmpl.SerialNumber = big.NewInt(time.Now().UnixNano() + certSerial)
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(24 * time.Hour)
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return key, cert, der, nil
}
