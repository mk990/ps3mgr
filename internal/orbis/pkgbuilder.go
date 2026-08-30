package orbis

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
)

// BuildOptions describes the package to build.
type BuildOptions struct {
	// ContentID is the 36 character content ID, e.g. UP9000-SLUS20946_00-SLUS000000000001.
	ContentID string
	// Passcode is the 32 character fake package passcode.
	Passcode string
	// Root is the filesystem tree that becomes the image, including sce_sys.
	Root *Tree
	// Timestamp is stored in the filesystem inodes.
	Timestamp time.Time
	// CreationDate ends up in PUBTOOLINFO.
	CreationDate time.Time
	// Context cancels a long build.
	Context context.Context
}

const (
	blockSize     = 0x10000
	pkgBodyOffset = 0x2000
	pkgAlign      = 0x80000
)

func align(value, alignment uint64) uint64 {
	if remainder := value % alignment; remainder != 0 {
		value += alignment - remainder
	}
	return value
}

// pkgBuilder assembles a fake PKG.
type pkgBuilder struct {
	options   BuildOptions
	ekpfs     []byte
	innerPfs  *pfsBuilder
	outerPfs  *pfsBuilder
	header    *pkgHeader
	entries   []*pkgEntry
	names     *nameTable
	metas     []*metaEntry
	digests   []byte
	general   *generalDigests
	paramSFO  *ParamSFO
	chunkSHA  []byte
	chunkDat  []byte
	metasEnt  *pkgEntry
	digestEnt *pkgEntry
	shaEnt    *pkgEntry
}

// Build writes a fake PKG to the given path.
func Build(options BuildOptions, path string, progress func(stage string, percent float64)) error {
	if progress == nil {
		progress = func(string, float64) {}
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	options.Root.WithContext(options.Context)
	builder := &pkgBuilder{options: options}
	ekpfs, err := computeKeys(options.ContentID, options.Passcode, 1)
	if err != nil {
		return err
	}
	builder.ekpfs = ekpfs

	progress("preparing filesystem", 0)
	if err := builder.prepare(); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	total := int64(builder.header.bodyOffset + builder.header.bodySize + builder.header.pfsImageSize)
	if err := file.Truncate(total); err != nil {
		return fmt.Errorf("allocate package: %w", err)
	}

	progress("writing filesystem", 5)
	if err := builder.outerPfs.writeImage(file, int64(builder.header.pfsImageOffset)); err != nil {
		return err
	}
	progress("calculating chunk digests", 70)
	if err := builder.calcPlaygoDigests(file); err != nil {
		return err
	}
	progress("finishing package", 85)
	if err := builder.finish(file); err != nil {
		return err
	}
	progress("done", 100)
	return file.Sync()
}

func (b *pkgBuilder) prepare() error {
	timestamp := b.options.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	fileTime := timestamp.Unix()

	// The keystone ties the image to the passcode.
	if b.options.Root.root.getFile("sce_sys/keystone") == nil {
		if err := b.options.Root.root.addFile("sce_sys", "keystone", createKeystone(b.options.Passcode)); err != nil {
			return err
		}
	}
	inner, err := newPfsBuilder(pfsProperties{
		ctx:       b.options.Context,
		root:      b.options.Root.root,
		blockSize: blockSize,
		fileTime:  fileTime,
		minBlocks: 0x55,
	})
	if err != nil {
		return fmt.Errorf("prepare inner PFS: %w", err)
	}
	b.innerPfs = inner

	pfsc := newPfscWriter(inner.size())
	outerRoot := &fsDir{}
	outerRoot.files = append(outerRoot.files, &fsFile{
		name:     "pfs_image.dat",
		parent:   outerRoot,
		size:     pfsc.size(),
		compSize: inner.size(),
		compress: true,
		writeAt: func(target readerWriterAt, offset int64) error {
			if err := pfsc.writeHeader(io.NewOffsetWriter(target, offset)); err != nil {
				return err
			}
			return inner.writeImage(target, offset+pfsc.headerSize)
		},
	})
	outer, err := newPfsBuilder(pfsProperties{
		ctx:       b.options.Context,
		root:      outerRoot,
		blockSize: blockSize,
		fileTime:  fileTime,
		encrypt:   true,
		sign:      true,
		ekpfs:     b.ekpfs,
		seed:      make([]byte, 16),
	})
	if err != nil {
		return fmt.Errorf("prepare outer PFS: %w", err)
	}
	b.outerPfs = outer
	return b.buildHeaderAndEntries(outer.size())
}

func (b *pkgBuilder) buildHeaderAndEntries(pfsSize int64) error {
	b.header = &pkgHeader{
		flags:            0x01,
		unk0x0C:          0xF,
		scEntryCount:     6,
		entryTableOffset: 0x2A80,
		mainEntDataSize:  0xD00,
		bodyOffset:       pkgBodyOffset,
		bodySize:         0x7E000,
		contentID:        b.options.ContentID,
		drmType:          drmTypePS4,
		contentType:      contentTypeGD,
		contentFlags:     contentFlagUnkX8000000 | contentFlagGD_AC,
		versionDate:      0x20161020,
		versionHash:      0x1738551,
		ekcVersion:       1,
		scEntries1Hash:   make([]byte, 32),
		scEntries2Hash:   make([]byte, 32),
		digestTableHash:  make([]byte, 32),
		bodyDigest:       make([]byte, 32),
		unk0x400:         1,
		pfsImageCount:    1,
		pfsFlags:         0x80000000000003CC,
		pfsImageOffset:   0x80000,
		pfsImageSize:     uint64(pfsSize),
		pfsSignedSize:    0x10000,
		pfsCacheSize:     0xD0000,
		pfsImageDigest:   make([]byte, 32),
		pfsSignedDgst:    make([]byte, 32),
	}
	b.header.packageSize = 0x80000 + uint64(pfsSize)

	keys, err := newKeysEntry(b.options.ContentID, b.options.Passcode)
	if err != nil {
		return err
	}
	b.general = newGeneralDigests()
	b.names = newNameTable()

	imageKey := rsa2048EncryptKey(fakeKeyset.Modulus, b.ekpfs)
	license, err := newLicenseDat(b.options.ContentID, contentTypeGD)
	if err != nil {
		return err
	}

	paramFile := b.options.Root.root.getFile("sce_sys/param.sfo")
	if paramFile == nil {
		return fmt.Errorf("the image is missing sce_sys/param.sfo")
	}
	paramData, err := collectFile(paramFile)
	if err != nil {
		return err
	}
	sfo, err := ParseSFO(paramData)
	if err != nil {
		return err
	}
	b.paramSFO = sfo

	creation := b.options.CreationDate
	if creation.IsZero() {
		creation = time.Now().UTC()
	}
	sizeInfo := fmt.Sprintf(",img0_l0_size=%d,img0_l1_size=0,img0_sc_ksize=512,img0_pc_ksize=832",
		(b.header.packageSize+0xFFFFF)/(1024*1024))
	if err := sfo.SetText("PUBTOOLINFO", "c_date="+creation.Format("20060102")+sizeInfo); err != nil {
		// PUBTOOLINFO is generated, so grow the field when the template is small.
		sfo.Set(SFOValue{Name: "PUBTOOLINFO", Type: SFOUtf8, Text: "c_date=" + creation.Format("20060102") + sizeInfo, Max: 0x200})
	}
	sfo.SetInteger("PUBTOOLVER", 0x02890000)

	entryKeysEntry := staticEntry(entryEntryKeys, "", keys.marshal())
	imageKeyEntry := staticEntry(entryImageKey, "", imageKey)
	generalEntry := &pkgEntry{id: entryGeneralDigests, size: b.general.length(), write: func(w io.Writer) error {
		_, err := w.Write(b.general.marshal())
		return err
	}}
	b.metasEnt = &pkgEntry{id: entryMetas, write: func(w io.Writer) error {
		for _, meta := range b.metas {
			if _, err := w.Write(meta.marshal()); err != nil {
				return err
			}
		}
		return nil
	}}
	b.digestEnt = &pkgEntry{id: entryDigests, write: func(w io.Writer) error {
		_, err := w.Write(b.digests)
		return err
	}}
	namesEntry := &pkgEntry{id: entryEntryNames, write: func(w io.Writer) error {
		_, err := w.Write(b.names.marshal())
		return err
	}}
	b.chunkDat = playgoChunkDat(b.options.ContentID)
	chunkDatEntry := &pkgEntry{id: entryPlaygoChunkDat, name: "playgo-chunk.dat", size: uint32(len(b.chunkDat)), write: func(w io.Writer) error {
		_, err := w.Write(b.chunkDat)
		return err
	}}
	b.shaEnt = &pkgEntry{id: entryPlaygoChunkSHA, name: "playgo-chunk.sha", write: func(w io.Writer) error {
		_, err := w.Write(b.chunkSHA)
		return err
	}}
	manifestEntry := staticEntry(entryPlaygoManifest, "playgo-manifest.xml", playgoManifest)
	sfoEntry := &pkgEntry{id: entryParamSFO, name: "param.sfo", write: func(w io.Writer) error {
		_, err := w.Write(b.paramSFO.Serialize())
		return err
	}}
	psReserved := staticEntry(entryPsReservedDat, "", make([]byte, 0x2000))
	licenseEntry := staticEntry(entryLicenseDat, "", license.dat)
	licenseInfoEntry := staticEntry(entryLicenseInfo, "", license.info)

	b.entries = []*pkgEntry{
		entryKeysEntry, imageKeyEntry, generalEntry, b.metasEnt, b.digestEnt, namesEntry,
		chunkDatEntry, b.shaEnt, manifestEntry,
		licenseEntry, licenseInfoEntry, sfoEntry, psReserved,
	}

	// Files below sce_sys with a reserved entry name become PKG entries.
	sceSys := b.options.Root.root.getDir("sce_sys")
	if sceSys == nil {
		return fmt.Errorf("the image is missing the sce_sys directory")
	}
	for _, file := range sceSys.allFiles() {
		name := file.name
		for parent := file.parent; parent != nil && parent != b.options.Root.root && parent.name != "sce_sys"; parent = parent.parent {
			name = parent.name + "/" + name
		}
		id, known := entryNameToID[name]
		if !known || name == "param.sfo" {
			continue
		}
		source := file
		b.entries = append(b.entries, &pkgEntry{id: id, name: name, size: uint32(source.size), write: func(w io.Writer) error {
			return source.write(w)
		}})
	}

	b.digests = make([]byte, len(b.entries)*32)

	// First pass: register entry names in sorted order.
	for _, entry := range sortEntriesByName(b.entries) {
		b.names.offset(entry.name)
	}
	namesEntry.size = b.names.length
	b.digestEnt.size = uint32(len(b.digests))
	sfoEntry.size = uint32(b.paramSFO.Size())

	// Second pass: assign offsets and sizes.
	flagMap := map[uint32]uint32{
		entryDigests:        0x40000000,
		entryEntryKeys:      0x60000000,
		entryImageKey:       0xE0000000,
		entryGeneralDigests: 0x60000000,
		entryMetas:          0x60000000,
		entryEntryNames:     0x40000000,
		entryLicenseDat:     0x80000000,
		entryLicenseInfo:    0x80000000,
	}
	keyMap := map[uint32]uint32{
		entryImageKey:    3 << 12,
		entryLicenseDat:  3 << 12,
		entryLicenseInfo: 2 << 12,
	}
	dataOffset := b.header.bodyOffset
	b.metas = nil
	for _, entry := range b.entries {
		meta := &metaEntry{
			id:              entry.id,
			nameTableOffset: b.names.offset(entry.name),
			dataOffset:      uint32(dataOffset),
			dataSize:        entry.size,
			flags1:          flagMap[entry.id],
			flags2:          keyMap[entry.id],
		}
		b.metas = append(b.metas, meta)
		if entry == b.metasEnt {
			meta.dataSize = uint32(len(b.entries)) * 32
			entry.size = meta.dataSize
		}
		if entry == b.shaEnt {
			// Reserve room for one digest per 64 KiB chunk of the finished PKG.
			estimate := uint64(0)
			for _, e := range b.entries {
				estimate += align(uint64(e.size), 16)
			}
			pkgSize := align(b.header.bodyOffset+estimate, pkgAlign) + uint64(pfsSize)
			pkgSize += ((pkgSize + 16) / 0x10000) * 4
			meta.dataSize = uint32(pkgSize/0x10000) * 4
			entry.size = meta.dataSize
		}
		dataOffset = align(dataOffset+uint64(meta.dataSize), 16)
		entry.meta = meta
	}
	bodySize := dataOffset - b.header.bodyOffset
	sortMetas(b.metas)
	b.header.entryCount = uint32(len(b.entries))
	b.header.entryCount2 = uint16(len(b.entries))
	b.header.bodySize = align(b.header.bodyOffset+bodySize, pkgAlign) - b.header.bodyOffset
	b.header.mainEntDataSize = uint32(entryKeysEntry.size + imageKeyEntry.size + generalEntry.size + b.metasEnt.size + b.digestEnt.size)
	b.header.pfsImageOffset = b.header.bodyOffset + b.header.bodySize
	b.header.packageSize = b.header.bodySize + b.header.bodyOffset + b.header.pfsImageSize
	b.header.mountImgSize = b.header.packageSize
	b.header.promoteSize = uint32(b.header.bodySize + b.header.bodyOffset)

	b.chunkSHA = make([]byte, 4*(b.header.packageSize/0x10000))
	if uint32(len(b.chunkSHA)) > b.shaEnt.meta.dataSize {
		return fmt.Errorf("playgo chunk hash table was not allocated enough space")
	}
	b.shaEnt.meta.dataSize = uint32(len(b.chunkSHA))
	b.shaEnt.size = uint32(len(b.chunkSHA))
	setPlaygoSizes(b.chunkDat, b.header.packageSize, uint64(b.innerPfs.size()))
	return nil
}

func sortMetas(metas []*metaEntry) {
	for i := 1; i < len(metas); i++ {
		for j := i; j > 0 && metas[j-1].id > metas[j].id; j-- {
			metas[j-1], metas[j] = metas[j], metas[j-1]
		}
	}
}

// collectFile reads a builder file into memory. Only used for small files.
func collectFile(file *fsFile) ([]byte, error) {
	buffer := &limitedBuffer{limit: 1 << 22}
	if err := file.write(buffer); err != nil {
		return nil, err
	}
	return buffer.data, nil
}

type limitedBuffer struct {
	data  []byte
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.data)+len(p) > l.limit {
		return 0, fmt.Errorf("file is larger than %d bytes", l.limit)
	}
	l.data = append(l.data, p...)
	return len(p), nil
}

func (b *pkgBuilder) calcPlaygoDigests(file *os.File) error {
	const chunk = 0x10000
	start := int64(b.header.pfsImageOffset) / chunk
	end := int64(b.header.packageSize) / chunk
	return parallelDo(int(end-start), func(index int) error {
		if err := b.options.Context.Err(); err != nil {
			return err
		}
		position := (start + int64(index)) * chunk
		buffer := make([]byte, chunk)
		if _, err := file.ReadAt(buffer, position); err != nil {
			return err
		}
		digest := sha256sum(buffer)
		offset := (start + int64(index)) * 4
		if offset+4 > int64(len(b.chunkSHA)) {
			return nil
		}
		copy(b.chunkSHA[offset:], digest[:4])
		return nil
	})
}

func (b *pkgBuilder) finish(file *os.File) error {
	// PFS image digests.
	signed, err := hashSection(file, int64(b.header.pfsImageOffset), 0x10000)
	if err != nil {
		return err
	}
	b.header.pfsSignedDgst = signed
	image, err := hashSection(file, int64(b.header.pfsImageOffset), int64(b.header.pfsImageSize))
	if err != nil {
		return err
	}
	b.header.pfsImageDigest = image

	// General digests.
	b.general.set(digestHeader, digestFlagHeader, sha256sum(b.header.headerDigestInput()))
	b.general.set(digestGame, digestFlagGame, b.header.pfsImageDigest)
	b.general.set(digestContent, digestFlagContent, b.contentDigest())
	b.general.set(digestMajorParam, digestFlagMajorParam, b.majorParamDigest())
	b.general.set(digestParam, digestFlagParam, sha256sum(b.paramSFO.Serialize()))

	// Body.
	for _, entry := range b.entries {
		if err := b.writeEntry(file, entry); err != nil {
			return fmt.Errorf("write entry %#x: %w", entry.id, err)
		}
	}

	// Entry digests.
	digestsOffset := int64(0)
	for _, meta := range b.metas {
		if meta.id == entryDigests {
			digestsOffset = int64(meta.dataOffset)
		}
	}
	for i := 1; i < len(b.metas); i++ {
		meta := b.metas[i]
		hash, err := hashSection(file, int64(meta.dataOffset), int64(meta.dataSize))
		if err != nil {
			return err
		}
		copy(b.digests[32*i:], hash)
		if _, err := file.WriteAt(hash, digestsOffset+int64(32*i)); err != nil {
			return err
		}
	}

	bodyDigest, err := hashSection(file, int64(b.header.bodyOffset), int64(b.header.bodySize))
	if err != nil {
		return err
	}
	b.header.bodyDigest = bodyDigest
	b.header.digestTableHash = sha256sum(b.digests)

	scEntries1, err := b.concatEntries(file, []uint32{entryEntryKeys, entryImageKey, entryGeneralDigests, entryMetas, entryDigests}, false)
	if err != nil {
		return err
	}
	if uint32(len(scEntries1)) != b.header.mainEntDataSize {
		return fmt.Errorf("main entry data size mismatch: %d != %d", len(scEntries1), b.header.mainEntDataSize)
	}
	b.header.scEntries1Hash = sha256sum(scEntries1)
	scEntries2, err := b.concatEntries(file, []uint32{entryEntryKeys, entryImageKey, entryGeneralDigests, entryMetas}, true)
	if err != nil {
		return err
	}
	b.header.scEntries2Hash = sha256sum(scEntries2)

	if _, err := file.WriteAt(b.header.marshal()[:0x5A0], 0); err != nil {
		return err
	}
	headerDigest, err := hashSection(file, 0, 0xFE0)
	if err != nil {
		return err
	}
	if _, err := file.WriteAt(headerDigest, 0xFE0); err != nil {
		return err
	}
	headerHash, err := hashSection(file, 0, 0x1000)
	if err != nil {
		return err
	}
	signature := rsa2048EncryptKey(pkgSignKey, headerHash)
	_, err = file.WriteAt(signature, 0x1000)
	return err
}

// concatEntries reads the given entries back from the package body.
func (b *pkgBuilder) concatEntries(file *os.File, ids []uint32, truncateMetas bool) ([]byte, error) {
	out := make([]byte, 0, 0x1000)
	for _, id := range ids {
		var meta *metaEntry
		for _, candidate := range b.metas {
			if candidate.id == id {
				meta = candidate
				break
			}
		}
		if meta == nil {
			return nil, fmt.Errorf("entry %#x is missing", id)
		}
		size := int64(meta.dataSize)
		if truncateMetas && id == entryMetas {
			size = int64(b.header.scEntryCount) * 0x20
		}
		buffer := make([]byte, size)
		if _, err := file.ReadAt(buffer, int64(meta.dataOffset)); err != nil {
			return nil, err
		}
		out = append(out, buffer...)
	}
	return out, nil
}

func (b *pkgBuilder) writeEntry(file *os.File, entry *pkgEntry) error {
	if !entry.meta.encrypted() {
		return entry.write(io.NewOffsetWriter(file, int64(entry.meta.dataOffset)))
	}
	buffer := &limitedBuffer{limit: 1 << 20}
	if err := entry.write(buffer); err != nil {
		return err
	}
	passcodeKey, err := computeKeys(b.options.ContentID, b.options.Passcode, entry.meta.keyIndex())
	if err != nil {
		return err
	}
	ivKey := sha256sum(entry.meta.marshal(), passcodeKey)
	data := buffer.data
	if err := aesCBCNoPadding(data, ivKey[16:32], ivKey[0:16], true); err != nil {
		return err
	}
	_, err = file.WriteAt(data, int64(entry.meta.dataOffset))
	return err
}

func (b *pkgBuilder) majorParamString() string {
	out := "ATTRIBUTE" + b.paramSFO.String("ATTRIBUTE")
	if _, ok := b.paramSFO.Get("ATTRIBUTE2"); ok {
		out += "ATTRIBUTE2" + b.paramSFO.String("ATTRIBUTE2")
	}
	out += "CATEGORY" + b.paramSFO.String("CATEGORY")
	out += "FORMAT" + b.paramSFO.String("FORMAT")
	out += "PUBTOOLVER" + b.paramSFO.String("PUBTOOLVER")
	return out
}

func (b *pkgBuilder) majorParamDigest() []byte { return sha256sum([]byte(b.majorParamString())) }

func (b *pkgBuilder) contentDigest() []byte {
	buffer := make([]byte, 0, 128)
	contentID := make([]byte, 36)
	copy(contentID, b.options.ContentID)
	buffer = append(buffer, contentID...)
	buffer = append(buffer, make([]byte, 12)...)
	numbers := make([]byte, 8)
	binary.BigEndian.PutUint32(numbers[0:], b.header.drmType)
	binary.BigEndian.PutUint32(numbers[4:], b.header.contentType)
	buffer = append(buffer, numbers...)
	buffer = append(buffer, b.header.pfsImageDigest...)
	buffer = append(buffer, b.majorParamDigest()...)
	return sha256sum(buffer)
}

// hashSection returns the SHA-256 of a region of the file.
func hashSection(file *os.File, offset, length int64) ([]byte, error) {
	reader := io.NewSectionReader(file, offset, length)
	digest := sha256.New()
	if _, err := io.CopyBuffer(digest, reader, make([]byte, 1<<20)); err != nil {
		return nil, err
	}
	return digest.Sum(nil), nil
}
