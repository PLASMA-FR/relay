// Package identity manages the installation's Ed25519 TLS identity.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type Identity struct {
	Fingerprint string
	certificate tls.Certificate
}

func LoadOrCreate(stateDir string) (*Identity, error) {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "identity.pem")
	info, err := os.Lstat(path)
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("identity %s must be a regular private file (chmod 600)", path)
	}
	var key ed25519.PrivateKey
	if errors.Is(err, os.ErrNotExist) {
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		der, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return nil, e
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(e, os.ErrExist) {
			return LoadOrCreate(stateDir)
		}
		if e != nil {
			return nil, e
		}
		e = pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
	} else if err != nil {
		return nil, err
	} else {
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, errors.New("invalid identity PEM")
		}
		parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return nil, e
		}
		var ok bool
		key, ok = parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("identity key must be Ed25519")
		}
	}
	now := time.Now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Relay"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Identity{Fingerprint: Fingerprint(cert), certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}}, nil
}

func Fingerprint(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(h[:])
}

// TLSConfig requires proof of possession. Authorization is deliberately performed
// by the protocol per operation: unauthenticated discovery may only read Hello.
// Send independently pins the full server fingerprint before sending an offer.
func (i *Identity) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{i.certificate}, ClientAuth: tls.RequireAnyClientCert, InsecureSkipVerify: true, NextProtos: []string{"relay/1"}, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) != 1 {
			return errors.New("expected one Relay identity certificate")
		}
		if _, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey); !ok {
			return errors.New("Relay requires Ed25519 identity")
		}
		return nil
	}}
}
