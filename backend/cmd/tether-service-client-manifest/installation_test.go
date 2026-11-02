package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

const syntheticAnchor = `{"protocol":"rescue-installation-anchor/1","serviceName":"SyntheticService","recordPath":"C:\\record.json","digestPath":"C:\\record.sha256","volume":{"root":"C:\\","filesystem":"NTFS","serial":"7"},"readMs":"1000","cleanupMs":"1000"}` + "\n"

func TestInstallationCommandTwoPhaseSourcePair(t *testing.T) {
	inputs := map[string][]byte{"anchor": []byte(syntheticAnchor), "record": commandRecord(t), "build": []byte(syntheticServiceBuild)}
	open := func(name string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(inputs[name])), nil }
	var first, pair, diag bytes.Buffer
	if execute([]string{"--anchor-source", "anchor"}, &first, &diag, open) != 0 {
		t.Fatal("anchor phase refused", diag.String())
	}
	if !strings.Contains(first.String(), "func fixedInstallationAnchor() Anchor") || strings.Contains(first.String(), "SHA256") {
		t.Fatal("wrong acyclic anchor source")
	}
	inputs["selected"] = first.Bytes()
	args := []string{"--paired-anchor", "anchor", "--selected-go-anchor", "selected", "--record", "record", "--service-image", "build"}
	if execute(args, &pair, &diag, open) != 0 {
		t.Fatal("pair refused", diag.String())
	}
	var got struct{ Protocol, Trust, GoAnchorSource, RustSeedSource string }
	if err := json.Unmarshal(pair.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Protocol != "rescue-installation-source-pair/1" || got.Trust != "unverified" || got.GoAnchorSource != first.String() {
		t.Fatal("uncorrelated source pair")
	}
	for _, want := range []string{"use super::Seed;", "pub(super) fn seed() -> Option<Seed>", `service_name: "SyntheticService"`, `operator_sid: "S-1-5-21-1-2-3-1001"`, `guest_sid: "S-1-5-21-1-2-3-1002"`, "serial: 7", "record: &[", "digest: &["} {
		if !strings.Contains(got.RustSeedSource, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if !bytes.Equal(rustByteField(t, got.RustSeedSource, "record"), inputs["record"]) {
		t.Fatal("record bytes were reserialized")
	}
	// Independently fixed SHA256 of the committed synthetic fixture, not producer output.
	if string(rustByteField(t, got.RustSeedSource, "digest")) != "e8fda48851aca0163a900879775e2ff8489057aa20a76782542e72ad2944e7ce\n" {
		t.Fatal("wrong original-record digest")
	}
	var again bytes.Buffer
	if execute(args, &again, &diag, open) != 0 || !bytes.Equal(again.Bytes(), pair.Bytes()) {
		t.Fatal("pair is not deterministic")
	}
	inputs["selected"] = append(append([]byte{}, first.Bytes()...), '\n')
	pair.Reset()
	if execute(args, &pair, &diag, open) == 0 || pair.Len() != 0 {
		t.Fatal("edited selected source accepted")
	}
}

func TestInstallationCommandRejectsAmbiguousAnchorAndModes(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate":    strings.Replace(syntheticAnchor, `"protocol":`, `"protocol":"rescue-installation-anchor/1","protocol":`, 1),
		"unknown":      strings.Replace(syntheticAnchor, "{", `{"operatorSid":"S-1-5-18",`, 1),
		"case":         strings.Replace(syntheticAnchor, "serviceName", "ServiceName", 1),
		"partial":      strings.Replace(syntheticAnchor, `"readMs":"1000",`, "", 1),
		"version":      strings.Replace(syntheticAnchor, "anchor/1", "anchor/2", 1),
		"zero":         strings.Replace(syntheticAnchor, `"readMs":"1000"`, `"readMs":"0"`, 1),
		"negative":     strings.Replace(syntheticAnchor, `"readMs":"1000"`, `"readMs":"-1"`, 1),
		"overflow":     strings.Replace(syntheticAnchor, `"cleanupMs":"1000"`, `"cleanupMs":"18446744073709551616"`, 1),
		"limit":        strings.Replace(syntheticAnchor, `"cleanupMs":"1000"`, `"cleanupMs":"120001"`, 1),
		"leading-zero": strings.Replace(syntheticAnchor, `"readMs":"1000"`, `"readMs":"01000"`, 1),
		"number":       strings.Replace(syntheticAnchor, `"readMs":"1000"`, `"readMs":1000`, 1),
		"serial":       strings.Replace(syntheticAnchor, `"serial":"7"`, `"serial":"07"`, 1),
		"crlf":         strings.ReplaceAll(syntheticAnchor, "\n", "\r\n"),
		"trailing":     syntheticAnchor + "\n",
		"huge":         strings.Repeat(" ", 8193),
		"same-leaf":    strings.Replace(syntheticAnchor, "record.sha256", "record.json", 1),
		"unicode":      strings.Replace(syntheticAnchor, "record.json", `\ud800.json`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var out, diag bytes.Buffer
			r := &fixtureReader{Reader: strings.NewReader(input)}
			if execute([]string{"--anchor-source", "anchor"}, &out, &diag, func(string) (io.ReadCloser, error) { return r, nil }) == 0 || out.Len() != 0 || r.closes != 1 {
				t.Fatal("ambiguous authority emitted source")
			}
		})
	}
	for _, args := range [][]string{
		{"--anchor-source", "a", "--anchor-source", "b"},
		{"--anchor-source", "a", "--record", "r", "--service-image", "b"},
		{"--record", "r", "--service-image", "b", "--selected-go-anchor", "s"},
		{"--record", "r", "--service-image", "b", "--paired-anchor", "a"},
		{"--record", "r", "--service-image", "b", "--paired-anchor", "a", "--selected-go-anchor", "s", "--check-projection", "c"},
	} {
		var out, diag bytes.Buffer
		opens := 0
		if execute(args, &out, &diag, func(string) (io.ReadCloser, error) { opens++; return nil, nil }) == 0 || opens != 0 || out.Len() != 0 {
			t.Fatal("invalid mode read inputs", args)
		}
	}
}

func TestInstallationCommandRejectsInconsistentSourcePair(t *testing.T) {
	for _, fault := range []string{"service", "volume", "build", "record", "go-source", "anchor-close", "selected-close"} {
		t.Run(fault, func(t *testing.T) {
			inputs := map[string][]byte{"a": []byte(syntheticAnchor), "r": commandRecord(t), "b": []byte(syntheticServiceBuild)}
			if fault == "service" {
				inputs["a"] = bytes.ReplaceAll(inputs["a"], []byte("SyntheticService"), []byte("OtherService"))
			}
			if fault == "volume" {
				inputs["a"] = bytes.ReplaceAll(inputs["a"], []byte(`"serial":"7"`), []byte(`"serial":"8"`))
			}
			var source, out, diag bytes.Buffer
			readers := []*fixtureReader{}
			open := func(name string) (io.ReadCloser, error) {
				r := &fixtureReader{Reader: bytes.NewReader(inputs[name])}
				if fault == "anchor-close" && name == "a" || fault == "selected-close" && name == "s" {
					r.closeErr = io.ErrClosedPipe
				}
				readers = append(readers, r)
				return r, nil
			}
			// Phase one succeeds independently of the selected record.
			phaseOpen := func(name string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(inputs[name])), nil }
			if execute([]string{"--anchor-source", "a"}, &source, &diag, phaseOpen) != 0 {
				t.Fatal("phase one")
			}
			inputs["s"] = source.Bytes()
			switch fault {
			case "build":
				inputs["b"] = []byte("other")
			case "record":
				inputs["r"] = append(inputs["r"], ' ')
			case "go-source":
				inputs["s"] = append(append([]byte{}, inputs["s"]...), '\n')
			}
			if execute([]string{"--paired-anchor", "a", "--selected-go-anchor", "s", "--record", "r", "--service-image", "b"}, &out, &diag, open) == 0 || out.Len() != 0 {
				t.Fatal("bad pair emitted")
			}
			for _, r := range readers {
				if r.closes != 1 {
					t.Fatal("reader not closed exactly once")
				}
			}
		})
	}
}

func rustByteField(t *testing.T, source, name string) []byte {
	t.Helper()
	_, tail, ok := strings.Cut(source, name+": &[")
	if !ok {
		t.Fatal("missing byte field", name)
	}
	body, _, ok := strings.Cut(tail, "]")
	if !ok {
		t.Fatal("unterminated byte field", name)
	}
	var out []byte
	for _, value := range strings.Split(body, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		n, err := strconv.ParseUint(value, 10, 8)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, byte(n))
	}
	return out
}
