package winver

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func utf16Bytes(value string) []byte {
	units := utf16.Encode([]rune(value))
	out := make([]byte, 0, (len(units)+1)*2)
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return append(out, 0, 0)
}

func makeVersionBlock(key string, valueType, valueLength uint16, value []byte, children ...[]byte) []byte {
	header := make([]byte, 6)
	keyBytes := utf16Bytes(key)
	out := append(header, keyBytes...)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	out = append(out, value...)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	for _, child := range children {
		out = append(out, child...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))
	binary.LittleEndian.PutUint16(out[2:], valueLength)
	binary.LittleEndian.PutUint16(out[4:], valueType)
	return out
}

func stringBlock(key, value string) []byte {
	return makeVersionBlock(key, 1, uint16(len(utf16.Encode([]rune(value)))+1), utf16Bytes(value))
}

func TestParseVersionInfo(t *testing.T) {
	stringsBlock := makeVersionBlock("StringFileInfo", 1, 0, nil,
		makeVersionBlock("040904B0", 1, 0, nil,
			stringBlock("SkyPluginId", "example.author.plugin"),
			stringBlock("Version", "1.4.0"),
			stringBlock("PluginName", "Example Plugin"),
			stringBlock("Author", "Example Author"),
			stringBlock("Description", "An example"),
		),
	)
	root := makeVersionBlock("VS_VERSION_INFO", 0, 52, make([]byte, 52), stringsBlock)

	tables, err := parseVersionInfo(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 {
		t.Fatalf("tables = %d", len(tables))
	}
	if tables[0]["SkyPluginId"] != "example.author.plugin" {
		t.Fatalf("id = %q", tables[0]["SkyPluginId"])
	}
	if tables[0]["Version"] != "1.4.0" {
		t.Fatalf("version = %q", tables[0]["Version"])
	}
	if tables[0]["Description"] != "An example" {
		t.Fatalf("description = %q", tables[0]["Description"])
	}
}

func TestInvalidVersionInfo(t *testing.T) {
	if _, err := parseVersionInfo([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected invalid block to fail")
	}
}

func TestInfoFromTablesAllowsMissingVersion(t *testing.T) {
	info, err := infoFromTables([]versionTable{
		{"ProductName": "No identity"},
		{"SkyPluginId": "example.author.plugin", "PluginName": "Example Plugin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "example.author.plugin" {
		t.Fatalf("id = %q", info.ID)
	}
	if info.Version != "" {
		t.Fatalf("version = %q", info.Version)
	}
	if info.Name != "Example Plugin" {
		t.Fatalf("name = %q", info.Name)
	}
}

func TestInfoFromTablesRequiresID(t *testing.T) {
	if _, err := infoFromTables([]versionTable{{"Version": "1.0.0"}}); err == nil {
		t.Fatal("expected missing SkyPluginId to fail")
	}
}
