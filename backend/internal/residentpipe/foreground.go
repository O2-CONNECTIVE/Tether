package residentpipe

import (
	"strconv"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
)

// The input dialect is fixed by trusted composition, never by a stream, flag,
// policy, environment variable or remote request. Both use the same owner loop.
type inputDialect uint8

const (
	jsonInput inputDialect = iota
	foregroundInput
)

type controlKind uint8

const (
	controlIntent controlKind = iota
	controlStatus
	controlClose
)

type controlCommand struct {
	kind   controlKind
	intent Intent
}

func decodeControl(line []byte, dialect inputDialect) (controlCommand, error) {
	if dialect == jsonInput {
		r, err := Decode(line)
		// The product's JSON on cannot name a length until #410 (#399).
		if err == nil && r.Intent == "on" {
			r.StandbyFor = residentmac.DefaultStandbyFor
		}
		return controlCommand{kind: controlIntent, intent: r}, err
	}
	if dialect != foregroundInput || validateFrame(line) != nil {
		return controlCommand{}, ErrProtocol
	}
	text := string(line[:len(line)-1])
	switch text {
	case "status":
		return controlCommand{kind: controlStatus}, nil
	case "close":
		return controlCommand{kind: controlClose}, nil
	}
	fields := strings.Split(text, " ")
	// on and extend name how long standby stays on, in hours spelled exactly
	// as one of the listed lengths (#399): no other number is parsed, so none
	// can wrap around into a listed one. allow and deny switch the local
	// command-run allowance, which lasts until standby turns off.
	intent, ok := map[string]string{"on": "on", "extend": "extend", "off": "off", "allow": "allow-run", "deny": "deny-run"}[fields[0]]
	var standbyFor time.Duration
	if ok && (intent == "on" || intent == "extend") {
		if len(fields) == 4 {
			standbyFor, ok = map[string]time.Duration{"2": 2 * time.Hour, "4": 4 * time.Hour, "8": 8 * time.Hour, "12": 12 * time.Hour, "24": 24 * time.Hour}[fields[3]]
			fields = fields[:3]
		} else {
			ok = false
		}
	}
	if len(fields) != 3 || !ok {
		return controlCommand{}, ErrProtocol
	}
	var numbers [2]uint64
	for i, field := range fields[1:] {
		n, err := strconv.ParseUint(field, 10, 64)
		if err != nil || n > MaxInteger || strconv.FormatUint(n, 10) != field {
			return controlCommand{}, ErrProtocol
		}
		numbers[i] = n
	}
	if numbers[0] == 0 {
		return controlCommand{}, ErrProtocol
	}
	return controlCommand{kind: controlIntent, intent: Intent{1, numbers[0], numbers[1], intent, standbyFor}}, nil
}
