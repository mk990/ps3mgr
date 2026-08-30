package orbis

import (
	"fmt"
	"io"
	"strings"
)

// xtsDecryptReader decrypts an AES-XTS encrypted image on the fly.
type xtsDecryptReader struct {
	source      io.ReaderAt
	transform   *xtsTransform
	startSector int64
	sectorSize  int64
}

func newXtsDecryptReader(source io.ReaderAt, dataKey, tweakKey []byte, startSector, sectorSize int64) (*xtsDecryptReader, error) {
	transform, err := newXTSTransform(dataKey, tweakKey)
	if err != nil {
		return nil, err
	}
	return &xtsDecryptReader{source: source, transform: transform, startSector: startSector, sectorSize: sectorSize}, nil
}

func (x *xtsDecryptReader) ReadAt(out []byte, offset int64) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	first := offset / x.sectorSize
	last := (offset + int64(len(out)) - 1) / x.sectorSize
	buffer := make([]byte, x.sectorSize)
	written := 0
	for sector := first; sector <= last; sector++ {
		if _, err := x.source.ReadAt(buffer, sector*x.sectorSize); err != nil {
			return written, err
		}
		if sector >= x.startSector {
			x.transform.decryptSector(buffer, uint64(sector))
		}
		start := int64(0)
		if sector == first {
			start = offset % x.sectorSize
		}
		written += copy(out[written:], buffer[start:])
	}
	return written, nil
}

// chunkedReader maps a file made of non contiguous PFS blocks.
type chunkedReader struct {
	source    io.ReaderAt
	blockSize int64
	blocks    []int32
}

func (c *chunkedReader) ReadAt(out []byte, offset int64) (int, error) {
	written := 0
	for written < len(out) {
		position := offset + int64(written)
		index := position / c.blockSize
		if index < 0 || index >= int64(len(c.blocks)) {
			return written, io.EOF
		}
		into := position % c.blockSize
		length := int64(len(out) - written)
		if length > c.blockSize-into {
			length = c.blockSize - into
		}
		read, err := c.source.ReadAt(out[written:written+int(length)], int64(c.blocks[index])*c.blockSize+into)
		written += read
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// PfsNode is a file or directory inside a PFS image.
type PfsNode struct {
	Name     string
	Path     string
	IsDir    bool
	Size     int64
	CompSize int64
	Flags    uint32
	Children []*PfsNode

	offset int64
	blocks []int32
	reader *pfsReader
}

// Compressed reports whether the node holds a PFSC container.
func (n *PfsNode) Compressed() bool { return n.Flags&inodeFlagCompressed != 0 }

// Reader returns a reader over the raw contents of a file node.
func (n *PfsNode) Reader() io.ReaderAt {
	if n.blocks != nil {
		return &chunkedReader{source: n.reader.source, blockSize: int64(n.reader.hdr.blockSize), blocks: n.blocks}
	}
	return io.NewSectionReader(n.reader.source, n.offset, n.Size)
}

// Find returns the descendant at the given slash separated path.
func (n *PfsNode) Find(path string) *PfsNode {
	current := n
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		var next *PfsNode
		for _, child := range current.Children {
			if strings.EqualFold(child.Name, part) {
				next = child
				break
			}
		}
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

// pfsReader parses a PFS image.
type pfsReader struct {
	source io.ReaderAt
	hdr    *pfsHeader
	inodes []inode
	root   *PfsNode
	uroot  *PfsNode
}

// newPfsReader parses the image, decrypting it when a key is supplied.
func newPfsReader(source io.ReaderAt, ekpfs []byte) (*pfsReader, error) {
	head := make([]byte, 0x400)
	if _, err := source.ReadAt(head, 0); err != nil {
		return nil, fmt.Errorf("read PFS superblock: %w", err)
	}
	hdr, err := parsePfsHeader(head)
	if err != nil {
		return nil, err
	}
	reader := &pfsReader{source: source, hdr: hdr}
	if hdr.encrypted() {
		if len(ekpfs) == 0 {
			return nil, fmt.Errorf("PFS image is encrypted but no key was provided")
		}
		tweakKey, dataKey := pfsGenEncKey(ekpfs, hdr.seed, false)
		decrypted, err := newXtsDecryptReader(source, dataKey, tweakKey, int64(hdr.blockSize)/xtsSectorSize, xtsSectorSize)
		if err != nil {
			return nil, err
		}
		reader.source = decrypted
	}
	if hdr.dinodeCount < 0 || hdr.dinodeCount > 1<<22 {
		return nil, fmt.Errorf("PFS image declares an implausible inode count: %d", hdr.dinodeCount)
	}
	inodeSize := int64(dinodeD32Size)
	if hdr.signed() {
		inodeSize = dinodeS32Size
	}
	blockSize := int64(hdr.blockSize)
	perBlock := blockSize / inodeSize
	// Skip indirect blocks; block signatures are not verified here.
	indirect := int64(0)
	for _, block := range hdr.inodeBlockSig.ib {
		if block > 0 {
			indirect++
		}
	}
	start := blockSize + blockSize*indirect
	reader.inodes = make([]inode, 0, hdr.dinodeCount)
	buffer := make([]byte, blockSize)
	for i := int64(0); i < hdr.dinodeBlkCount && int64(len(reader.inodes)) < hdr.dinodeCount; i++ {
		if _, err := reader.source.ReadAt(buffer, start+blockSize*i); err != nil {
			return nil, fmt.Errorf("read PFS inode block %d: %w", i, err)
		}
		for j := int64(0); j < perBlock && int64(len(reader.inodes)) < hdr.dinodeCount; j++ {
			data := buffer[j*inodeSize : (j+1)*inodeSize]
			if hdr.signed() {
				reader.inodes = append(reader.inodes, parseDinodeS32(data))
			} else {
				reader.inodes = append(reader.inodes, parseDinodeD32(data))
			}
		}
	}
	root, err := reader.loadDir(0, "", "")
	if err != nil {
		return nil, err
	}
	reader.root = root
	for _, child := range root.Children {
		if child.Name == "uroot" {
			reader.uroot = child
		}
	}
	if reader.uroot == nil {
		return nil, fmt.Errorf("PFS image has no uroot directory")
	}
	return reader, nil
}

func (p *pfsReader) loadDir(number uint32, name, path string) (*PfsNode, error) {
	if int(number) >= len(p.inodes) {
		return nil, fmt.Errorf("PFS inode %d is out of range", number)
	}
	ino := p.inodes[number]
	node := &PfsNode{Name: name, Path: path, IsDir: true, Size: ino.size, reader: p}
	blockSize := int64(p.hdr.blockSize)
	if ino.blocks < 1 || ino.startBlock() < 1 || int64(ino.blocks) > 1<<20 {
		return nil, fmt.Errorf("PFS directory inode %d is corrupt", number)
	}
	buffer := make([]byte, blockSize)
	type pending struct {
		number uint32
		name   string
	}
	var subdirs []pending
	for block := int64(0); block < int64(ino.blocks); block++ {
		if _, err := p.source.ReadAt(buffer, (int64(ino.startBlock())+block)*blockSize); err != nil {
			return nil, fmt.Errorf("read directory block: %w", err)
		}
		offset := 0
		for offset < len(buffer) {
			dirent, size := parseDirent(buffer[offset:])
			if size == 0 {
				break
			}
			offset += size
			switch dirent.direntType {
			case direntFile:
				child, err := p.loadFile(dirent.inodeNumber, dirent.name, joinPath(path, dirent.name))
				if err != nil {
					return nil, err
				}
				node.Children = append(node.Children, child)
			case direntDirectory:
				subdirs = append(subdirs, pending{number: dirent.inodeNumber, name: dirent.name})
			}
		}
	}
	for _, sub := range subdirs {
		child, err := p.loadDir(sub.number, sub.name, joinPath(path, sub.name))
		if err != nil {
			return nil, err
		}
		node.Children = append(node.Children, child)
	}
	return node, nil
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func (p *pfsReader) loadFile(number uint32, name, path string) (*PfsNode, error) {
	if int(number) >= len(p.inodes) {
		return nil, fmt.Errorf("PFS inode %d is out of range", number)
	}
	ino := p.inodes[number]
	blockSize := int64(p.hdr.blockSize)
	node := &PfsNode{
		Name:     name,
		Path:     path,
		Size:     ino.size,
		CompSize: ino.sizeComp,
		Flags:    ino.flags,
		offset:   int64(ino.startBlock()) * blockSize,
		reader:   p,
	}
	if ino.blocks > 1 && ino.db[1] != -1 && p.hdr.signed() {
		blocks, err := p.readBlockList(ino)
		if err != nil {
			return nil, err
		}
		node.blocks = blocks
	}
	return node, nil
}

// readBlockList resolves the direct and indirect block pointers of an inode.
func (p *pfsReader) readBlockList(ino inode) ([]int32, error) {
	blockSize := int64(p.hdr.blockSize)
	sigsPerBlock := blockSize / 36
	total := int64(ino.blocks)
	if total > 1<<24 {
		return nil, fmt.Errorf("PFS file claims %d blocks", total)
	}
	blocks := make([]int32, total)
	for i := int64(0); i < 12 && i < total; i++ {
		blocks[i] = ino.db[i]
	}
	pointer := make([]byte, 4)
	readPointer := func(offset int64) (int32, error) {
		if _, err := p.source.ReadAt(pointer, offset); err != nil {
			return 0, err
		}
		return int32(uint32(pointer[0]) | uint32(pointer[1])<<8 | uint32(pointer[2])<<16 | uint32(pointer[3])<<24), nil
	}
	index := int64(12)
	for i := int64(0); index < total && i < sigsPerBlock; i, index = i+1, index+1 {
		block, err := readPointer(int64(ino.ib[0])*blockSize + i*36 + 32)
		if err != nil {
			return nil, err
		}
		blocks[index] = block
	}
	for j := int64(0); index < total; j++ {
		indirect, err := readPointer(int64(ino.ib[1])*blockSize + j*36 + 32)
		if err != nil {
			return nil, err
		}
		for i := int64(0); i < sigsPerBlock && index < total; i, index = i+1, index+1 {
			block, err := readPointer(int64(indirect)*blockSize + i*36 + 32)
			if err != nil {
				return nil, err
			}
			blocks[index] = block
		}
	}
	contiguous := true
	for i := 1; i < len(blocks); i++ {
		if blocks[i-1]+1 != blocks[i] {
			contiguous = false
			break
		}
	}
	if contiguous {
		return nil, nil
	}
	return blocks, nil
}
