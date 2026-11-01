package agentwire

import (
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrJSON reports input outside the strict JSON subset shared by agentwire
// frames and the MCP adapter.
var ErrJSON = errors.New("agentwire: malformed or non-strict JSON")

const maxJSONDepth = 32

// CheckJSON accepts exactly one JSON value with valid UTF-8, paired surrogate
// escapes, no duplicate object keys at any depth and bounded nesting. null is
// rejected unless allowNull, so a privileged field cannot be cleared silently.
// encoding/json alone would replace invalid text and keep the last duplicate.
func CheckJSON(b []byte, allowNull bool) error {
	if !utf8.Valid(b) {
		return ErrJSON
	}
	s := &scanner{b: b, allowNull: allowNull}
	s.space()
	if !s.value(0) {
		return ErrJSON
	}
	s.space()
	if s.i != len(b) {
		return ErrJSON
	}
	return nil
}

// Fields splits a JSON object into its exact-case members after CheckJSON with
// null allowed. A non-empty allowed list rejects any other member. Unlike struct
// decoding, member matching never folds case, so "Op" cannot shadow "op".
func Fields(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if CheckJSON(raw, true) != nil {
		return nil, ErrJSON
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, ErrJSON
	}
	if len(allowed) > 0 {
		for k := range m {
			if !slices.Contains(allowed, k) {
				return nil, ErrJSON
			}
		}
	}
	return m, nil
}

type scanner struct {
	b         []byte
	i         int
	allowNull bool
}

func (s *scanner) peek() byte {
	if s.i < len(s.b) {
		return s.b[s.i]
	}
	return 0
}

func (s *scanner) space() {
	for {
		switch s.peek() {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

func (s *scanner) value(depth int) bool {
	switch c := s.peek(); {
	case c == '{':
		return depth < maxJSONDepth && s.object(depth+1)
	case c == '[':
		return depth < maxJSONDepth && s.array(depth+1)
	case c == '"':
		_, ok := s.str(false)
		return ok
	case c == 't':
		return s.literal("true")
	case c == 'f':
		return s.literal("false")
	case c == 'n':
		return s.allowNull && s.literal("null")
	case c == '-' || isDigit(c):
		return s.number()
	}
	return false
}

func (s *scanner) literal(word string) bool {
	if len(s.b)-s.i < len(word) || string(s.b[s.i:s.i+len(word)]) != word {
		return false
	}
	s.i += len(word)
	return true
}

func (s *scanner) object(depth int) bool {
	s.i++
	s.space()
	if s.peek() == '}' {
		s.i++
		return true
	}
	seen := map[string]struct{}{}
	for {
		if s.peek() != '"' {
			return false
		}
		key, ok := s.str(true)
		if !ok {
			return false
		}
		if _, dup := seen[key]; dup {
			return false
		}
		seen[key] = struct{}{}
		s.space()
		if s.peek() != ':' {
			return false
		}
		s.i++
		s.space()
		if !s.value(depth) {
			return false
		}
		s.space()
		switch s.peek() {
		case ',':
			s.i++
			s.space()
		case '}':
			s.i++
			return true
		default:
			return false
		}
	}
}

func (s *scanner) array(depth int) bool {
	s.i++
	s.space()
	if s.peek() == ']' {
		s.i++
		return true
	}
	for {
		if !s.value(depth) {
			return false
		}
		s.space()
		switch s.peek() {
		case ',':
			s.i++
			s.space()
		case ']':
			s.i++
			return true
		default:
			return false
		}
	}
}

// str scans one string. Keys are decoded so that "a" and "a" collide.
func (s *scanner) str(decode bool) (string, bool) {
	s.i++
	var out []byte
	for s.i < len(s.b) {
		c := s.b[s.i]
		switch {
		case c == '"':
			s.i++
			return string(out), true
		case c < 0x20:
			return "", false
		case c != '\\':
			if decode {
				out = append(out, c)
			}
			s.i++
			continue
		}
		if s.i+1 >= len(s.b) {
			return "", false
		}
		e := s.b[s.i+1]
		s.i += 2
		var r rune
		switch e {
		case '"', '\\', '/':
			r = rune(e)
		case 'b':
			r = '\b'
		case 'f':
			r = '\f'
		case 'n':
			r = '\n'
		case 'r':
			r = '\r'
		case 't':
			r = '\t'
		case 'u':
			var ok bool
			if r, ok = s.hex4(); !ok {
				return "", false
			}
			if utf16.IsSurrogate(r) {
				if r >= 0xDC00 || s.i+1 >= len(s.b) || s.b[s.i] != '\\' || s.b[s.i+1] != 'u' {
					return "", false
				}
				s.i += 2
				lo, ok := s.hex4()
				if !ok || lo < 0xDC00 || lo > 0xDFFF {
					return "", false
				}
				r = utf16.DecodeRune(r, lo)
			}
		default:
			return "", false
		}
		if decode {
			out = utf8.AppendRune(out, r)
		}
	}
	return "", false
}

func (s *scanner) hex4() (rune, bool) {
	if len(s.b)-s.i < 4 {
		return 0, false
	}
	var r rune
	for _, c := range s.b[s.i : s.i+4] {
		switch {
		case isDigit(c):
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	s.i += 4
	return r, true
}

func (s *scanner) number() bool {
	if s.peek() == '-' {
		s.i++
	}
	switch c := s.peek(); {
	case c == '0':
		s.i++
	case c >= '1' && c <= '9':
		s.digits()
	default:
		return false
	}
	if s.peek() == '.' {
		s.i++
		if !s.digits() {
			return false
		}
	}
	if c := s.peek(); c == 'e' || c == 'E' {
		s.i++
		if c := s.peek(); c == '+' || c == '-' {
			s.i++
		}
		if !s.digits() {
			return false
		}
	}
	return true
}

func (s *scanner) digits() bool {
	start := s.i
	for isDigit(s.peek()) {
		s.i++
	}
	return s.i > start
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
