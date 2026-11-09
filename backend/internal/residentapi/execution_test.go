package residentapi

import (
	"errors"
	"reflect"
	"testing"
)

func TestExecutionObservationIsDetachedValue(t *testing.T) {
	original := Execution{RequestID: "request", Exit: ExitStatus{Known: true, Code: -7}, WorkError: "output_limit"}
	copy := original
	copy.RequestID, copy.Exit.Code, copy.WorkError = "changed", 99, "changed"
	if original.RequestID != "request" || original.Exit.Code != -7 || original.WorkError != "output_limit" {
		t.Fatal("copy changed original observation", original)
	}
	var _ int32 = original.Exit.Code
}

func TestExecutionObservationContainsOnlyMetadata(t *testing.T) {
	want := map[string]reflect.Type{
		"RequestID": reflect.TypeFor[string](), "Phase": reflect.TypeFor[string](),
		"Profile": reflect.TypeFor[string](), "CommandID": reflect.TypeFor[string](),
		"CommandPhase": reflect.TypeFor[string](), "Exit": reflect.TypeFor[ExitStatus](),
		"Stop": reflect.TypeFor[string](), "Terminal": reflect.TypeFor[bool](),
		"CommandCleanup": reflect.TypeFor[string](), "OwnerCleanup": reflect.TypeFor[string](),
		"GatewayCleanup": reflect.TypeFor[string](), "CollectionEnded": reflect.TypeFor[bool](),
		"ResultReleased": reflect.TypeFor[bool](), "WorkError": reflect.TypeFor[string](),
	}
	typ := reflect.TypeFor[Execution]()
	if typ.NumField() != len(want) {
		t.Fatal("observation acquired fields outside retained metadata")
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type != want[field.Name] {
			t.Fatal("unexpected observation field", field.Name, field.Type)
		}
	}
	exit := reflect.TypeFor[ExitStatus]()
	if exit.NumField() != 2 || exit.Field(0).Name != "Known" || exit.Field(0).Type != reflect.TypeFor[bool]() || exit.Field(1).Name != "Code" || exit.Field(1).Type != reflect.TypeFor[int32]() {
		t.Fatal("exit observation is no longer reference-free metadata", exit)
	}
}

func TestExecutionReadErrorsAreStableValues(t *testing.T) {
	for code, want := range map[ReadError]string{ErrNotRead: "not_read", ErrInvalid: "invalid", ErrNotFound: "not_found"} {
		var err error = code
		var got ReadError
		if err.Error() != want || !errors.Is(err, code) || !errors.As(err, &got) || got != code {
			t.Fatal("read error changed", code, err)
		}
	}
	if errors.Is(ErrNotRead, ErrNotFound) || errors.Is(ErrInvalid, ErrNotFound) {
		t.Fatal("distinct read failures compare equal")
	}
}
