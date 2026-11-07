//go:build darwin && cgo && ci_actual_producer

// Compiler-overlay fixture only. Never a production trust configuration API.
package agenttransport

import (
	"crypto/x509"
	"net"
	"net/http"
	"net/url"
	"strconv"
)

// CIProducerTrust is confined to one exact numeric-loopback WSS endpoint.
// It neither creates authentication evidence nor changes native dial ownership.
type CIProducerTrust struct {
	endpoint string
	roots    *x509.CertPool
}

func NewCIProducerTrust(endpoint string, der []byte) (*CIProducerTrust, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "wss" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() != "127.0.0.1" {
		return nil, ErrConfig
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || host != "127.0.0.1" || n < 1 || n > 65535 {
		return nil, ErrConfig
	}
	if _, err = parseEndpoint(endpoint); err != nil {
		return nil, ErrConfig
	}
	// x509 certificates retain slices into DER. Own the bytes before the
	// bootstrap caller clears its buffer; CertPool does not make that copy.
	cert, err := x509.ParseCertificate(append([]byte(nil), der...))
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || cert.CheckSignatureFrom(cert) != nil {
		return nil, ErrConfig
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &CIProducerTrust{endpoint: endpoint, roots: roots}, nil
}

func (f *CIProducerTrust) transport(own acquirer) http.RoundTripper {
	// Start from the actual owned transport. Only the per-instance trust pool
	// differs; normal certificate/hostname validation and native dial remain.
	tr := nativeTransport(own).(*http.Transport)
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.RootCAs = f.roots.Clone()
	return tr
}
func (f *CIProducerTrust) BindClient(c *Client) error {
	if f == nil || f.roots == nil || c == nil {
		return ErrConfig
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.endpoint != f.endpoint {
		return ErrConfig
	}
	c.transport = f.transport
	return nil
}
func (f *CIProducerTrust) BindResident(c *ResidentClient) error {
	if f == nil || f.roots == nil || c == nil {
		return ErrConfig
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.cfg.Endpoint != f.endpoint {
		return ErrConfig
	}
	c.transport = f.transport
	return nil
}
