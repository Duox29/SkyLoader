// Package winver reads a plugin's Windows VERSIONINFO resource directly from
// disk. It never calls LoadLibrary, so importing an untrusted DLL cannot run
// its code.
package winver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"unicode/utf16"

	"github.com/HerokeyVN/SkyLoader/internal/pluginid"
)

// Info is the normalized plugin manifest from a DLL's VERSIONINFO resource.
type Info struct {
	ID          string
	Version     string
	Name        string
	Author      string
	Description string
}

const (
	resourceTypeVersion = 16
	versionResourceName = 1
)

// Read parses the VERSIONINFO resource of the PE file at path. A missing or
// invalid SkyPluginId is reported as an error; a missing version is allowed and
// yields an empty Version. Callers that only need display metadata can treat a
// missing ID as a legacy plugin.
func Read(path string) (Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	resource, err := resourceData(data, resourceTypeVersion, versionResourceName)
	if err != nil {
		return Info{}, err
	}
	tables, err := parseVersionInfo(resource)
	if err != nil {
		return Info{}, err
	}
	return infoFromTables(tables)
}

func infoFromTables(tables []versionTable) (Info, error) {
	for _, table := range tables {
		id := pluginid.Canonical(table["SkyPluginId"])
		if id == "" {
			continue
		}
		return Info{
			ID:          id,
			Version:     firstNonEmpty(table["Version"], table["ProductVersion"], table["FileVersion"]),
			Name:        firstNonEmpty(table["PluginName"], table["ProductName"]),
			Author:      firstNonEmpty(table["Author"], table["CompanyName"]),
			Description: firstNonEmpty(table["Description"], table["FileDescription"]),
		}, nil
	}
	return Info{}, errors.New("no usable SkyPluginId in VERSIONINFO")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// resourceData locates a resource by numeric type and name, returning the bytes
// of the first language variant.
func resourceData(file []byte, resourceType, resourceName uint16) ([]byte, error) {
	if len(file) < 0x40 || file[0] != 'M' || file[1] != 'Z' {
		return nil, errors.New("not a PE file")
	}
	peOffset := int(binary.LittleEndian.Uint32(file[0x3c:]))
	if peOffset < 0 || peOffset+24 > len(file) || string(file[peOffset:peOffset+4]) != "PE\x00\x00" {
		return nil, errors.New("invalid PE header")
	}

	coff := peOffset + 4
	numberOfSections := int(binary.LittleEndian.Uint16(file[coff+2:]))
	optionalSize := int(binary.LittleEndian.Uint16(file[coff+16:]))
	optional := coff + 20
	if optional+optionalSize > len(file) {
		return nil, errors.New("truncated optional header")
	}

	magic := binary.LittleEndian.Uint16(file[optional:])
	var dataDirectory int
	switch magic {
	case 0x10b: // PE32
		dataDirectory = optional + 96
	case 0x20b: // PE32+
		dataDirectory = optional + 112
	default:
		return nil, errors.New("unknown PE optional header")
	}
	if dataDirectory+3*8 > optional+optionalSize {
		return nil, errors.New("missing resource directory")
	}
	resourceRVA := binary.LittleEndian.Uint32(file[dataDirectory+2*8:])
	resourceSize := binary.LittleEndian.Uint32(file[dataDirectory+2*8+4:])
	if resourceRVA == 0 || resourceSize == 0 {
		return nil, errors.New("no resource directory")
	}

	sections := make([]section, 0, numberOfSections)
	sectionTable := optional + optionalSize
	for i := 0; i < numberOfSections; i++ {
		offset := sectionTable + i*40
		if offset+40 > len(file) {
			return nil, errors.New("truncated section table")
		}
		sections = append(sections, section{
			virtualAddress: binary.LittleEndian.Uint32(file[offset+12:]),
			virtualSize:    binary.LittleEndian.Uint32(file[offset+8:]),
			rawSize:        binary.LittleEndian.Uint32(file[offset+16:]),
			rawPointer:     binary.LittleEndian.Uint32(file[offset+20:]),
		})
	}

	resourceOffset, ok := rvaToOffset(resourceRVA, sections)
	if !ok {
		return nil, errors.New("resource directory is not mapped")
	}
	directoryEnd := resourceOffset + resourceSize
	if directoryEnd > uint32(len(file)) {
		directoryEnd = uint32(len(file))
	}

	// Root: resource type. Second level: resource name. Third level: language.
	first, ok := findResourceEntry(file, resourceOffset, directoryEnd, uint16(resourceType))
	if !ok {
		return nil, errors.New("VERSIONINFO resource type not found")
	}
	second, ok := firstSubdirectory(file, resourceOffset, directoryEnd, first)
	if !ok {
		return nil, errors.New("VERSIONINFO resource name not found")
	}
	language, ok := firstDataEntry(file, resourceOffset, directoryEnd, second)
	if !ok {
		return nil, errors.New("VERSIONINFO language entry not found")
	}
	dataOffset, ok := rvaToOffset(language.offset, sections)
	if !ok {
		return nil, errors.New("VERSIONINFO data is not mapped")
	}
	end := dataOffset + language.size
	if end > uint32(len(file)) {
		return nil, errors.New("truncated VERSIONINFO data")
	}
	return file[dataOffset:end], nil
}

type section struct {
	virtualAddress uint32
	virtualSize    uint32
	rawSize        uint32
	rawPointer     uint32
}

func rvaToOffset(rva uint32, sections []section) (uint32, bool) {
	for _, section := range sections {
		size := section.virtualSize
		if size == 0 {
			size = section.rawSize
		}
		if rva >= section.virtualAddress && rva < section.virtualAddress+size {
			return section.rawPointer + (rva - section.virtualAddress), true
		}
	}
	return 0, false
}

type resourceEntry struct {
	isDirectory bool
	offset      uint32
}

type resourceDataEntry struct {
	isDirectory bool
	offset      uint32
	size        uint32
}

func readResourceEntry(file []byte, base, directoryEnd, entryIndex uint32) (resourceEntry, bool) {
	entryOffset := base + 16 + entryIndex*8
	if entryOffset+8 > directoryEnd || entryOffset+8 > uint32(len(file)) {
		return resourceEntry{}, false
	}
	offset := binary.LittleEndian.Uint32(file[entryOffset+4:])
	return resourceEntry{
		isDirectory: offset&0x80000000 != 0,
		offset:      offset & 0x7fffffff,
	}, true
}

func directoryCounts(file []byte, base, directoryEnd uint32) (named, ids uint16, ok bool) {
	if base+16 > directoryEnd || base+16 > uint32(len(file)) {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint16(file[base+12:]), binary.LittleEndian.Uint16(file[base+14:]), true
}

func entryName(file []byte, base, entryIndex uint32) (uint32, bool) {
	entryOffset := base + 16 + entryIndex*8
	if entryOffset+4 > uint32(len(file)) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(file[entryOffset:]), true
}

func findResourceEntry(file []byte, base, directoryEnd uint32, id uint16) (resourceEntry, bool) {
	named, ids, ok := directoryCounts(file, base, directoryEnd)
	if !ok {
		return resourceEntry{}, false
	}
	total := uint32(named) + uint32(ids)
	for i := uint32(0); i < total; i++ {
		name, ok := entryName(file, base, i)
		if !ok {
			return resourceEntry{}, false
		}
		if name&0x80000000 != 0 {
			continue // named resources are irrelevant for VERSIONINFO
		}
		if uint16(name) == id {
			return readResourceEntry(file, base, directoryEnd, i)
		}
	}
	return resourceEntry{}, false
}

func firstSubdirectory(file []byte, root, directoryEnd uint32, parent resourceEntry) (resourceEntry, bool) {
	if !parent.isDirectory {
		return resourceEntry{}, false
	}
	base := root + parent.offset
	named, ids, ok := directoryCounts(file, base, directoryEnd)
	if !ok {
		return resourceEntry{}, false
	}
	total := uint32(named) + uint32(ids)
	if total == 0 {
		return resourceEntry{}, false
	}
	return readResourceEntry(file, base, directoryEnd, 0)
}

// firstDataEntry reads the first language entry under a resource-name
// directory. Resource data entries are offsets relative to the resource root,
// not RVAs.
func firstDataEntry(file []byte, root, directoryEnd uint32, parent resourceEntry) (resourceDataEntry, bool) {
	entry, ok := firstSubdirectory(file, root, directoryEnd, parent)
	if !ok || entry.isDirectory {
		return resourceDataEntry{}, false
	}
	dataOffset := root + entry.offset
	if dataOffset+16 > uint32(len(file)) {
		return resourceDataEntry{}, false
	}
	return resourceDataEntry{
		offset: binary.LittleEndian.Uint32(file[dataOffset:]),
		size:   binary.LittleEndian.Uint32(file[dataOffset+4:]),
	}, true
}

// versionTable is one StringTable (for example 040904B0) from VERSIONINFO.
type versionTable map[string]string

func parseVersionInfo(data []byte) ([]versionTable, error) {
	root, ok := parseBlock(data, 0, len(data))
	if !ok {
		return nil, errors.New("invalid VS_VERSIONINFO")
	}
	if root.key != "VS_VERSION_INFO" {
		return nil, fmt.Errorf("unexpected VERSIONINFO key %q", root.key)
	}
	var tables []versionTable
	for _, child := range root.children {
		if child.key != "StringFileInfo" {
			continue
		}
		for _, tableBlock := range child.children {
			table := versionTable{}
			for _, stringBlock := range tableBlock.children {
				if stringBlock.value != "" || stringBlock.key != "" {
					table[stringBlock.key] = stringBlock.value
				}
			}
			if len(table) > 0 {
				tables = append(tables, table)
			}
		}
	}
	if len(tables) == 0 {
		return nil, errors.New("VERSIONINFO has no StringFileInfo")
	}
	return tables, nil
}

type versionBlock struct {
	key      string
	value    string
	children []versionBlock
}

func align4(value int) int {
	return (value + 3) &^ 3
}

func parseBlock(data []byte, start, limit int) (versionBlock, bool) {
	if start < 0 || limit > len(data) || start+6 > limit {
		return versionBlock{}, false
	}
	length := int(binary.LittleEndian.Uint16(data[start:]))
	valueLength := int(binary.LittleEndian.Uint16(data[start+2:]))
	valueType := binary.LittleEndian.Uint16(data[start+4:])
	if length < 6 || start+length > limit {
		return versionBlock{}, false
	}
	end := start + length

	keyStart := start + 6
	keyEnd := keyStart
	for keyEnd+1 < end && !(data[keyEnd] == 0 && data[keyEnd+1] == 0) {
		keyEnd += 2
	}
	if keyEnd+1 >= end {
		return versionBlock{}, false
	}
	block := versionBlock{key: decodeUTF16(data[keyStart:keyEnd])}

	valueStart := align4(keyEnd + 2)
	valueBytes := 0
	if valueType == 1 {
		valueBytes = valueLength * 2
	} else {
		valueBytes = valueLength
	}
	if valueStart+valueBytes > end {
		valueBytes = 0
	}
	if valueBytes > 0 {
		rawValue := data[valueStart : valueStart+valueBytes]
		switch valueType {
		case 1:
			block.value = decodeUTF16(rawValue)
		case 0:
			// VS_FIXEDFILEINFO is binary; the plugin's fields live in strings.
		}
	}

	childStart := align4(valueStart + valueBytes)
	for childStart+6 <= end {
		childLength := int(binary.LittleEndian.Uint16(data[childStart:]))
		if childLength == 0 {
			break
		}
		child, ok := parseBlock(data, childStart, end)
		if !ok {
			break
		}
		block.children = append(block.children, child)
		childStart = align4(childStart + childLength)
	}
	return block, true
}

func decodeUTF16(data []byte) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	values := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		values = append(values, binary.LittleEndian.Uint16(data[i:]))
	}
	for len(values) > 0 && values[len(values)-1] == 0 {
		values = values[:len(values)-1]
	}
	return string(utf16.Decode(values))
}
