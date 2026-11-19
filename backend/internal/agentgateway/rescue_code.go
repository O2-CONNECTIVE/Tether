package agentgateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The rescue connection code (docs/rescue-connection-start.md, 接続コード and
// 回数の制限). maxRescueCodes, rescueFailureLimit with rescueFailureWindow,
// and MaxPrincipals are the premises of 推測への耐性 there: changing any of
// them means redoing that calculation first.
const (
	rescueCodeLifetime  = 5 * time.Minute
	maxRescueCodes      = 100 // N, the valid codes at once
	rescueFailureLimit  = 5   // failed matches per principal per window
	rescueFailureWindow = 10 * time.Minute
)

var rescueCodeSpace = big.NewInt(1_000_000_000_000) // 12 decimal digits

var (
	// errRescueCodeInvalid covers an unknown, spent, expired or ended code
	// alike; the caller answers all of them as one "invalid code".
	errRescueCodeInvalid = errors.New("agentgateway: rescue code is not valid")
	errRescueCodesFull   = errors.New("agentgateway: too many valid rescue codes")
	errRescueLocked      = errors.New("agentgateway: too many failed rescue codes")
)

type rescueCode struct {
	reception string
	expires   time.Time
}

// rescueCodes holds the live codes by a keyed digest only, and each
// principal's recent failures by label and time only.
type rescueCodes struct {
	mu       sync.Mutex
	clock    func() time.Time
	random   io.Reader
	key      [32]byte // per start: a digest seen once tells nothing after a restart
	codes    map[[32]byte]rescueCode
	failures map[string][]time.Time
}

func newRescueCodes(clock func() time.Time) *rescueCodes {
	s := &rescueCodes{clock: clock, random: rand.Reader, codes: map[[32]byte]rescueCode{}, failures: map[string][]time.Time{}}
	_, _ = rand.Read(s.key[:]) // never fails; it crashes instead
	return s
}

// issue makes a new code for reception, valid for rescueCodeLifetime. It
// refuses with errRescueCodesFull while maxRescueCodes are valid.
func (s *rescueCodes) issue(reception string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	for d, c := range s.codes {
		if !now.Before(c.expires) {
			delete(s.codes, d)
		}
	}
	if len(s.codes) >= maxRescueCodes {
		return "", errRescueCodesFull
	}
	for {
		n, err := rand.Int(s.random, rescueCodeSpace) // uniform by rejection
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%012d", n.Uint64())
		d := s.digest(code)
		if _, live := s.codes[d]; !live {
			s.codes[d] = rescueCode{reception: reception, expires: now.Add(rescueCodeLifetime)}
			return code, nil
		}
	}
}

// redeem spends code for p and returns its reception. Only a rescue_operator
// may try, and is refused before it takes a failure. The failure is taken
// before the match and given back only on success, all under one lock, so
// concurrent tries of one principal cannot pass the limit together. A
// success keeps the earlier failures.
func (s *rescueCodes) redeem(p *principal, code string) (string, error) {
	if p == nil || !p.rescue {
		return "", ErrRefused
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	failures, locked := recentRescueFailures(s.failures[p.label], now)
	s.failures[p.label] = failures
	if locked {
		return "", errRescueLocked
	}
	d := s.digest(strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, code))
	c, ok := s.codes[d]
	delete(s.codes, d)
	if !ok || !now.Before(c.expires) {
		s.failures[p.label] = append(failures, now)
		return "", errRescueCodeInvalid
	}
	return c.reception, nil
}

// recentRescueFailures drops failures older than the window and reports a
// lock. Once they reach the limit it keeps all of them until the newest is a
// window old, so the principal stops for a full window after the last one.
func recentRescueFailures(at []time.Time, now time.Time) ([]time.Time, bool) {
	if len(at) >= rescueFailureLimit {
		newest := at[0]
		for _, t := range at {
			if t.After(newest) {
				newest = t
			}
		}
		if now.Sub(newest) < rescueFailureWindow {
			return at, true
		}
	}
	kept := at[:0]
	for _, t := range at {
		if now.Sub(t) < rescueFailureWindow {
			kept = append(kept, t)
		}
	}
	return kept, false
}

// end drops the code of a reception that ended.
func (s *rescueCodes) end(reception string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for d, c := range s.codes {
		if c.reception == reception {
			delete(s.codes, d)
		}
	}
}

func (s *rescueCodes) digest(code string) [32]byte {
	m := hmac.New(sha256.New, s.key[:])
	m.Write([]byte(code))
	var d [32]byte
	m.Sum(d[:0])
	return d
}
