//go:build !darwin || !cgo

package hostkeychain

func storeSeed(string, []byte) error  { return ErrUnsupported }
func loadSeed(string) ([]byte, error) { return nil, ErrUnsupported }
func deleteSeed(string) error         { return ErrUnsupported }
