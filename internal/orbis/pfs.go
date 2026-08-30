package orbis

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"
)

// PFS mode flags.
const (
	pfsModeSigned    = 0x1
	pfsModeEncrypted = 0x4
	pfsModeAlwaysSet = 0x8
)

// Inode mode bits.
const (
	inodeModeDir  = 16384
	inodeModeFile = 32768
	inodeModeRX   = 1 | 4 | 8 | 32 | 64 | 256 // r-x for other, group and user
)

// Inode flags.
const (
	inodeFlagCompressed = 0x1
	inodeFlagUnk1       = 0x2
	inodeFlagUnk2       = 0x4
	inodeFlagReadOnly   = 0x10
	inodeFlagInternal   = 0x20000
)

// Dirent types.
const (
	direntFile      = 2
	direntDirectory = 3
	direntDot       = 4
	direntDotDot    = 5
)

const (
	dinodeD32Size = 0xA8
	dinodeS32Size = 0x2C8
	dinodeS64Size = 0x310
	direntMaxSize = 280
	xtsSectorSize = 0x1000
)

// inode describes a file or directory in a PFS image. The same structure backs
// the 32-bit unsigned (inner) and signed (outer) on-disk variants; block
// signatures are patched into the image after the data is written.
type inode struct {
	number   uint32
	mode     uint16
	nlink    uint16
	flags    uint32
	size     int64
	sizeComp int64
	time     int64
	blocks   uint32
	db       [12]int32
	ib       [5]int32
}

func (i *inode) setDirectBlock(index int, block int32) { i.db[index] = block }
func (i *inode) startBlock() int32                     { return i.db[0] }

// writePrefix writes the fields shared by every inode variant.
func (i *inode) writePrefix(out []byte) {
	binary.LittleEndian.PutUint16(out[0:], i.mode)
	binary.LittleEndian.PutUint16(out[2:], i.nlink)
	binary.LittleEndian.PutUint32(out[4:], i.flags)
	binary.LittleEndian.PutUint64(out[8:], uint64(i.size))
	binary.LittleEndian.PutUint64(out[16:], uint64(i.sizeComp))
	for t := 0; t < 4; t++ {
		binary.LittleEndian.PutUint64(out[24+t*8:], uint64(i.time))
	}
	// nanoseconds, uid and gid stay zero
	binary.LittleEndian.PutUint32(out[96:], i.blocks)
}

// marshalD32 returns the unsigned 32-bit on-disk inode.
func (i *inode) marshalD32() []byte {
	out := make([]byte, dinodeD32Size)
	i.writePrefix(out)
	for index, block := range i.db {
		binary.LittleEndian.PutUint32(out[100+index*4:], uint32(block))
	}
	for index, block := range i.ib {
		binary.LittleEndian.PutUint32(out[148+index*4:], uint32(block))
	}
	return out
}

// marshalS32 returns the signed 32-bit on-disk inode. Signatures are zero here
// and patched in once the referenced blocks have been written.
func (i *inode) marshalS32() []byte {
	out := make([]byte, dinodeS32Size)
	i.writePrefix(out)
	for index, block := range i.db {
		binary.LittleEndian.PutUint32(out[100+index*36+32:], uint32(block))
	}
	for index, block := range i.ib {
		binary.LittleEndian.PutUint32(out[100+12*36+index*36+32:], uint32(block))
	}
	return out
}

// marshalS64 returns the signed 64-bit inode embedded in a PFS superblock.
func (i *inode) marshalS64() []byte {
	out := make([]byte, dinodeS64Size)
	i.writePrefix(out)
	binary.LittleEndian.PutUint64(out[96:], uint64(i.blocks))
	for index, block := range i.db {
		binary.LittleEndian.PutUint64(out[104+index*40+32:], uint64(block))
	}
	for index, block := range i.ib {
		binary.LittleEndian.PutUint64(out[104+12*40+index*40+32:], uint64(block))
	}
	return out
}

func parseInodePrefix(data []byte, i *inode) {
	i.mode = binary.LittleEndian.Uint16(data[0:])
	i.nlink = binary.LittleEndian.Uint16(data[2:])
	i.flags = binary.LittleEndian.Uint32(data[4:])
	i.size = int64(binary.LittleEndian.Uint64(data[8:]))
	i.sizeComp = int64(binary.LittleEndian.Uint64(data[16:]))
	i.time = int64(binary.LittleEndian.Uint64(data[24:]))
}

func parseDinodeD32(data []byte) inode {
	var i inode
	parseInodePrefix(data, &i)
	i.blocks = binary.LittleEndian.Uint32(data[96:])
	for index := 0; index < 12; index++ {
		i.db[index] = int32(binary.LittleEndian.Uint32(data[100+index*4:]))
	}
	for index := 0; index < 5; index++ {
		i.ib[index] = int32(binary.LittleEndian.Uint32(data[148+index*4:]))
	}
	return i
}

func parseDinodeS32(data []byte) inode {
	var i inode
	parseInodePrefix(data, &i)
	i.blocks = binary.LittleEndian.Uint32(data[96:])
	for index := 0; index < 12; index++ {
		i.db[index] = int32(binary.LittleEndian.Uint32(data[100+index*36+32:]))
	}
	for index := 0; index < 5; index++ {
		i.ib[index] = int32(binary.LittleEndian.Uint32(data[100+12*36+index*36+32:]))
	}
	return i
}

func parseDinodeS64(data []byte) inode {
	var i inode
	parseInodePrefix(data, &i)
	i.blocks = uint32(binary.LittleEndian.Uint64(data[96:]))
	for index := 0; index < 12; index++ {
		i.db[index] = int32(binary.LittleEndian.Uint64(data[104+index*40+32:]))
	}
	for index := 0; index < 5; index++ {
		i.ib[index] = int32(binary.LittleEndian.Uint64(data[104+12*40+index*40+32:]))
	}
	return i
}

// pfsHeader is the PFS superblock.
type pfsHeader struct {
	version        int64
	magic          int64
	id             int64
	mode           uint16
	blockSize      uint32
	nblock         int64
	dinodeCount    int64
	ndblock        int64
	dinodeBlkCount int64
	inodeBlockSig  inode
	unknownIndex   int32
	readOnly       byte
	seed           []byte
}

func newPfsHeader() *pfsHeader {
	return &pfsHeader{
		version:   1,
		magic:     20130315,
		nblock:    1,
		blockSize: 0x10000,
		mode:      pfsModeAlwaysSet,
		inodeBlockSig: inode{
			nlink:    1,
			flags:    inodeFlagReadOnly,
			size:     0x10000,
			sizeComp: 0x10000,
			blocks:   1,
		},
	}
}

func (h *pfsHeader) signed() bool    { return h.mode&pfsModeSigned != 0 }
func (h *pfsHeader) encrypted() bool { return h.mode&pfsModeEncrypted != 0 }

// marshal renders the superblock into a full block-sized buffer.
func (h *pfsHeader) marshal() []byte {
	out := make([]byte, 0x400)
	binary.LittleEndian.PutUint64(out[0:], uint64(h.version))
	binary.LittleEndian.PutUint64(out[8:], uint64(h.magic))
	binary.LittleEndian.PutUint64(out[16:], uint64(h.id))
	out[27] = 0
	out[26] = h.readOnly
	binary.LittleEndian.PutUint16(out[28:], h.mode)
	binary.LittleEndian.PutUint32(out[32:], h.blockSize)
	binary.LittleEndian.PutUint64(out[40:], uint64(h.nblock))
	binary.LittleEndian.PutUint64(out[48:], uint64(h.dinodeCount))
	binary.LittleEndian.PutUint64(out[56:], uint64(h.ndblock))
	binary.LittleEndian.PutUint64(out[64:], uint64(h.dinodeBlkCount))
	copy(out[0x50:], h.inodeBlockSig.marshalS64())
	if h.seed != nil {
		binary.LittleEndian.PutUint32(out[0x36C:], uint32(h.unknownIndex))
		copy(out[0x370:], h.seed)
	} else {
		binary.LittleEndian.PutUint32(out[0x368:], 1)
	}
	return out
}

func parsePfsHeader(data []byte) (*pfsHeader, error) {
	if len(data) < 0x400 {
		return nil, fmt.Errorf("PFS superblock is truncated")
	}
	h := &pfsHeader{
		version:        int64(binary.LittleEndian.Uint64(data[0:])),
		magic:          int64(binary.LittleEndian.Uint64(data[8:])),
		id:             int64(binary.LittleEndian.Uint64(data[16:])),
		readOnly:       data[26],
		mode:           binary.LittleEndian.Uint16(data[28:]),
		blockSize:      binary.LittleEndian.Uint32(data[32:]),
		nblock:         int64(binary.LittleEndian.Uint64(data[40:])),
		dinodeCount:    int64(binary.LittleEndian.Uint64(data[48:])),
		ndblock:        int64(binary.LittleEndian.Uint64(data[56:])),
		dinodeBlkCount: int64(binary.LittleEndian.Uint64(data[64:])),
	}
	if h.version != 1 || h.magic != 20130315 {
		return nil, fmt.Errorf("invalid PFS superblock (version %d, magic %d)", h.version, h.magic)
	}
	if h.blockSize == 0 || h.blockSize > 1<<24 {
		return nil, fmt.Errorf("invalid PFS block size %d", h.blockSize)
	}
	h.inodeBlockSig = parseDinodeS64(data[0x50:])
	h.seed = append([]byte(nil), data[0x370:0x380]...)
	return h, nil
}

// pfsDirent is a directory entry.
type pfsDirent struct {
	inodeNumber uint32
	direntType  int32
	name        string
}

func (d pfsDirent) entSize() int {
	size := len(d.name) + 17
	if remainder := size % 8; remainder != 0 {
		size += 8 - remainder
	}
	return size
}

func (d pfsDirent) marshal() []byte {
	out := make([]byte, d.entSize())
	binary.LittleEndian.PutUint32(out[0:], d.inodeNumber)
	binary.LittleEndian.PutUint32(out[4:], uint32(d.direntType))
	binary.LittleEndian.PutUint32(out[8:], uint32(len(d.name)))
	binary.LittleEndian.PutUint32(out[12:], uint32(d.entSize()))
	copy(out[16:], d.name)
	return out
}

func parseDirent(data []byte) (pfsDirent, int) {
	if len(data) < 16 {
		return pfsDirent{}, 0
	}
	d := pfsDirent{
		inodeNumber: binary.LittleEndian.Uint32(data[0:]),
		direntType:  int32(binary.LittleEndian.Uint32(data[4:])),
	}
	nameLength := int(binary.LittleEndian.Uint32(data[8:]))
	entSize := int(binary.LittleEndian.Uint32(data[12:]))
	if entSize <= 0 || entSize > len(data) || nameLength < 0 || 16+nameLength > len(data) {
		return pfsDirent{}, 0
	}
	name := data[16 : 16+nameLength]
	if index := indexByteSafe(name); index >= 0 {
		name = name[:index]
	}
	d.name = string(name)
	return d, entSize
}

func indexByteSafe(data []byte) int {
	for i, b := range data {
		if b == 0 {
			return i
		}
	}
	return -1
}

// fsNode is a file or directory in a PFS image being built.
type fsNode interface {
	nodeName() string
	nodeParent() *fsDir
	nodeSize() int64
	compressedSize() int64
	getInode() *inode
	setInode(*inode)
}

// fsFile is a file to be written into a PFS image.
type fsFile struct {
	name     string
	parent   *fsDir
	size     int64
	compSize int64
	compress bool
	// write emits exactly size bytes sequentially.
	write func(w io.Writer) error
	// writeAt writes the file at an absolute offset of the target when the
	// contents need random access, as the nested PFS image does.
	writeAt func(target readerWriterAt, offset int64) error
	ino     *inode
}

func (f *fsFile) nodeName() string   { return f.name }
func (f *fsFile) nodeParent() *fsDir { return f.parent }
func (f *fsFile) nodeSize() int64    { return f.size }
func (f *fsFile) compressedSize() int64 {
	if f.compSize != 0 {
		return f.compSize
	}
	return f.size
}
func (f *fsFile) getInode() *inode  { return f.ino }
func (f *fsFile) setInode(i *inode) { f.ino = i }

// fsDir is a directory to be written into a PFS image.
type fsDir struct {
	name    string
	parent  *fsDir
	dirs    []*fsDir
	files   []*fsFile
	dirents []pfsDirent
	ino     *inode
}

func (d *fsDir) nodeName() string   { return d.name }
func (d *fsDir) nodeParent() *fsDir { return d.parent }
func (d *fsDir) nodeSize() int64 {
	total := int64(0)
	for _, dirent := range d.dirents {
		total += int64(dirent.entSize())
	}
	return total
}
func (d *fsDir) compressedSize() int64 { return d.nodeSize() }
func (d *fsDir) getInode() *inode      { return d.ino }
func (d *fsDir) setInode(i *inode)     { d.ino = i }

func fullPath(node fsNode) string {
	parts := []string{}
	var walk func(n fsNode)
	walk = func(n fsNode) {
		if n == nil || n.nodeParent() == nil {
			return
		}
		parts = append([]string{n.nodeName()}, parts...)
		walk(n.nodeParent())
	}
	walk(node)
	if len(parts) == 0 {
		return ""
	}
	return "/" + strings.Join(parts, "/")
}

// allDirs returns every directory below this one, deepest last.
func (d *fsDir) allDirs() []*fsDir {
	out := append([]*fsDir{}, d.dirs...)
	for _, child := range d.dirs {
		out = append(out, child.allDirs()...)
	}
	return out
}

// allFiles returns every file below this directory.
func (d *fsDir) allFiles() []*fsFile {
	out := append([]*fsFile{}, d.files...)
	for _, child := range d.allDirs() {
		out = append(out, child.files...)
	}
	return out
}

// getDir returns the child directory with the given relative path.
func (d *fsDir) getDir(path string) *fsDir {
	current := d
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		var next *fsDir
		for _, child := range current.dirs {
			if child.name == part {
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

// getFile returns the file at the given relative path.
func (d *fsDir) getFile(path string) *fsFile {
	index := strings.LastIndex(path, "/")
	dir := d
	name := path
	if index >= 0 {
		dir = d.getDir(path[:index])
		name = path[index+1:]
	}
	if dir == nil {
		return nil
	}
	for _, file := range dir.files {
		if file.name == name {
			return file
		}
	}
	return nil
}

// addFile inserts a file with in-memory contents at the given directory path.
func (d *fsDir) addFile(dirPath, name string, data []byte) error {
	dir := d.getDir(dirPath)
	if dir == nil {
		return fmt.Errorf("directory %q does not exist in the image", dirPath)
	}
	dir.files = append(dir.files, &fsFile{
		name:   name,
		parent: dir,
		size:   int64(len(data)),
		write:  func(w io.Writer) error { _, err := w.Write(data); return err },
	})
	return nil
}

func sortDirsByPath(dirs []*fsDir) {
	sort.SliceStable(dirs, func(i, j int) bool { return fullPath(dirs[i]) < fullPath(dirs[j]) })
}

func sortFilesByPath(files []*fsFile) {
	sort.SliceStable(files, func(i, j int) bool { return fullPath(files[i]) < fullPath(files[j]) })
}

// flatPathTable maps uppercased path hashes to inode numbers.
type flatPathTable struct {
	hashes []uint32
	values map[uint32]uint32
}

func pathHash(name string) uint32 {
	var hash uint32
	for _, c := range name {
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		hash = uint32(c) + 31*hash
	}
	return hash
}

// collisionResolver stores full paths for entries whose hashes collide.
type collisionResolver struct {
	entries [][]pfsDirent
	size    int
}

func hasHashCollision(nodes []fsNode) bool {
	seen := make(map[uint32]bool, len(nodes))
	for _, node := range nodes {
		hash := pathHash(fullPath(node))
		if seen[hash] {
			return true
		}
		seen[hash] = true
	}
	return false
}

func newFlatPathTable(nodes []fsNode) (*flatPathTable, *collisionResolver) {
	values := make(map[uint32]uint32)
	nodeMap := make(map[uint32][]fsNode)
	collision := false
	for _, node := range nodes {
		hash := pathHash(fullPath(node))
		if _, exists := values[hash]; exists {
			values[hash] = 0x80000000
			nodeMap[hash] = append(nodeMap[hash], node)
			collision = true
			continue
		}
		value := node.getInode().number
		if _, isDir := node.(*fsDir); isDir {
			value |= 0x20000000
		}
		values[hash] = value
		nodeMap[hash] = []fsNode{node}
	}
	hashes := make([]uint32, 0, len(values))
	for hash := range values {
		hashes = append(hashes, hash)
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	table := &flatPathTable{hashes: hashes, values: values}
	if !collision {
		return table, nil
	}
	offset := uint32(0)
	resolver := &collisionResolver{}
	for _, hash := range hashes {
		if table.values[hash] != 0x80000000 {
			continue
		}
		table.values[hash] = 0x80000000 | offset
		entries := make([]pfsDirent, 0, len(nodeMap[hash]))
		for _, node := range nodeMap[hash] {
			direntType := int32(direntFile)
			if _, isDir := node.(*fsDir); isDir {
				direntType = direntDirectory
			}
			dirent := pfsDirent{inodeNumber: node.getInode().number, direntType: direntType, name: fullPath(node)}
			entries = append(entries, dirent)
			offset += uint32(dirent.entSize())
		}
		offset += 0x18
		resolver.entries = append(resolver.entries, entries)
	}
	for _, list := range resolver.entries {
		for _, entry := range list {
			resolver.size += entry.entSize()
		}
		resolver.size += 0x18
	}
	return table, resolver
}

func (f *flatPathTable) size() int64 { return int64(len(f.hashes) * 8) }

func (f *flatPathTable) marshal() []byte {
	out := make([]byte, len(f.hashes)*8)
	for i, hash := range f.hashes {
		binary.LittleEndian.PutUint32(out[i*8:], hash)
		binary.LittleEndian.PutUint32(out[i*8+4:], f.values[hash])
	}
	return out
}

func (c *collisionResolver) marshal() []byte {
	out := make([]byte, c.size)
	offset := 0
	for _, list := range c.entries {
		for _, entry := range list {
			copy(out[offset:], entry.marshal())
			offset += entry.entSize()
		}
		offset += 0x18
	}
	return out
}
