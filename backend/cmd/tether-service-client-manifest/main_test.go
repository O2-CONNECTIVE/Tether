package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

const syntheticServiceBuild = "synthetic service build\n"

func commandRecord(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/service_record.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestManifestCommandProducesBlockedPairFromActualSelectedBytes(t *testing.T) {
	record := commandRecord(t)
	opens := []string{}
	open := func(name string) (io.ReadCloser, error) {
		opens = append(opens, name)
		if name == "record" {
			return io.NopCloser(bytes.NewReader(record)), nil
		}
		if name == "build" {
			return io.NopCloser(bytes.NewBufferString(syntheticServiceBuild)), nil
		}
		t.Fatalf("unexpected source %s", name)
		return nil, nil
	}
	var out, diag bytes.Buffer
	if code := execute([]string{"--record", "record", "--service-image", "build"}, &out, &diag, open); code != 0 {
		t.Fatalf("code=%d diagnostic=%s", code, diag.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(record)
	binary := sha256.Sum256([]byte(syntheticServiceBuild))
	if got["activation"] != "blocked" || got["trust"] != "unverified" || got["recordSHA256"] != hex.EncodeToString(sum[:]) || got["serviceBuildSHA256"] != hex.EncodeToString(binary[:]) || len(opens) != 2 || diag.Len() != 0 {
		t.Fatalf("wrong artifact: %s", out.String())
	}
}

type fixtureReader struct {
	io.Reader
	closeErr error
	closes   int
}

func (r *fixtureReader) Close() error { r.closes++; return r.closeErr }

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("private fixture detail") }

type failedWriter struct{ short bool }

func (w failedWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("private write detail")
}

func TestManifestCommandRejectsOverridesBeforeReading(t *testing.T) {
	for _, args := range [][]string{
		nil, {"--record", "record"}, {"--record", "record", "--service-image", "build", "extra"},
		{"--record", "record", "--record", "again", "--service-image", "build"},
		{"--record", "record", "--service-image", "build", "--service-image", "again"},
		{"--record", "record", "--service-image", "build", "--service-sid", "S-1-5-18"},
		{"--record", "record", "--service-image", "build", "--digest", "00"},
		{"--record", "record", "--service-image", "build", "--endpoint", "arbitrary"},
	} {
		var out, diag bytes.Buffer
		opens := 0
		code := execute(args, &out, &diag, func(string) (io.ReadCloser, error) { opens++; return nil, nil })
		if code == 0 || opens != 0 || out.Len() != 0 || diag.String() != "SERVICE_CLIENT_PROJECTION_REJECTED\n" {
			t.Fatalf("args %v: code=%d opens=%d", args, code, opens)
		}
	}
}

func TestManifestCommandBoundsFailuresAndClosesEveryAcquiredReader(t *testing.T) {
	for _, fault := range []string{"wrong-build", "empty-build", "large-build", "large-record", "bad-record", "record-read", "build-read", "record-close", "build-close", "record-open", "build-open", "partial-open", "nil-reader"} {
		t.Run(fault, func(t *testing.T) {
			record := &fixtureReader{Reader: bytes.NewReader(commandRecord(t))}
			build := &fixtureReader{Reader: bytes.NewBufferString(syntheticServiceBuild)}
			switch fault {
			case "wrong-build":
				build.Reader = strings.NewReader("different selected build")
			case "empty-build":
				build.Reader = strings.NewReader("")
			case "large-build":
				build.Reader = strings.NewReader(strings.Repeat("x", 1025))
			case "large-record":
				record.Reader = strings.NewReader(strings.Repeat(" ", 256*1024+1))
			case "bad-record":
				record.Reader = strings.NewReader("{}")
			case "record-read":
				record.Reader = failedReader{}
			case "build-read":
				build.Reader = failedReader{}
			case "record-close":
				record.closeErr = errors.New("private close detail")
			case "build-close":
				build.closeErr = errors.New("private close detail")
			}
			recordAcquired, buildAcquired := false, false
			open := func(name string) (io.ReadCloser, error) {
				if name == "record" {
					if fault == "record-open" {
						return nil, errors.New("private path")
					}
					if fault == "nil-reader" {
						return nil, nil
					}
					recordAcquired = true
					if fault == "partial-open" {
						return record, errors.New("partial fixture")
					}
					return record, nil
				}
				if name != "build" {
					t.Fatal("unexpected input")
				}
				if fault == "build-open" {
					return nil, errors.New("private path")
				}
				buildAcquired = true
				return build, nil
			}
			var out, diag bytes.Buffer
			if code := execute([]string{"--record", "record", "--service-image", "build"}, &out, &diag, open); code == 0 || out.Len() != 0 || diag.String() != "SERVICE_CLIENT_PROJECTION_REJECTED\n" {
				t.Fatalf("failure became output: %s %s", out.String(), diag.String())
			}
			expectedRecord, expectedBuild := 0, 0
			if recordAcquired {
				expectedRecord = 1
			}
			if buildAcquired {
				expectedBuild = 1
			}
			if record.closes != expectedRecord || build.closes != expectedBuild {
				t.Fatalf("closes: %d/%d", record.closes, build.closes)
			}
			if strings.HasPrefix(fault, "record-") && fault != "record-open" && buildAcquired {
				t.Fatal("bad record reached build")
			}
		})
	}
}

func TestManifestCommandOutputFailureIsNotSuccessfulArtifact(t *testing.T) {
	for _, writer := range []io.Writer{nil, failedWriter{}, failedWriter{short: true}} {
		record := commandRecord(t)
		open := func(name string) (io.ReadCloser, error) {
			if name == "record" {
				return io.NopCloser(bytes.NewReader(record)), nil
			}
			return io.NopCloser(bytes.NewBufferString(syntheticServiceBuild)), nil
		}
		var diag bytes.Buffer
		if code := execute([]string{"--record", "record", "--service-image", "build"}, writer, &diag, open); code == 0 {
			t.Fatal("unknown output reported success")
		}
	}
}

func TestManifestCommandChecksOnlyExactDerivedArtifact(t *testing.T) {
	record := commandRecord(t)
	var artifact []byte
	open := func(name string) (io.ReadCloser, error) {
		switch name {
		case "record":
			return io.NopCloser(bytes.NewReader(record)), nil
		case "build":
			return io.NopCloser(bytes.NewBufferString(syntheticServiceBuild)), nil
		case "projection":
			return io.NopCloser(bytes.NewReader(artifact)), nil
		default:
			t.Fatal("unknown source")
			return nil, nil
		}
	}
	args := []string{"--record", "record", "--service-image", "build"}
	var generated, diag bytes.Buffer
	if execute(args, &generated, &diag, open) != 0 {
		t.Fatal("fixture generation failed")
	}
	artifact = append([]byte(nil), generated.Bytes()...)
	var out bytes.Buffer
	if execute(append(append([]string(nil), args...), "--check-projection", "projection"), &out, &diag, open) != 0 || out.Len() != 0 {
		t.Fatal("exact source/build artifact was not checked")
	}
	for _, altered := range [][]byte{
		bytes.ReplaceAll(generated.Bytes(), []byte("\n"), []byte("\r\n")),
		bytes.ReplaceAll(generated.Bytes(), []byte(`"blocked"`), []byte(`"enabled"`)),
		bytes.ReplaceAll(generated.Bytes(), []byte("SyntheticService"), []byte("OtherService")),
		append(append([]byte(nil), generated.Bytes()...), '\n'),
	} {
		artifact = altered
		if execute(append(append([]string(nil), args...), "--check-projection", "projection"), &out, &diag, open) == 0 {
			t.Fatal("altered projection accepted")
		}
	}
}
