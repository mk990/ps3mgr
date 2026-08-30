package orbis

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// PKG entry identifiers.
const (
	entryDigests          uint32 = 0x0001
	entryEntryKeys        uint32 = 0x0010
	entryImageKey         uint32 = 0x0020
	entryGeneralDigests   uint32 = 0x0080
	entryMetas            uint32 = 0x0100
	entryEntryNames       uint32 = 0x0200
	entryLicenseDat       uint32 = 0x0400
	entryLicenseInfo      uint32 = 0x0401
	entryPsReservedDat    uint32 = 0x0409
	entryParamSFO         uint32 = 0x1000
	entryPlaygoChunkDat   uint32 = 0x1001
	entryPlaygoChunkSHA   uint32 = 0x1002
	entryPlaygoManifest   uint32 = 0x1003
	entryPic1PNG          uint32 = 0x1006
	entrySaveDataPNG      uint32 = 0x100D
	entryIcon0PNG         uint32 = 0x1200
	entryIcon000PNG       uint32 = 0x1201
	entryPic0PNG          uint32 = 0x1220
	entrySnd0AT9          uint32 = 0x1240
	entryPic100PNG        uint32 = 0x1241
	entryChangeinfoXML    uint32 = 0x1260
	entryChangeinfo00XML  uint32 = 0x1261
	entryIcon0DDS         uint32 = 0x1280
	entryIcon000DDS       uint32 = 0x1281
	entryPic0DDS          uint32 = 0x12A0
	entryPic1DDS          uint32 = 0x12C0
	entryPic100DDS        uint32 = 0x12C1
	entryTrophy00TRP      uint32 = 0x1400
	entryKeymapRP001PNG   uint32 = 0x1600
	entryKeymapRP00001PNG uint32 = 0x1610
)

// Content and DRM types.
const (
	contentTypeGD = 0x1A
	drmTypePS4    = 0x0F

	contentFlagGD_AC       = 0x02000000
	contentFlagUnkX8000000 = 0x08000000
)

var (
	entryIDToName = map[uint32]string{}
	entryNameToID = map[string]uint32{}
)

func init() {
	base := map[uint32]string{
		entryDigests:        ".digests",
		entryEntryKeys:      ".entry_keys",
		entryImageKey:       ".image_key",
		entryGeneralDigests: ".general_digests",
		entryMetas:          ".metas",
		entryEntryNames:     ".entry_names",
		entryLicenseDat:     "license.dat",
		entryLicenseInfo:    "license.info",
		0x0402:              "nptitle.dat",
		0x0403:              "npbind.dat",
		0x0404:              "selfinfo.dat",
		0x0406:              "imageinfo.dat",
		0x0407:              "target-deltainfo.dat",
		0x0408:              "origin-deltainfo.dat",
		entryPsReservedDat:  "psreserved.dat",
		entryParamSFO:       "param.sfo",
		entryPlaygoChunkDat: "playgo-chunk.dat",
		entryPlaygoChunkSHA: "playgo-chunk.sha",
		entryPlaygoManifest: "playgo-manifest.xml",
		0x1004:              "pronunciation.xml",
		0x1005:              "pronunciation.sig",
		entryPic1PNG:        "pic1.png",
		0x1007:              "pubtoolinfo.dat",
		0x1008:              "app/playgo-chunk.dat",
		0x1009:              "app/playgo-chunk.sha",
		0x100A:              "app/playgo-manifest.xml",
		0x100B:              "shareparam.json",
		0x100C:              "shareoverlayimage.png",
		entrySaveDataPNG:    "save_data.png",
		0x100E:              "shareprivacyguardimage.png",
		entryIcon0PNG:       "icon0.png",
		entryPic0PNG:        "pic0.png",
		entrySnd0AT9:        "snd0.at9",
		entryChangeinfoXML:  "changeinfo/changeinfo.xml",
		entryIcon0DDS:       "icon0.dds",
		entryPic0DDS:        "pic0.dds",
		entryPic1DDS:        "pic1.dds",
	}
	for id, name := range base {
		entryIDToName[id] = name
	}
	for i := uint32(0); i < 31; i++ {
		entryIDToName[entryIcon000PNG+i] = fmt.Sprintf("icon0_%02d.png", i)
		entryIDToName[entryIcon000DDS+i] = fmt.Sprintf("icon0_%02d.dds", i)
		entryIDToName[entryPic100PNG+i] = fmt.Sprintf("pic1_%02d.png", i)
		entryIDToName[entryPic100DDS+i] = fmt.Sprintf("pic1_%02d.dds", i)
		entryIDToName[entryChangeinfo00XML+i] = fmt.Sprintf("changeinfo/changeinfo_%02d.xml", i)
		if i < 10 {
			entryIDToName[entryKeymapRP001PNG+i] = fmt.Sprintf("keymap_rp/0%02d.png", i+1)
		}
		for j := uint32(0); j < 10; j++ {
			entryIDToName[entryKeymapRP00001PNG+16*i+j] = fmt.Sprintf("keymap_rp/%02d/0%02d.png", i, j+1)
		}
	}
	for i := uint32(0); i < 100; i++ {
		entryIDToName[entryTrophy00TRP+i] = fmt.Sprintf("trophy/trophy%02d.trp", i)
	}
	for id, name := range entryIDToName {
		entryNameToID[name] = id
	}
}

// metaEntry is a row of the PKG entry table.
type metaEntry struct {
	id              uint32
	nameTableOffset uint32
	flags1          uint32
	flags2          uint32
	dataOffset      uint32
	dataSize        uint32
}

func (m metaEntry) marshal() []byte {
	out := make([]byte, 32)
	binary.BigEndian.PutUint32(out[0:], m.id)
	binary.BigEndian.PutUint32(out[4:], m.nameTableOffset)
	binary.BigEndian.PutUint32(out[8:], m.flags1)
	binary.BigEndian.PutUint32(out[12:], m.flags2)
	binary.BigEndian.PutUint32(out[16:], m.dataOffset)
	binary.BigEndian.PutUint32(out[20:], m.dataSize)
	return out
}

func parseMetaEntry(data []byte) metaEntry {
	return metaEntry{
		id:              binary.BigEndian.Uint32(data[0:]),
		nameTableOffset: binary.BigEndian.Uint32(data[4:]),
		flags1:          binary.BigEndian.Uint32(data[8:]),
		flags2:          binary.BigEndian.Uint32(data[12:]),
		dataOffset:      binary.BigEndian.Uint32(data[16:]),
		dataSize:        binary.BigEndian.Uint32(data[20:]),
	}
}

func (m metaEntry) keyIndex() uint32 { return (m.flags2 & 0xF000) >> 12 }
func (m metaEntry) encrypted() bool  { return m.flags1&0x80000000 != 0 }

// pkgEntry is a single entry to be written into the package body.
type pkgEntry struct {
	id   uint32
	name string
	size uint32
	// write emits exactly size bytes.
	write func(w io.Writer) error
	meta  *metaEntry
}

func staticEntry(id uint32, name string, data []byte) *pkgEntry {
	return &pkgEntry{id: id, name: name, size: uint32(len(data)), write: func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}}
}

// nameTable is the .entry_names table.
type nameTable struct {
	names   []string
	offsets map[string]uint32
	length  uint32
}

func newNameTable() *nameTable {
	return &nameTable{names: []string{""}, offsets: map[string]uint32{"": 0}, length: 1}
}

func (n *nameTable) offset(name string) uint32 {
	if name == "" {
		return 0
	}
	if offset, ok := n.offsets[name]; ok {
		return offset
	}
	offset := n.length
	n.names = append(n.names, name)
	n.offsets[name] = offset
	n.length += uint32(len(name)) + 1
	return offset
}

func (n *nameTable) marshal() []byte {
	out := make([]byte, 0, n.length)
	for _, name := range n.names {
		out = append(out, name...)
		out = append(out, 0)
	}
	return out
}

// pkgHeader mirrors the PS4 PKG header.
type pkgHeader struct {
	flags            uint32
	unk0x0C          uint32
	entryCount       uint32
	scEntryCount     uint16
	entryCount2      uint16
	entryTableOffset uint32
	mainEntDataSize  uint32
	bodyOffset       uint64
	bodySize         uint64
	contentID        string
	drmType          uint32
	contentType      uint32
	contentFlags     uint32
	promoteSize      uint32
	versionDate      uint32
	versionHash      uint32
	iroTag           uint32
	ekcVersion       uint32
	scEntries1Hash   []byte
	scEntries2Hash   []byte
	digestTableHash  []byte
	bodyDigest       []byte

	unk0x400       uint32
	pfsImageCount  uint32
	pfsFlags       uint64
	pfsImageOffset uint64
	pfsImageSize   uint64
	mountImgOffset uint64
	mountImgSize   uint64
	packageSize    uint64
	pfsSignedSize  uint32
	pfsCacheSize   uint32
	pfsImageDigest []byte
	pfsSignedDgst  []byte
	pfsSplitSize0  uint64
	pfsSplitSize1  uint64
}

func (h *pkgHeader) marshal() []byte {
	out := make([]byte, 0x1000)
	copy(out[0:], "\x7fCNT")
	binary.BigEndian.PutUint32(out[0x04:], h.flags)
	binary.BigEndian.PutUint32(out[0x0C:], h.unk0x0C)
	binary.BigEndian.PutUint32(out[0x10:], h.entryCount)
	binary.BigEndian.PutUint16(out[0x14:], h.scEntryCount)
	binary.BigEndian.PutUint16(out[0x16:], h.entryCount2)
	binary.BigEndian.PutUint32(out[0x18:], h.entryTableOffset)
	binary.BigEndian.PutUint32(out[0x1C:], h.mainEntDataSize)
	binary.BigEndian.PutUint64(out[0x20:], h.bodyOffset)
	binary.BigEndian.PutUint64(out[0x28:], h.bodySize)
	copy(out[0x40:], h.contentID)
	binary.BigEndian.PutUint32(out[0x70:], h.drmType)
	binary.BigEndian.PutUint32(out[0x74:], h.contentType)
	binary.BigEndian.PutUint32(out[0x78:], h.contentFlags)
	binary.BigEndian.PutUint32(out[0x7C:], h.promoteSize)
	binary.BigEndian.PutUint32(out[0x80:], h.versionDate)
	binary.BigEndian.PutUint32(out[0x84:], h.versionHash)
	binary.BigEndian.PutUint32(out[0x98:], h.iroTag)
	binary.BigEndian.PutUint32(out[0x9C:], h.ekcVersion)
	copy(out[0x100:], h.scEntries1Hash)
	copy(out[0x120:], h.scEntries2Hash)
	copy(out[0x140:], h.digestTableHash)
	copy(out[0x160:], h.bodyDigest)
	binary.BigEndian.PutUint32(out[0x400:], h.unk0x400)
	binary.BigEndian.PutUint32(out[0x404:], h.pfsImageCount)
	binary.BigEndian.PutUint64(out[0x408:], h.pfsFlags)
	binary.BigEndian.PutUint64(out[0x410:], h.pfsImageOffset)
	binary.BigEndian.PutUint64(out[0x418:], h.pfsImageSize)
	binary.BigEndian.PutUint64(out[0x420:], h.mountImgOffset)
	binary.BigEndian.PutUint64(out[0x428:], h.mountImgSize)
	binary.BigEndian.PutUint64(out[0x430:], h.packageSize)
	binary.BigEndian.PutUint32(out[0x438:], h.pfsSignedSize)
	binary.BigEndian.PutUint32(out[0x43C:], h.pfsCacheSize)
	copy(out[0x440:], h.pfsImageDigest)
	copy(out[0x460:], h.pfsSignedDgst)
	binary.BigEndian.PutUint64(out[0x480:], h.pfsSplitSize0)
	binary.BigEndian.PutUint64(out[0x488:], h.pfsSplitSize1)
	return out
}

// headerDigestInput returns the header bytes hashed for the general digests.
func (h *pkgHeader) headerDigestInput() []byte {
	header := h.marshal()
	input := make([]byte, 0, 192)
	input = append(input, header[0:64]...)
	input = append(input, header[0x400:0x400+128]...)
	return input
}

// PkgInfo describes a parsed package header.
type PkgInfo struct {
	ContentID      string
	ContentType    uint32
	DrmType        uint32
	PfsImageOffset uint64
	PfsImageSize   uint64
	PackageSize    uint64
	EntryCount     uint32
}

func parsePkgHeader(data []byte) (*PkgInfo, error) {
	if len(data) < 0x1000 {
		return nil, fmt.Errorf("PKG header is truncated")
	}
	if string(data[0:4]) != "\x7fCNT" {
		return nil, fmt.Errorf("file does not have PS4 PKG magic")
	}
	contentID := string(data[0x40:0x70])
	if index := indexByteSafe([]byte(contentID)); index >= 0 {
		contentID = contentID[:index]
	}
	return &PkgInfo{
		ContentID:      contentID,
		ContentType:    binary.BigEndian.Uint32(data[0x74:]),
		DrmType:        binary.BigEndian.Uint32(data[0x70:]),
		PfsImageOffset: binary.BigEndian.Uint64(data[0x410:]),
		PfsImageSize:   binary.BigEndian.Uint64(data[0x418:]),
		PackageSize:    binary.BigEndian.Uint64(data[0x430:]),
		EntryCount:     binary.BigEndian.Uint32(data[0x10:]),
	}, nil
}

// generalDigests is the GENERAL_DIGESTS entry.
type generalDigests struct {
	unk1       uint16
	entryType  uint16
	setDigests uint32
	digests    [][]byte
}

// General digest slots, in the order they are stored.
const (
	digestContent = iota
	digestGame
	digestHeader
	digestSystem
	digestMajorParam
	digestParam
	digestCount
)

const (
	digestFlagContent    = 1 << 1
	digestFlagGame       = 1 << 2
	digestFlagHeader     = 1 << 3
	digestFlagMajorParam = 1 << 5
	digestFlagParam      = 1 << 6
)

func newGeneralDigests() *generalDigests {
	digests := make([][]byte, digestCount)
	for i := range digests {
		digests[i] = make([]byte, 32)
	}
	return &generalDigests{unk1: 0xD256, entryType: 0x100, digests: digests}
}

func (g *generalDigests) set(slot int, flag uint32, value []byte) {
	copy(g.digests[slot], value)
	g.setDigests |= flag
}

func (g *generalDigests) length() uint32 { return 0x180 }

func (g *generalDigests) marshal() []byte {
	out := make([]byte, g.length())
	binary.BigEndian.PutUint16(out[0:], g.unk1)
	binary.BigEndian.PutUint16(out[2:], g.entryType)
	binary.BigEndian.PutUint32(out[28:], g.setDigests)
	for i, digest := range g.digests {
		copy(out[32+i*32:], digest)
	}
	return out
}

// keysEntry is the ENTRY_KEYS entry.
type keysEntry struct {
	seedDigest []byte
	digests    [][]byte
	keys       [][]byte
}

func newKeysEntry(contentID, passcode string) (*keysEntry, error) {
	padded := make([]byte, 48)
	copy(padded, contentID)
	entry := &keysEntry{seedDigest: sha256sum(padded)}
	for i := uint32(0); i < 7; i++ {
		passcodeKey, err := computeKeys(contentID, passcode, i)
		if err != nil {
			return nil, err
		}
		entry.digests = append(entry.digests, xorBytes(sha256sum(passcodeKey), passcodeKey))
		entry.keys = append(entry.keys, rsa2048EncryptKey(pkgPublicKeys[i], passcodeKey))
	}
	entry.keys[0] = rsa2048EncryptKey(pkgPublicKeys[0], []byte(passcode))
	return entry, nil
}

func (k *keysEntry) marshal() []byte {
	out := make([]byte, 2048)
	copy(out, k.seedDigest)
	for i, digest := range k.digests {
		copy(out[32+i*32:], digest)
	}
	for i, key := range k.keys {
		copy(out[32+7*32+i*256:], key)
	}
	return out
}

// sortEntriesByName orders entries the way the publishing tools do when
// building the name table.
func sortEntriesByName(entries []*pkgEntry) []*pkgEntry {
	sorted := append([]*pkgEntry{}, entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	return sorted
}
