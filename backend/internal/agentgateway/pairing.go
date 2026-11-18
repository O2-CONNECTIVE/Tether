package agentgateway

import (
	"net/http"
	"time"
)

const (
	PairPath             = "/tether-agent/v1/pair"
	PairTokenMaxLifetime = 600 * time.Second
	MaxPairBody          = 4 << 10
)

type HostEntry struct {
	Label      string
	Key        []byte
	Principals []string
	Paired     bool
}
type PairRecord struct {
	HostLabel       string
	HostKey         []byte
	Issuer, Subject string
	RegisteredAt    time.Time
}
type PairingConfig struct {
	Verifier                       *Verifier
	Endpoint, RegistrationEndpoint string
	Records                        []PairRecord
	Save                           func([]PairRecord) error
	Disconnect                     func(string)
}

func PairTranscript(endpoint string, hostKey []byte, tokenHash [32]byte) []byte { return nil }

func (g *Gateway) ConfigureHosts(hosts []HostEntry) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hosts = make(map[string][]byte, len(hosts))
	for _, h := range hosts {
		g.hosts[h.Label] = append([]byte(nil), h.Key...)
	}
	return nil
}

func (g *Gateway) HostKey(label string) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]byte(nil), g.hosts[label]...)
}

func (g *Gateway) ConfigurePairing(cfg PairingConfig) error {
	g.pairing = &cfg
	for _, record := range cfg.Records {
		g.hosts[record.HostLabel] = append([]byte(nil), record.HostKey...)
	}
	return nil
}
func (g *Gateway) ServePair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	http.Error(w, "", http.StatusMethodNotAllowed)
}
