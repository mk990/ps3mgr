package orbis

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// SFO value types.
const (
	SFOUtf8Special uint16 = 0x0004
	SFOUtf8        uint16 = 0x0204
	SFOInteger     uint16 = 0x0404
)

// SFOValue is a single param.sfo entry.
type SFOValue struct {
	Name string
	Type uint16
	// Text holds the value for the UTF-8 types.
	Text string
	// Number holds the value for integer entries.
	Number int32
	// Max is the reserved size of the value in the data table.
	Max int
}

func (v SFOValue) length() int {
	switch v.Type {
	case SFOInteger:
		return 4
	case SFOUtf8:
		return len(v.Text) + 1
	default:
		return len(v.Text)
	}
}

func (v SFOValue) maxLength() int {
	if v.Type == SFOInteger {
		return 4
	}
	return v.Max
}

func (v SFOValue) bytes() []byte {
	switch v.Type {
	case SFOInteger:
		out := make([]byte, 4)
		binary.LittleEndian.PutUint32(out, uint32(v.Number))
		return out
	case SFOUtf8:
		return append([]byte(v.Text), 0)
	default:
		return []byte(v.Text)
	}
}

// ParamSFO is a parsed param.sfo file.
type ParamSFO struct {
	Values []SFOValue
}

// Get returns the value with the given key.
func (p *ParamSFO) Get(name string) (SFOValue, bool) {
	for _, v := range p.Values {
		if v.Name == name {
			return v, true
		}
	}
	return SFOValue{}, false
}

// String returns the textual form of a key, or the empty string when absent.
func (p *ParamSFO) String(name string) string {
	value, ok := p.Get(name)
	if !ok {
		return ""
	}
	if value.Type == SFOInteger {
		return fmt.Sprintf("0x%08x", uint32(value.Number))
	}
	return value.Text
}

// Set inserts or replaces a value, keeping the table sorted by key.
func (p *ParamSFO) Set(value SFOValue) {
	for i, existing := range p.Values {
		if existing.Name == value.Name {
			p.Values[i] = value
			return
		}
	}
	p.Values = append(p.Values, value)
	sort.Slice(p.Values, func(i, j int) bool { return p.Values[i].Name < p.Values[j].Name })
}

// SetText inserts or replaces a UTF-8 value, keeping the original reserved size
// when the key already exists.
func (p *ParamSFO) SetText(name, text string) error {
	max := len(text) + 1
	if existing, ok := p.Get(name); ok && existing.Type != SFOInteger {
		if len(text)+1 > existing.Max {
			return fmt.Errorf("param.sfo value %s is limited to %d bytes, got %d", name, existing.Max-1, len(text))
		}
		max = existing.Max
	}
	p.Set(SFOValue{Name: name, Type: SFOUtf8, Text: text, Max: max})
	return nil
}

// SetInteger inserts or replaces an integer value.
func (p *ParamSFO) SetInteger(name string, number int32) {
	p.Set(SFOValue{Name: name, Type: SFOInteger, Number: number, Max: 4})
}

// ParseSFO reads a param.sfo file.
func ParseSFO(data []byte) (*ParamSFO, error) {
	start := 0
	if len(data) >= 4 && binary.BigEndian.Uint32(data) == 0x53434543 {
		start = 0x800
	}
	if len(data) < start+0x14 {
		return nil, fmt.Errorf("param.sfo is truncated")
	}
	body := data[start:]
	if binary.BigEndian.Uint32(body) != 0x00505346 {
		return nil, fmt.Errorf("param.sfo is missing the SFO magic")
	}
	keyTable := int(binary.LittleEndian.Uint32(body[8:]))
	dataTable := int(binary.LittleEndian.Uint32(body[12:]))
	count := int(binary.LittleEndian.Uint32(body[16:]))
	if count < 0 || count > 4096 {
		return nil, fmt.Errorf("param.sfo declares an implausible entry count: %d", count)
	}
	sfo := &ParamSFO{Values: make([]SFOValue, 0, count)}
	for i := 0; i < count; i++ {
		offset := 0x14 + i*0x10
		if offset+0x10 > len(body) {
			return nil, fmt.Errorf("param.sfo index table is truncated")
		}
		keyOffset := int(binary.LittleEndian.Uint16(body[offset:]))
		format := binary.LittleEndian.Uint16(body[offset+2:])
		length := int(binary.LittleEndian.Uint32(body[offset+4:]))
		max := int(binary.LittleEndian.Uint32(body[offset+8:]))
		dataOffset := int(binary.LittleEndian.Uint32(body[offset+12:]))
		nameStart := keyTable + keyOffset
		if nameStart < 0 || nameStart >= len(body) {
			return nil, fmt.Errorf("param.sfo key offset is out of range")
		}
		end := bytes.IndexByte(body[nameStart:], 0)
		if end < 0 {
			return nil, fmt.Errorf("param.sfo key is not terminated")
		}
		name := string(body[nameStart : nameStart+end])
		valueStart := dataTable + dataOffset
		if valueStart < 0 || valueStart+length > len(body) || length < 0 {
			return nil, fmt.Errorf("param.sfo value %q is out of range", name)
		}
		switch format {
		case SFOInteger:
			if length < 4 {
				return nil, fmt.Errorf("param.sfo integer %q is truncated", name)
			}
			sfo.Values = append(sfo.Values, SFOValue{Name: name, Type: format, Number: int32(binary.LittleEndian.Uint32(body[valueStart:])), Max: 4})
		case SFOUtf8:
			text := body[valueStart : valueStart+length]
			if len(text) > 0 {
				text = text[:len(text)-1]
			}
			sfo.Values = append(sfo.Values, SFOValue{Name: name, Type: format, Text: string(text), Max: max})
		case SFOUtf8Special:
			sfo.Values = append(sfo.Values, SFOValue{Name: name, Type: format, Text: string(body[valueStart : valueStart+length]), Max: max})
		default:
			return nil, fmt.Errorf("param.sfo value %q has unknown type %#04x", name, format)
		}
	}
	return sfo, nil
}

func (p *ParamSFO) keyTableOffset() int { return 0x14 + len(p.Values)*0x10 }

func (p *ParamSFO) layout() (dataTableOffset, fileSize int) {
	sort.Slice(p.Values, func(i, j int) bool { return p.Values[i].Name < p.Values[j].Name })
	keyTableSize, dataSize := 0, 0
	for _, v := range p.Values {
		keyTableSize += len(v.Name) + 1
		dataSize += v.maxLength()
	}
	dataTableOffset = p.keyTableOffset() + keyTableSize
	if remainder := dataTableOffset % 4; remainder != 0 {
		dataTableOffset += 4 - remainder
	}
	return dataTableOffset, dataTableOffset + dataSize
}

// Size returns the serialized size of the file.
func (p *ParamSFO) Size() int {
	_, size := p.layout()
	return size
}

// Serialize writes the param.sfo file.
func (p *ParamSFO) Serialize() []byte {
	dataTableOffset, fileSize := p.layout()
	out := make([]byte, fileSize)
	binary.BigEndian.PutUint32(out, 0x00505346)
	binary.LittleEndian.PutUint32(out[4:], 0x101)
	binary.LittleEndian.PutUint32(out[8:], uint32(p.keyTableOffset()))
	binary.LittleEndian.PutUint32(out[12:], uint32(dataTableOffset))
	binary.LittleEndian.PutUint32(out[16:], uint32(len(p.Values)))
	keyOffset, dataOffset := 0, 0
	for i, v := range p.Values {
		index := 0x14 + i*0x10
		binary.LittleEndian.PutUint16(out[index:], uint16(keyOffset))
		binary.LittleEndian.PutUint16(out[index+2:], v.Type)
		binary.LittleEndian.PutUint32(out[index+4:], uint32(v.length()))
		binary.LittleEndian.PutUint32(out[index+8:], uint32(v.maxLength()))
		binary.LittleEndian.PutUint32(out[index+12:], uint32(dataOffset))
		copy(out[p.keyTableOffset()+keyOffset:], v.Name)
		copy(out[dataTableOffset+dataOffset:], v.bytes())
		keyOffset += len(v.Name) + 1
		dataOffset += v.maxLength()
	}
	return out
}
