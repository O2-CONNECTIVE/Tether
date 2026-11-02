// tether-service-client-manifest is an offline packaging pair producer.
// It neither installs its output nor authenticates a native service/client.
package main

import (
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceprovision"
)

const maximumServiceBuildBytes int64 = 256 * 1024 * 1024

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, func(name string) (io.ReadCloser, error) { return os.Open(name) }))
}

// The reader seam belongs to this offline command's tests, not native trust.
func execute(args []string, output, diagnostic io.Writer, open func(string) (io.ReadCloser, error)) int {
	if err := produce(args, output, open); err != nil {
		if diagnostic != nil {
			_, _ = fmt.Fprintln(diagnostic, "SERVICE_CLIENT_PROJECTION_REJECTED")
		}
		return 2
	}
	return 0 // Pairing/serialization only; output remains blocked and unverified.
}

func produce(args []string, output io.Writer, open func(string) (io.ReadCloser, error)) error {
	if output == nil || open == nil {
		return serviceprovision.ErrRecord
	}
	flags := flag.NewFlagSet("tether-service-client-manifest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var recordPath, imagePath, checkPath string
	var anchorSource, pairedAnchor, selectedGo string
	for name, target := range map[string]*string{"anchor-source": &anchorSource, "paired-anchor": &pairedAnchor, "selected-go-anchor": &selectedGo} {
		flags.Func(name, "offline installation source input; never a runtime override", func(value string) error {
			if *target != "" || value == "" {
				return serviceprovision.ErrRecord
			}
			*target = value
			return nil
		})
	}
	flags.Func("record", "reviewed nonsecret service record", func(value string) error {
		if recordPath != "" || value == "" {
			return serviceprovision.ErrRecord
		}
		recordPath = value
		return nil
	})
	flags.Func("service-image", "independently selected service build", func(value string) error {
		if imagePath != "" || value == "" {
			return serviceprovision.ErrRecord
		}
		imagePath = value
		return nil
	})
	flags.Func("check-projection", "check an exact candidate artifact without emitting output", func(value string) error {
		if checkPath != "" || value == "" {
			return serviceprovision.ErrRecord
		}
		checkPath = value
		return nil
	})
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return serviceprovision.ErrRecord
	}
	if anchorSource != "" {
		if pairedAnchor != "" || selectedGo != "" || recordPath != "" || imagePath != "" || checkPath != "" {
			return serviceprovision.ErrRecord
		}
		input, err := readCandidate(open, anchorSource, serviceprovision.InstallationAnchorBytes)
		if err != nil {
			return err
		}
		encoded, err := serviceprovision.EncodeInstallationAnchorSource(input)
		if err != nil {
			return err
		}
		return writeCandidate(output, encoded)
	}
	if recordPath == "" || imagePath == "" || (pairedAnchor == "") != (selectedGo == "") || pairedAnchor != "" && checkPath != "" {
		return serviceprovision.ErrRecord
	}
	var anchorInput, selectedSource []byte
	if pairedAnchor != "" {
		var err error
		anchorInput, err = readCandidate(open, pairedAnchor, serviceprovision.InstallationAnchorBytes)
		if err != nil {
			return err
		}
		// Reject malformed authority before reading the record or selected binary.
		if _, err = serviceprovision.EncodeInstallationAnchorSource(anchorInput); err != nil {
			return err
		}
		selectedSource, err = readCandidate(open, selectedGo, 32768)
		if err != nil {
			return err
		}
	}
	recordBytes, err := readCandidate(open, recordPath, serviceprovision.RecordBytes)
	if err != nil {
		return err
	}
	defer clear(recordBytes)
	record, err := serviceprovision.DecodeRecord(recordBytes)
	if err != nil {
		return err
	}
	limit := record.Profile.SupervisorImage.MaxBytes
	if limit <= 0 || limit > maximumServiceBuildBytes {
		return serviceprovision.ErrRecord
	}
	digest, err := hashBuild(open, imagePath, limit)
	if err != nil {
		return err
	}
	if pairedAnchor != "" {
		encoded, err := serviceprovision.EncodeInstallationSourcePair(anchorInput, selectedSource, recordBytes, digest)
		if err != nil {
			return err
		}
		return writeCandidate(output, encoded)
	}
	encoded, err := serviceprovision.EncodeUntrustedClientProjection(recordBytes, digest)
	if err != nil {
		return err
	}
	if checkPath != "" {
		artifact, err := readCandidate(open, checkPath, int64(len(encoded)))
		if err != nil {
			return err
		}
		return serviceprovision.VerifyUntrustedClientProjection(recordBytes, digest, artifact)
	}
	return writeCandidate(output, encoded)
}

func writeCandidate(output io.Writer, encoded []byte) error {
	n, err := output.Write(encoded)
	if err == nil && n != len(encoded) {
		return io.ErrShortWrite
	}
	return err
}

func readCandidate(open func(string) (io.ReadCloser, error), name string, limit int64) (data []byte, result error) {
	reader, err := open(name)
	if reader == nil {
		return nil, serviceprovision.ErrRecord
	}
	defer func() { result = errors.Join(result, reader.Close()) }()
	if err != nil {
		return nil, serviceprovision.ErrRecord
	}
	data, err = io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return nil, serviceprovision.ErrRecord
	}
	return data, nil
}

func hashBuild(open func(string) (io.ReadCloser, error), name string, limit int64) (digest [32]byte, result error) {
	reader, err := open(name)
	if reader == nil {
		return digest, serviceprovision.ErrRecord
	}
	defer func() { result = errors.Join(result, reader.Close()) }()
	if err != nil {
		return digest, serviceprovision.ErrRecord
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(reader, limit+1))
	if err != nil || n == 0 || n > limit {
		return digest, serviceprovision.ErrRecord
	}
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
