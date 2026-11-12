package macagent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// ponytail: one earlier file (name.1) is kept; add numbered rotation if a
// longer history is needed. The record lives in a directory the commands
// themselves can write, so it is a view for the person, not tamper evidence.
const maxRunLogBytes = 1 << 20

type runLogEntry struct {
	At      string `json:"at"`
	Dir     string `json:"dir"`
	Command string `json:"command"`
}

func appendRunLog(name, dir string, command []byte) (retErr error) {
	if info, err := os.Lstat(name); err == nil && info.Size() > maxRunLogBytes {
		if err := os.Rename(name, name+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	// The opened file must be the regular file at that name, not a link target.
	if at, err := os.Lstat(name); err != nil || !at.Mode().IsRegular() || !os.SameFile(at, info) {
		return errors.New("macagent: run history is not a regular file")
	}
	line, err := json.Marshal(runLogEntry{At: time.Now().UTC().Format(time.RFC3339), Dir: dir, Command: string(command)})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	n, writeErr := f.Write(line)
	if writeErr == nil && n != len(line) {
		writeErr = io.ErrShortWrite
	}
	return writeErr
}
