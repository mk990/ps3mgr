package orbis

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
)

// readerWriterAt is a random access target, in practice the output file.
type readerWriterAt interface {
	io.ReaderAt
	io.WriterAt
}

// pfsProperties configures a PFS image build.
type pfsProperties struct {
	ctx       context.Context
	root      *fsDir
	fileTime  int64
	blockSize uint32
	minBlocks int64
	encrypt   bool
	sign      bool
	ekpfs     []byte
	seed      []byte
}

// blockSigInfo records a block whose HMAC must be written at sigOffset.
type blockSigInfo struct {
	block     int64
	sigOffset int64
	size      int
}

// pfsBuilder lays out and writes a PFS image.
type pfsBuilder struct {
	props            pfsProperties
	hdr              *pfsHeader
	inodes           []*inode
	superRootDirents []pfsDirent
	superRootIno     *inode
	fptIno           *inode
	crIno            *inode
	allDirs          []*fsDir
	allFiles         []*fsFile
	allNodes         []fsNode
	fpt              *flatPathTable
	resolver         *collisionResolver
	emptyBlock       int64
	finalSigs        []blockSigInfo
	dataSigs         []blockSigInfo
}

func ceilDiv(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	result := a / b
	if a%b != 0 {
		result++
	}
	return result
}

func newPfsBuilder(props pfsProperties) (*pfsBuilder, error) {
	b := &pfsBuilder{props: props, emptyBlock: 4}
	if err := b.setup(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *pfsBuilder) setup() error {
	// The superblock digest is calculated last, so it is pushed first.
	b.finalSigs = append(b.finalSigs, blockSigInfo{block: 0, sigOffset: 0x380, size: 0x5A0})
	b.hdr = newPfsHeader()
	b.hdr.blockSize = b.props.blockSize
	b.hdr.readOnly = 1
	b.hdr.mode = pfsModeAlwaysSet
	if b.props.sign {
		b.hdr.mode |= pfsModeSigned
	}
	if b.props.encrypt {
		b.hdr.mode |= pfsModeEncrypted
	}
	b.hdr.unknownIndex = 1
	if b.props.encrypt || b.props.sign {
		b.hdr.seed = b.props.seed
	}

	b.allDirs = b.props.root.allDirs()
	sortDirsByPath(b.allDirs)
	for _, file := range b.props.root.allFiles() {
		if isPkgEntryFile(b.props.root, file) {
			continue
		}
		b.allFiles = append(b.allFiles, file)
	}
	sortFilesByPath(b.allFiles)
	b.allNodes = nil
	for _, dir := range b.allDirs {
		b.allNodes = append(b.allNodes, dir)
	}
	for _, file := range b.allFiles {
		b.allNodes = append(b.allNodes, file)
	}

	b.setupRootStructure(hasHashCollision(b.allNodes))
	b.addDirInodes()
	b.addFileInodes()
	b.fpt, b.resolver = newFlatPathTable(b.allNodes)
	b.allNodes = append([]fsNode{b.props.root}, b.allNodes...)
	return b.calculateDataBlockLayout()
}

// isPkgEntryFile reports whether a file below sce_sys is stored as a PKG entry
// instead of inside the filesystem image.
func isPkgEntryFile(root *fsDir, file *fsFile) bool {
	name := file.name
	parent := file.parent
	for parent != nil && parent != root {
		if parent.parent == root && parent.name == "sce_sys" {
			_, known := entryNameToID[name]
			return known
		}
		name = parent.name + "/" + name
		parent = parent.parent
	}
	return false
}

func (b *pfsBuilder) makeInode(mode uint16, blocks uint32, size, sizeComp int64, nlink uint16, number uint32, flags uint32) *inode {
	if b.props.sign {
		flags |= inodeFlagUnk1 | inodeFlagUnk2
	}
	return &inode{
		number:   number,
		mode:     mode,
		nlink:    nlink,
		flags:    flags,
		size:     size,
		sizeComp: sizeComp,
		blocks:   blocks,
		time:     b.props.fileTime,
	}
}

// setupRootStructure creates the superroot, flat_path_table and uroot inodes.
func (b *pfsBuilder) setupRootStructure(hasCollision bool) {
	number := uint32(0)
	b.superRootIno = b.makeInode(inodeModeDir|inodeModeRX, 1, 65536, 65536, 1, number, inodeFlagInternal|inodeFlagReadOnly)
	number++
	b.inodes = append(b.inodes, b.superRootIno)
	b.fptIno = b.makeInode(inodeModeFile|inodeModeRX, 1, 0, 0, 1, number, inodeFlagInternal|inodeFlagReadOnly)
	number++
	b.inodes = append(b.inodes, b.fptIno)
	if hasCollision {
		b.crIno = b.makeInode(inodeModeFile|inodeModeRX, 1, 0, 0, 1, number, inodeFlagInternal|inodeFlagReadOnly)
		number++
		b.inodes = append(b.inodes, b.crIno)
	}
	urootIno := b.makeInode(inodeModeDir|inodeModeRX, 1, 65536, 65536, 3, number, inodeFlagReadOnly)

	b.superRootDirents = []pfsDirent{{inodeNumber: b.fptIno.number, direntType: direntFile, name: "flat_path_table"}}
	if hasCollision {
		b.superRootDirents = append(b.superRootDirents, pfsDirent{inodeNumber: b.crIno.number, direntType: direntFile, name: "collision_resolver"})
	}
	b.superRootDirents = append(b.superRootDirents, pfsDirent{inodeNumber: urootIno.number, direntType: direntDirectory, name: "uroot"})

	b.props.root.name = "uroot"
	b.props.root.ino = urootIno
	b.props.root.dirents = []pfsDirent{
		{inodeNumber: urootIno.number, direntType: direntDot, name: "."},
		{inodeNumber: urootIno.number, direntType: direntDotDot, name: ".."},
	}
	if b.props.sign {
		// Outer PFS images do not set the readonly flag.
		b.superRootIno.flags &^= inodeFlagReadOnly
		b.fptIno.flags &^= inodeFlagReadOnly
		urootIno.flags &^= inodeFlagReadOnly
	}
}

func (b *pfsBuilder) addDirInodes() {
	b.inodes = append(b.inodes, b.props.root.ino)
	for _, dir := range b.allDirs {
		ino := b.makeInode(inodeModeDir|inodeModeRX, 1, 65536, 0, 2, uint32(len(b.inodes)), inodeFlagReadOnly)
		dir.ino = ino
		dir.dirents = append(dir.dirents,
			pfsDirent{inodeNumber: ino.number, direntType: direntDot, name: "."},
			pfsDirent{inodeNumber: dir.parent.ino.number, direntType: direntDotDot, name: ".."})
		dir.parent.dirents = append(dir.parent.dirents, pfsDirent{inodeNumber: ino.number, direntType: direntDirectory, name: dir.name})
		dir.parent.ino.nlink++
		b.inodes = append(b.inodes, ino)
	}
}

func (b *pfsBuilder) addFileInodes() {
	for _, file := range b.allFiles {
		flags := uint32(inodeFlagReadOnly)
		if file.compress {
			flags |= inodeFlagCompressed
		}
		ino := b.makeInode(inodeModeFile|inodeModeRX, uint32(ceilDiv(file.size, int64(b.hdr.blockSize))), file.size, file.compressedSize(), 1, uint32(len(b.inodes)), flags)
		if b.props.sign {
			ino.flags &^= inodeFlagReadOnly
		}
		file.ino = ino
		file.parent.dirents = append(file.parent.dirents, pfsDirent{inodeNumber: ino.number, direntType: direntFile, name: file.name})
		b.inodes = append(b.inodes, ino)
	}
}

func (b *pfsBuilder) roundUpToBlock(size int64) int64 {
	return ceilDiv(size, int64(b.hdr.blockSize)) * int64(b.hdr.blockSize)
}

func (b *pfsBuilder) indirectBlockCount(size int64) int64 {
	sigsPerBlock := int64(b.hdr.blockSize) / 36
	blocks := ceilDiv(size, int64(b.hdr.blockSize))
	count := int64(0)
	if blocks > 12 {
		blocks -= 12
		count++
	}
	if blocks > sigsPerBlock {
		blocks -= sigsPerBlock
		count += 1 + ceilDiv(blocks, sigsPerBlock)
	}
	return count
}

// inoNumberToOffset returns the absolute offset of an inode's direct block entry.
func (b *pfsBuilder) inoNumberToOffset(number uint32, db int) int64 {
	return int64(b.hdr.blockSize) + dinodeS32Size*int64(number) + 0x64 + int64(36*db)
}

func (b *pfsBuilder) pushFinal(sig blockSigInfo) {
	if sig.size == 0 {
		sig.size = int(b.hdr.blockSize)
	}
	b.finalSigs = append(b.finalSigs, sig)
}

func (b *pfsBuilder) pushData(sig blockSigInfo) {
	if sig.size == 0 {
		sig.size = int(b.hdr.blockSize)
	}
	b.dataSigs = append(b.dataSigs, sig)
}

func (b *pfsBuilder) calculateDataBlockLayout() error {
	blockSize := int64(b.hdr.blockSize)
	if b.props.sign {
		b.hdr.ndblock = 1
		inodesPerBlock := blockSize / dinodeS32Size
		b.hdr.dinodeCount = int64(len(b.inodes))
		b.hdr.dinodeBlkCount = ceilDiv(int64(len(b.inodes)), inodesPerBlock)
		b.hdr.inodeBlockSig.blocks = uint32(b.hdr.dinodeBlkCount)
		b.hdr.inodeBlockSig.size = b.hdr.dinodeBlkCount * blockSize
		b.hdr.inodeBlockSig.sizeComp = b.hdr.dinodeBlkCount * blockSize
		b.hdr.inodeBlockSig.time = b.props.fileTime
		b.hdr.inodeBlockSig.flags = 0
		for i := int64(0); i < b.hdr.dinodeBlkCount; i++ {
			if i < 12 {
				b.hdr.inodeBlockSig.setDirectBlock(int(i), int32(1+i))
			}
			b.pushFinal(blockSigInfo{block: 1 + i, sigOffset: 0xB8 + 36*i})
		}
		b.hdr.ndblock += b.hdr.dinodeBlkCount
		b.superRootIno.setDirectBlock(0, int32(b.hdr.dinodeBlkCount+1))
		b.pushFinal(blockSigInfo{block: int64(b.superRootIno.startBlock()), sigOffset: b.inoNumberToOffset(b.superRootIno.number, 0)})
		b.hdr.ndblock += int64(b.superRootIno.blocks)

		b.fptIno.setDirectBlock(0, b.superRootIno.startBlock()+1)
		b.fptIno.size = b.fpt.size()
		b.fptIno.sizeComp = b.fpt.size()
		b.fptIno.blocks = uint32(ceilDiv(b.fpt.size(), blockSize))
		b.pushFinal(blockSigInfo{block: int64(b.fptIno.startBlock()), sigOffset: b.inoNumberToOffset(b.fptIno.number, 0)})
		for i := 1; int64(i) < int64(b.fptIno.blocks) && i < 12; i++ {
			b.fptIno.setDirectBlock(i, int32(b.hdr.ndblock))
			b.hdr.ndblock++
			b.pushFinal(blockSigInfo{block: int64(b.fptIno.startBlock()), sigOffset: b.inoNumberToOffset(b.fptIno.number, i)})
		}

		// Retail images include an empty block after the flat path table, and
		// the outer image keeps one unencrypted block of zeroes.
		b.hdr.ndblock++
		b.emptyBlock = b.hdr.ndblock
		b.hdr.ndblock++

		ibStartBlock := b.hdr.ndblock
		for _, node := range b.allNodes {
			b.hdr.ndblock += b.indirectBlockCount(node.nodeSize())
		}

		sigsPerBlock := blockSize / 36
		for _, node := range b.allNodes {
			ino := node.getInode()
			blocks := ceilDiv(node.nodeSize(), blockSize)
			ino.setDirectBlock(0, int32(b.hdr.ndblock))
			ino.blocks = uint32(blocks)
			if _, isDir := node.(*fsDir); isDir {
				ino.size = b.roundUpToBlock(node.nodeSize())
			} else {
				ino.size = node.nodeSize()
			}
			if ino.sizeComp == 0 {
				ino.sizeComp = ino.size
			}
			for i := 0; blocks-int64(i) > 0 && i < 12; i++ {
				b.pushData(blockSigInfo{block: b.hdr.ndblock, sigOffset: b.inoNumberToOffset(ino.number, i)})
				b.hdr.ndblock++
			}
			if blocks > 12 {
				b.pushFinal(blockSigInfo{block: ibStartBlock, sigOffset: b.inoNumberToOffset(ino.number, 12)})
				ino.ib[0] = int32(ibStartBlock)
				for i, pointerOffset := int64(12), int64(0); blocks-i > 0 && i < 12+sigsPerBlock; i, pointerOffset = i+1, pointerOffset+36 {
					b.pushData(blockSigInfo{block: b.hdr.ndblock, sigOffset: ibStartBlock*blockSize + pointerOffset})
					b.hdr.ndblock++
				}
				ibStartBlock++
			}
			if blocks > 12+sigsPerBlock {
				blockSigsDone := 12 + sigsPerBlock
				b.pushFinal(blockSigInfo{block: ibStartBlock, sigOffset: b.inoNumberToOffset(ino.number, 13)})
				ino.ib[1] = int32(ibStartBlock)
				ib1Block := ibStartBlock
				for i := int64(0); i < sigsPerBlock && blockSigsDone < blocks; i++ {
					ibStartBlock++
					b.pushFinal(blockSigInfo{block: ibStartBlock, sigOffset: ib1Block*blockSize + i*36})
					for j := int64(0); j < sigsPerBlock && blockSigsDone < blocks; j, blockSigsDone = j+1, blockSigsDone+1 {
						b.pushData(blockSigInfo{block: b.hdr.ndblock, sigOffset: ibStartBlock*blockSize + j*36})
						b.hdr.ndblock++
					}
				}
			}
		}
	} else {
		b.hdr.ndblock = 1
		inodesPerBlock := blockSize / dinodeD32Size
		b.hdr.dinodeCount = int64(len(b.inodes))
		b.hdr.dinodeBlkCount = ceilDiv(int64(len(b.inodes)), inodesPerBlock)
		b.hdr.inodeBlockSig.blocks = uint32(b.hdr.dinodeBlkCount)
		b.hdr.inodeBlockSig.size = b.hdr.dinodeBlkCount * blockSize
		b.hdr.inodeBlockSig.sizeComp = b.hdr.dinodeBlkCount * blockSize
		b.hdr.inodeBlockSig.setDirectBlock(0, int32(b.hdr.ndblock))
		b.hdr.ndblock++
		b.hdr.inodeBlockSig.time = b.props.fileTime
		for i := int64(1); i < b.hdr.dinodeBlkCount; i++ {
			if i < 12 {
				b.hdr.inodeBlockSig.setDirectBlock(int(i), -1)
			}
			b.hdr.ndblock++
		}
		b.superRootIno.setDirectBlock(0, int32(b.hdr.ndblock))
		b.hdr.ndblock += int64(b.superRootIno.blocks)

		b.fptIno.setDirectBlock(0, int32(b.hdr.ndblock))
		b.hdr.ndblock++
		b.fptIno.size = b.fpt.size()
		b.fptIno.sizeComp = b.fpt.size()
		b.fptIno.blocks = uint32(ceilDiv(b.fpt.size(), blockSize))
		for i := 1; int64(i) < int64(b.fptIno.blocks) && i < 12; i++ {
			b.fptIno.setDirectBlock(i, int32(b.hdr.ndblock))
			b.hdr.ndblock++
		}

		if b.crIno == nil {
			b.hdr.ndblock++
		} else {
			b.crIno.setDirectBlock(0, int32(b.hdr.ndblock))
			b.hdr.ndblock++
			b.crIno.size = int64(b.resolver.size)
			b.crIno.sizeComp = int64(b.resolver.size)
			b.crIno.blocks = uint32(ceilDiv(int64(b.resolver.size), blockSize))
			for i := 1; int64(i) < int64(b.crIno.blocks) && i < 12; i++ {
				b.crIno.setDirectBlock(i, int32(b.hdr.ndblock))
				b.hdr.ndblock++
			}
		}

		for _, node := range b.allNodes {
			ino := node.getInode()
			blocks := ceilDiv(node.nodeSize(), blockSize)
			ino.setDirectBlock(0, int32(b.hdr.ndblock))
			ino.blocks = uint32(blocks)
			if _, isDir := node.(*fsDir); isDir {
				ino.size = b.roundUpToBlock(node.nodeSize())
			} else {
				ino.size = node.nodeSize()
			}
			if ino.sizeComp == 0 {
				ino.sizeComp = ino.size
			}
			for i := 1; int64(i) < blocks && i < 12; i++ {
				ino.setDirectBlock(i, -1)
			}
			b.hdr.ndblock += blocks
		}
	}
	if b.hdr.ndblock < b.props.minBlocks {
		b.hdr.ndblock = b.props.minBlocks
	}
	if b.hdr.ndblock*blockSize < 0 {
		return fmt.Errorf("PFS image size overflow")
	}
	return nil
}

// ctx returns the build context, defaulting to the background context.
func (b *pfsBuilder) ctx() context.Context {
	if b.props.ctx == nil {
		return context.Background()
	}
	return b.props.ctx
}

// size returns the final image size in bytes.
func (b *pfsBuilder) size() int64 { return b.hdr.ndblock * int64(b.hdr.blockSize) }

// writeData writes the filesystem contents (unsigned and unencrypted).
func (b *pfsBuilder) writeData(target readerWriterAt, base int64) error {
	blockSize := int64(b.hdr.blockSize)
	w := io.NewOffsetWriter(target, base)
	if _, err := w.WriteAt(b.hdr.marshal(), 0); err != nil {
		return err
	}
	if err := b.writeInodes(w); err != nil {
		return err
	}
	offset := blockSize * (b.hdr.dinodeBlkCount + 1)
	superRoot := make([]byte, 0, 256)
	for _, dirent := range b.superRootDirents {
		superRoot = append(superRoot, dirent.marshal()...)
	}
	if _, err := w.WriteAt(superRoot, offset); err != nil {
		return err
	}

	nodes := make([]fsNode, 0, len(b.allNodes)+2)
	fptData := b.fpt.marshal()
	nodes = append(nodes, &fsFile{
		name: "flat_path_table", size: int64(len(fptData)), ino: b.fptIno,
		write: func(w io.Writer) error { _, err := w.Write(fptData); return err },
	})
	if b.resolver != nil {
		resolverData := b.resolver.marshal()
		nodes = append(nodes, &fsFile{
			name: "collision_resolver", size: int64(len(resolverData)), ino: b.crIno,
			write: func(w io.Writer) error { _, err := w.Write(resolverData); return err },
		})
	}
	nodes = append(nodes, b.allNodes...)

	for _, node := range nodes {
		start := int64(node.getInode().startBlock()) * blockSize
		switch typed := node.(type) {
		case *fsDir:
			data, err := b.marshalDirents(typed)
			if err != nil {
				return err
			}
			if _, err := w.WriteAt(data, start); err != nil {
				return err
			}
		case *fsFile:
			switch {
			case typed.writeAt != nil:
				if err := typed.writeAt(target, base+start); err != nil {
					return fmt.Errorf("write %s: %w", typed.name, err)
				}
			case typed.write != nil:
				if err := typed.write(io.NewOffsetWriter(w, start)); err != nil {
					return fmt.Errorf("write %s: %w", typed.name, err)
				}
			default:
				return fmt.Errorf("file %q has no contents", typed.name)
			}
		}
	}
	return nil
}

func (b *pfsBuilder) marshalDirents(dir *fsDir) ([]byte, error) {
	blockSize := int(b.hdr.blockSize)
	capacity := int(dir.ino.blocks) * blockSize
	if capacity == 0 {
		capacity = blockSize
	}
	out := make([]byte, capacity)
	offset := 0
	for _, dirent := range dir.dirents {
		if offset+dirent.entSize() > len(out) {
			return nil, fmt.Errorf("directory %q does not fit in its allocated blocks", dir.name)
		}
		copy(out[offset:], dirent.marshal())
		offset += dirent.entSize()
		if offset%blockSize > blockSize-direntMaxSize {
			offset += blockSize - (offset % blockSize)
		}
	}
	return out, nil
}

func (b *pfsBuilder) writeInodes(w io.WriterAt) error {
	blockSize := int64(b.hdr.blockSize)
	inodeSize := int64(dinodeD32Size)
	if b.props.sign {
		inodeSize = dinodeS32Size
	}
	buffer := make([]byte, b.hdr.dinodeBlkCount*blockSize)
	offset := int64(0)
	for _, ino := range b.inodes {
		var data []byte
		if b.props.sign {
			data = ino.marshalS32()
		} else {
			data = ino.marshalD32()
		}
		if offset+int64(len(data)) > int64(len(buffer)) {
			return fmt.Errorf("inode table overflow")
		}
		copy(buffer[offset:], data)
		offset += int64(len(data))
		if offset%blockSize > blockSize-inodeSize {
			offset += blockSize - (offset % blockSize)
		}
	}
	_, err := w.WriteAt(buffer, blockSize)
	return err
}

// xtsSectors lists the sectors that get encrypted, skipping the empty block.
func (b *pfsBuilder) xtsSectors() []int64 {
	total := (b.size() + 0xFFF) / xtsSectorSize
	sectors := make([]int64, 0, total)
	for sector := int64(16); sector < total; sector++ {
		if sector/0x10 == b.emptyBlock {
			sector += 16
			if sector >= total {
				break
			}
		}
		sectors = append(sectors, sector)
	}
	return sectors
}

// writeImage writes, signs and encrypts the image at the given base offset.
func (b *pfsBuilder) writeImage(target readerWriterAt, base int64) error {
	if err := b.writeData(target, base); err != nil {
		return err
	}
	if b.hdr.signed() {
		if err := b.signImage(target, base); err != nil {
			return err
		}
	}
	if b.hdr.encrypted() {
		if err := b.encryptImage(target, base); err != nil {
			return err
		}
	}
	return nil
}

func (b *pfsBuilder) signImage(target readerWriterAt, base int64) error {
	signKey := pfsGenSignKey(b.props.ekpfs, b.hdr.seed)
	blockSize := int64(b.hdr.blockSize)
	sign := func(sig blockSigInfo) error {
		if err := b.ctx().Err(); err != nil {
			return err
		}
		buffer := make([]byte, sig.size)
		if _, err := target.ReadAt(buffer, base+sig.block*blockSize); err != nil {
			return err
		}
		out := make([]byte, 36)
		copy(out, hmacSHA256(signKey, buffer))
		binary.LittleEndian.PutUint32(out[32:], uint32(sig.block))
		_, err := target.WriteAt(out, base+sig.sigOffset)
		return err
	}
	// Data block signatures are independent of each other.
	if err := parallelDo(len(b.dataSigs), func(index int) error { return sign(b.dataSigs[index]) }); err != nil {
		return err
	}
	// Indirect blocks and the superblock digest depend on earlier signatures,
	// so they are processed in reverse push order.
	for i := len(b.finalSigs) - 1; i >= 0; i-- {
		if err := sign(b.finalSigs[i]); err != nil {
			return err
		}
	}
	return nil
}

func (b *pfsBuilder) encryptImage(target readerWriterAt, base int64) error {
	tweakKey, dataKey := pfsGenEncKey(b.props.ekpfs, b.hdr.seed, false)
	sectors := b.xtsSectors()
	imageSize := b.size()
	return parallelDo(len(sectors), func(index int) error {
		if err := b.ctx().Err(); err != nil {
			return err
		}
		sector := sectors[index]
		offset := sector * xtsSectorSize
		length := int64(xtsSectorSize)
		if offset+length > imageSize {
			length = imageSize - offset
		}
		if length <= 0 {
			return nil
		}
		transform, err := newXTSTransform(dataKey, tweakKey)
		if err != nil {
			return err
		}
		buffer := make([]byte, length)
		if _, err := target.ReadAt(buffer, base+offset); err != nil {
			return err
		}
		transform.encryptSector(buffer, uint64(sector))
		_, err = target.WriteAt(buffer, base+offset)
		return err
	})
}

// parallelDo runs work across the available CPUs, returning the first error.
func parallelDo(count int, work func(index int) error) error {
	if count <= 0 {
		return nil
	}
	workers := runtime.NumCPU()
	if workers > count {
		workers = count
	}
	if workers < 1 {
		workers = 1
	}
	indexes := make(chan int, workers)
	var (
		wg       sync.WaitGroup
		once     sync.Once
		failed   atomic.Bool
		firstErr error
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range indexes {
				if failed.Load() {
					continue
				}
				if err := work(index); err != nil {
					once.Do(func() { firstErr = err })
					failed.Store(true)
				}
			}
		}()
	}
	for i := 0; i < count; i++ {
		indexes <- i
	}
	close(indexes)
	wg.Wait()
	return firstErr
}
