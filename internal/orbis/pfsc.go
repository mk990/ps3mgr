package orbis

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
)

const pfscMagic = 0x50465343

// pfscWriter emits the header of a PFSC container. The container stores blocks
// uncompressed, which is what the publishing tools produce for fake packages.
type pfscWriter struct {
	numBlocks  int64
	headerSize int64
	blockSize  int64
}

func newPfscWriter(size int64) *pfscWriter {
	const blockSize = 0x10000
	numBlocks := (size + blockSize - 1) / blockSize
	pointerTableSize := 8 + numBlocks*8
	additional := ((pointerTableSize - 0xFC00) + 0xFFFF) / 0x10000
	headerSize := int64(0x10000)
	if additional > 0 {
		headerSize += blockSize * additional
	}
	return &pfscWriter{numBlocks: numBlocks, headerSize: headerSize, blockSize: blockSize}
}

// size returns the total size of the container including its header.
func (p *pfscWriter) size() int64 { return p.headerSize + p.numBlocks*p.blockSize }

func (p *pfscWriter) writeHeader(w io.Writer) error {
	header := make([]byte, p.headerSize)
	binary.BigEndian.PutUint32(header[0:], pfscMagic)
	binary.LittleEndian.PutUint32(header[4:], 0)
	binary.LittleEndian.PutUint32(header[8:], 6)
	binary.LittleEndian.PutUint32(header[12:], uint32(p.blockSize))
	binary.LittleEndian.PutUint64(header[16:], uint64(p.blockSize))
	binary.LittleEndian.PutUint64(header[24:], 0x400)
	binary.LittleEndian.PutUint64(header[32:], uint64(p.headerSize))
	binary.LittleEndian.PutUint64(header[40:], uint64(p.numBlocks*p.blockSize))
	for i := int64(0); i <= p.numBlocks; i++ {
		offset := 0x400 + i*8
		if offset+8 > int64(len(header)) {
			return fmt.Errorf("PFSC block table does not fit in the header")
		}
		binary.LittleEndian.PutUint64(header[offset:], uint64(p.headerSize+i*p.blockSize))
	}
	_, err := w.Write(header)
	return err
}

// pfscReader exposes the uncompressed contents of a PFSC container.
type pfscReader struct {
	source     io.ReaderAt
	blockSize  int64
	dataLength int64
	sectorMap  []int64
}

func newPfscReader(source io.ReaderAt) (*pfscReader, error) {
	header := make([]byte, 0x30)
	if _, err := source.ReadAt(header, 0); err != nil {
		return nil, fmt.Errorf("read PFSC header: %w", err)
	}
	if binary.LittleEndian.Uint32(header) != pfscMagic && binary.BigEndian.Uint32(header) != pfscMagic {
		return nil, fmt.Errorf("not a PFSC container")
	}
	blockSize := int64(binary.LittleEndian.Uint32(header[12:]))
	blockSize2 := int64(binary.LittleEndian.Uint64(header[16:]))
	blockOffsets := int64(binary.LittleEndian.Uint64(header[24:]))
	dataLength := int64(binary.LittleEndian.Uint64(header[40:]))
	if blockSize != blockSize2 || blockSize <= 0 {
		return nil, fmt.Errorf("PFSC block size mismatch")
	}
	count := dataLength / blockSize
	if count < 0 || count > 1<<28 {
		return nil, fmt.Errorf("PFSC block count %d is out of range", count)
	}
	table := make([]byte, (count+1)*8)
	if _, err := source.ReadAt(table, blockOffsets); err != nil {
		return nil, fmt.Errorf("read PFSC block table: %w", err)
	}
	sectorMap := make([]int64, count+1)
	for i := range sectorMap {
		sectorMap[i] = int64(binary.LittleEndian.Uint64(table[i*8:]))
	}
	return &pfscReader{source: source, blockSize: blockSize, dataLength: dataLength, sectorMap: sectorMap}, nil
}

func (p *pfscReader) size() int64 { return p.dataLength }

func (p *pfscReader) readSector(index int, out []byte) error {
	if index < 0 || index > len(p.sectorMap)-2 {
		return fmt.Errorf("PFSC sector %d is out of range", index)
	}
	offset := p.sectorMap[index]
	size := p.sectorMap[index+1] - offset
	switch {
	case size == p.blockSize:
		_, err := p.source.ReadAt(out[:p.blockSize], offset)
		return err
	case size > p.blockSize || size <= 2:
		for i := range out[:p.blockSize] {
			out[i] = 0
		}
		return nil
	default:
		// Compressed sector: raw deflate after a two byte zlib header.
		buffer := make([]byte, size-2)
		if _, err := p.source.ReadAt(buffer, offset+2); err != nil {
			return err
		}
		reader := flate.NewReader(bytes.NewReader(buffer))
		defer reader.Close()
		if _, err := io.ReadFull(reader, out[:p.blockSize]); err != nil && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("decompress PFSC sector %d: %w", index, err)
		}
		return nil
	}
}

// ReadAt implements io.ReaderAt over the uncompressed contents.
func (p *pfscReader) ReadAt(out []byte, offset int64) (int, error) {
	if offset < 0 || offset+int64(len(out)) > p.dataLength {
		return 0, fmt.Errorf("PFSC read beyond end of image")
	}
	sector := make([]byte, p.blockSize)
	written := 0
	for written < len(out) {
		current := (offset + int64(written)) / p.blockSize
		into := (offset + int64(written)) % p.blockSize
		if err := p.readSector(int(current), sector); err != nil {
			return written, err
		}
		copied := copy(out[written:], sector[into:])
		written += copied
	}
	return written, nil
}
