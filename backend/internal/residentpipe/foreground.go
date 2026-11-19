package residentpipe

import (
	"strconv"
	"strings"
	"time"
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
	// allow and deny switch the local command-run allowance. allow names its
	// length in hours, spelled exactly as one of the listed lengths (#399):
	// no other number is parsed, so none can wrap around into a listed one.
	intent, ok := map[string]string{"on": "on", "off": "off", "allow": "allow-run", "deny": "deny-run"}[fields[0]]
	var runFor time.Duration
	if ok && intent == "allow-run" && len(fields) == 4 {
		runFor, ok = map[string]time.Duration{"2": 2 * time.Hour, "4": 4 * time.Hour, "8": 8 * time.Hour, "12": 12 * time.Hour, "24": 24 * time.Hour}[fields[3]]
		fields = fields[:3]
	} else if intent == "allow-run" {
		ok = false
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
	return controlCommand{kind: controlIntent, intent: Intent{1, numbers[0], numbers[1], intent, runFor}}, nil
}
