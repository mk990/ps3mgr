package orbis

import "encoding/binary"

// playgoManifest is the default playgo-manifest.xml for a single chunk package.
var playgoManifest = []byte("\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"utf-8\" standalone=\"yes\"?>\r\n" +
	"<psproject fmt=\"playgo-manifest\" version=\"0990\">\r\n" +
	"  <volume>\r\n" +
	"    <chunk_info chunk_count=\"1\" scenario_count=\"1\">\r\n" +
	"      <scenarios default_id=\"0\">\r\n" +
	"        <scenario id=\"0\" type=\"sp\" initial_chunk_count=\"1\" label=\"Scenario #0\">0</scenario>\r\n" +
	"      </scenarios>\r\n" +
	"    </chunk_info>\r\n" +
	"  </volume>\r\n" +
	"</psproject>\r\n")

// playgoChunkDat builds a single chunk playgo-chunk.dat.
func playgoChunkDat(contentID string) []byte {
	const size = 416
	out := make([]byte, size)
	binary.LittleEndian.PutUint32(out[0x00:], 0x6f676c70) // 'plgo'
	binary.LittleEndian.PutUint16(out[0x08:], 1)          // image count
	binary.LittleEndian.PutUint16(out[0x0A:], 1)          // chunk count
	binary.LittleEndian.PutUint16(out[0x0C:], 1)          // mchunk count
	binary.LittleEndian.PutUint16(out[0x0E:], 1)          // scenario count
	binary.LittleEndian.PutUint32(out[0x10:], size)
	binary.LittleEndian.PutUint16(out[0x14:], 0) // default scenario
	binary.LittleEndian.PutUint16(out[0x16:], 1) // attrib
	for i := 0x20; i < 0x40; i++ {
		out[i] = 0xFF
	}
	copy(out[0x40:], contentID)

	table := []uint32{256, 32, 288, 2, 304, 9, 320, 16, 352, 32, 384, 2, 400, 12, 336, 16}
	for i, value := range table {
		binary.LittleEndian.PutUint32(out[0xC0+i*4:], value)
	}

	// Chunk attribute 0.
	out[0x100] = 0x80 // flag
	out[0x101] = 0    // image disc layer
	out[0x102] = 3    // required locus
	binary.LittleEndian.PutUint16(out[0x100+14:], 1)
	binary.LittleEndian.PutUint64(out[0x100+16:], 0xFFFFFFFFFFFFFFFF)
	binary.LittleEndian.PutUint32(out[0x100+24:], 0)
	binary.LittleEndian.PutUint32(out[0x100+28:], 0)

	binary.LittleEndian.PutUint16(out[0x120:], 0)
	copy(out[0x130:], "Chunk #0")

	// Scenario attribute 0.
	out[0x160] = 1
	binary.LittleEndian.PutUint16(out[0x160+20:], 1)
	binary.LittleEndian.PutUint16(out[0x160+22:], 1)
	binary.LittleEndian.PutUint32(out[0x160+24:], 0)
	binary.LittleEndian.PutUint32(out[0x160+28:], 0)

	binary.LittleEndian.PutUint16(out[0x180:], 0)
	copy(out[0x190:], "Scenario #0")
	return out
}

// setPlaygoSizes stores the finished package and inner image sizes.
func setPlaygoSizes(chunkDat []byte, packageSize, innerSize uint64) {
	binary.LittleEndian.PutUint64(chunkDat[0x140:], 0)
	binary.LittleEndian.PutUint64(chunkDat[0x148:], packageSize)
	binary.LittleEndian.PutUint64(chunkDat[0x150:], 0)
	binary.LittleEndian.PutUint64(chunkDat[0x158:], innerSize)
}
