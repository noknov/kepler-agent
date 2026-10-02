package postgresjson

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalPreservesNumbersEscapesAndSanitizesKeys(t *testing.T) {
	input := map[string]any{"n": uint64(18446744073709551615), "actual\x00key": "nul\x00value", "literal": `\u0000`, "slashes": "\\\x00"}
	data, err := Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("invalid JSON: %s", data)
	}
	var got struct {
		N       uint64 `json:"n"`
		Literal string `json:"literal"`
		Slashes string `json:"slashes"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	var actual string
	_ = json.Unmarshal(fields["actual�key"], &actual)
	if got.N != input["n"] || got.Literal != `\u0000` || got.Slashes != "\\�" || actual != "nul�value" {
		t.Fatalf("round trip: %+v", got)
	}
}

func FuzzMarshalString(f *testing.F) {
	for _, seed := range []string{"\x00", `\u0000`, "\\\x00", "\\\\\x00", "你好", "\"\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		// encoding/json replaces invalid UTF-8; use its round trip as the oracle.
		original, _ := json.Marshal(value)
		var normalized string
		_ = json.Unmarshal(original, &normalized)
		encoded, err := Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if want := strings.ReplaceAll(normalized, "\x00", "�"); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
}
