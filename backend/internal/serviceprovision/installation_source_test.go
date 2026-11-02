package serviceprovision

import (
	"context"
	"encoding/json"
	"go/parser"
	gotoken "go/token"
	"reflect"
	"strings"
	"testing"
)

const sourceAnchor = `{"protocol":"rescue-installation-anchor/1","serviceName":"SyntheticService","recordPath":"C:\\日本\\record.json","digestPath":"C:\\日本\\record.sha256","volume":{"root":"C:\\","filesystem":"NTFS","serial":"7"},"readMs":"1","cleanupMs":"120000"}` + "\n"

func TestInstallationSourcePublicPairRetainsOriginalAndSafeLiterals(t *testing.T) {
	goSource, err := EncodeInstallationAnchorSource([]byte(sourceAnchor))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(gotoken.NewFileSet(), "generated.go", goSource, parser.AllErrors); err != nil {
		t.Fatal(err)
	}
	record := clientProjectionRecord()
	encoded, err := EncodeInstallationSourcePair([]byte(sourceAnchor), goSource, record, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	var pair struct{ RustSeedSource string }
	if err := json.Unmarshal(encoded, &pair); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pair.RustSeedSource, `\u{65e5}\u{672c}`) {
		t.Fatal("unsafe Unicode literal")
	}

	if !strings.Contains(pair.RustSeedSource, "read_ms: 1,") || !strings.Contains(pair.RustSeedSource, "cleanup_ms: 120000,") {
		t.Fatal("bounds lost")
	}
}

func TestInstallationFixedEntryRemainsUnconfigured(t *testing.T) {
	cfg := fixedEvaluationConfig()
	if cfg.Anchor != (Anchor{}) {
		t.Fatal("committed build contains installation identity")
	}
	cfg.Anchor = Anchor{}
	if !reflect.DeepEqual(cfg, PreviewExecutionConfig{}) {
		t.Fatal("installation source activated execution limits")
	}
	if err := RunFixed(context.Background()); err == nil {
		t.Fatal("default fixed entry activated")
	}
}
