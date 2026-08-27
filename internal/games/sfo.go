package games

import (
	"encoding/binary"
	"fmt"
	"strings"
)

const sfoMagic = 0x46535000

// ParseSFO parses the string values in a PlayStation PARAM.SFO file.
func ParseSFO(data []byte) (map[string]string, error) {
	if len(data) < 20 || binary.LittleEndian.Uint32(data[:4]) != sfoMagic {
		return nil, fmt.Errorf("invalid PARAM.SFO header")
	}
	keyOffset := int(binary.LittleEndian.Uint32(data[8:12]))
	dataOffset := int(binary.LittleEndian.Uint32(data[12:16]))
	count := int(binary.LittleEndian.Uint32(data[16:20]))
	if count > 4096 || keyOffset < 20 || dataOffset < keyOffset || keyOffset > len(data) || dataOffset > len(data) {
		return nil, fmt.Errorf("invalid PARAM.SFO offsets")
	}
	result := make(map[string]string)
	for i := 0; i < count; i++ {
		pos := 20 + i*16
		if pos+16 > len(data) {
			return nil, fmt.Errorf("truncated PARAM.SFO index")
		}
		keyRel := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		length := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		valueRel := int(binary.LittleEndian.Uint32(data[pos+12 : pos+16]))
		keyPos, valuePos := keyOffset+keyRel, dataOffset+valueRel
		if keyPos < 0 || keyPos >= len(data) || valuePos < 0 || length < 0 || valuePos+length > len(data) {
			return nil, fmt.Errorf("PARAM.SFO entry outside file")
		}
		keyEnd := keyPos
		for keyEnd < len(data) && data[keyEnd] != 0 {
			keyEnd++
		}
		if keyEnd == len(data) {
			return nil, fmt.Errorf("unterminated PARAM.SFO key")
		}
		key := string(data[keyPos:keyEnd])
		value := strings.TrimRight(string(data[valuePos:valuePos+length]), "\x00")
		if key != "" {
			result[key] = value
		}
	}
	return result, nil
}
