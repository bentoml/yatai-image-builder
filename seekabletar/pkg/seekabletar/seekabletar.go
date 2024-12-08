package seekabletar

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/errors"
	"github.com/tidwall/btree"
)

type TarTOCEntry struct {
	Path   string      `json:"path"`
	Target string      `json:"target"`
	Off    uint64      `json:"off"` //nolint:tagliatelle
	Size   uint64      `json:"size"`
	Type   string      `json:"type"`
	Mode   os.FileMode `json:"mode"`
	Nlink  uint32      `json:"nlink"`
	Uid    int         `json:"uid"`    //nolint:stylecheck
	Gid    int         `json:"gid"`    //nolint:stylecheck
	MTime  time.Time   `json:"m_time"` // nolint:tagliatelle
	ATime  time.Time   `json:"a_time"` // nolint:tagliatelle
	CTime  time.Time   `json:"c_time"` // nolint:tagliatelle
}

func (t *TarTOCEntry) FuseMode() uint32 {
	mode := t.Mode

	var fuseMode uint32

	if mode.IsDir() { //nolint:gocritic
		fuseMode |= syscall.S_IFDIR
	} else if mode&os.ModeSymlink != 0 {
		fuseMode |= syscall.S_IFLNK
	} else if mode&os.ModeDevice != 0 {
		if mode&os.ModeCharDevice != 0 {
			fuseMode |= syscall.S_IFCHR
		} else {
			fuseMode |= syscall.S_IFBLK
		}
	} else if mode&os.ModeNamedPipe != 0 {
		fuseMode |= syscall.S_IFIFO
	} else if mode&os.ModeSocket != 0 {
		fuseMode |= syscall.S_IFSOCK
	} else {
		fuseMode |= syscall.S_IFREG
	}

	fuseMode |= uint32(mode.Perm())

	return fuseMode
}

type TarTOC []*TarTOCEntry

type TarTOCIndex struct {
	db *btree.BTree
}

func NewTOCIndex(toc *TarTOC) *TarTOCIndex {
	index := btree.New(func(a, b any) bool {
		return a.(*TarTOCEntry).Path < b.(*TarTOCEntry).Path
	})

	for _, entry := range *toc {
		index.Set(entry)
	}

	return &TarTOCIndex{db: index}
}

func (ti *TarTOCIndex) Get(path string) *TarTOCEntry {
	item := ti.db.Get(&TarTOCEntry{Path: path})
	if item == nil {
		return nil
	}
	return item.(*TarTOCEntry)
}

func (ti *TarTOCIndex) ListDirectory(path string) []fuse.DirEntry {
	var entries []fuse.DirEntry

	// Append '/' if not present at the end of the path
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}

	// Append null character to the path -- if we don't do this we could miss some child nodes.
	// It works because \x00 is lower lexographically than any other character
	pivot := &TarTOCEntry{Path: path + "\x00"}
	pathLen := len(path)

	ti.db.Ascend(pivot, func(a any) bool {
		node := a.(*TarTOCEntry)
		nodePath := node.Path

		// Check if this node path starts with 'path' (meaning it is a child --> continue)
		if len(nodePath) < pathLen || nodePath[:pathLen] != path {
			return true
		}

		// Check if there are any "/" left after removing the prefix
		for i := pathLen; i < len(nodePath); i++ {
			if nodePath[i] == '/' {
				if i == pathLen || nodePath[i-1] != '/' {
					// This node is not an immediate child, continue on
					return true
				}
			}
		}
		relativePath := nodePath[pathLen:]
		if relativePath != "" {
			entries = append(entries, fuse.DirEntry{
				Mode: node.FuseMode(),
				Name: relativePath,
			})
		}

		return true
	})

	return entries
}

func MarshalTOC(toc *TarTOC) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(toc); err != nil {
		return nil, errors.Wrap(err, "failed to marshal toc")
	}
	return buf.Bytes(), nil
}

func UnmarshalTOC(tocBytes []byte) (*TarTOC, error) {
	var toc TarTOC
	err := gob.NewDecoder(bytes.NewReader(tocBytes)).Decode(&toc)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal toc")
	}

	return &toc, nil
}

func init() {
	gob.Register(&TarTOCEntry{})
}

func getPath(name string) string {
	cleanName := path.Clean(name)
	if cleanName == "." {
		return "/"
	}
	return filepath.Join("/", cleanName)
}

func CalculateTOCFromTar(reader io.Reader) (*TarTOC, error) {
	toc := make(TarTOC, 0)

	linkCount := make(map[string]uint32)
	pathToIndex := make(map[string]int)

	toc = append(toc, &TarTOCEntry{Path: "/", Target: "", Size: 0, Type: string(tar.TypeDir), Mode: 0755, Uid: 0, Gid: 0, MTime: time.Now(), ATime: time.Now(), CTime: time.Now()})
	pathToIndex["/"] = 0

	ra, isReaderAt := reader.(readReaderAt)
	if !isReaderAt {
		buf, err := io.ReadAll(reader)
		if err != nil {
			return nil, errors.Wrap(err, "cannot read tar")
		}
		ra = bytes.NewReader(buf)
	}

	var cr readCounterIface
	if rs, isReadSeeker := ra.(io.ReadSeeker); isReadSeeker {
		cr = &readSeekCounter{ReadSeeker: rs}
	} else {
		cr = &readCounter{Reader: ra}
	}

	tr := tar.NewReader(cr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.Wrap(err, "failed to get next tar header")
		}

		if header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}

		path := getPath(header.Name)
		if path == "/" {
			continue
		}

		fileInfo := header.FileInfo()

		entry := TarTOCEntry{
			Path:   path,
			Target: header.Linkname,
			Size:   uint64(header.Size), //nolint:gosec
			Type:   string(header.Typeflag),
			Mode:   fileInfo.Mode(),
			Nlink:  1,
			Uid:    header.Uid,
			Gid:    header.Gid,
			Off:    uint64(cr.Count()), //nolint:gosec
			MTime:  header.ModTime,
			ATime:  header.AccessTime,
			CTime:  header.ChangeTime,
		}

		if header.Typeflag == tar.TypeDir {
			entry.Nlink = 2
		} else if header.Typeflag == tar.TypeLink {
			linkCount[header.Linkname]++
			linkCount[path]++
		}

		toc = append(toc, &entry)
		pathToIndex[path] = len(toc) - 1
	}
	for path, count := range linkCount {
		if idx, exists := pathToIndex[path]; exists {
			toc[idx].Nlink = count
			targetPath := toc[idx].Target
			if targetPath != "" {
				if targetIdx, exists := pathToIndex[targetPath]; exists {
					toc[targetIdx].Nlink = count
				}
			}
		}
	}
	return &toc, nil
}

const (
	// MagicFlag is the magic number to identify seekabletar files
	MagicFlag uint32 = 0x53454554 // "SEET" in ASCII
)

// [tar bytes][toc bytes][toc size][magic flag], where:
// - [toc size] is a 4-byte little-endian integer
// - [magic flag] is a 4-byte magic number
func ConvertToSeekableTar(tarReader io.ReadSeeker) (io.ReadCloser, error) {
	toc, err := CalculateTOCFromTar(tarReader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calculate toc")
	}

	tocBytes, err := MarshalTOC(toc)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal toc")
	}

	tocSize := len(tocBytes)
	if tocSize > 0xFFFFFFFF {
		return nil, errors.New("toc size is too large")
	}

	tailBytes := make([]byte, tocSize+8) // +8 for tocSize and magic flag
	copy(tailBytes, tocBytes)
	binary.LittleEndian.PutUint32(tailBytes[len(tocBytes):], uint32(tocSize))
	binary.LittleEndian.PutUint32(tailBytes[len(tocBytes)+4:], MagicFlag)

	tailReader := bytes.NewReader(tailBytes)
	_, err = tarReader.Seek(0, 0)
	if err != nil {
		return nil, errors.Wrap(err, "failed to seek tar reader")
	}
	return io.NopCloser(io.MultiReader(tarReader, tailReader)), nil
}

func IsSeekableTar(readerAt io.ReaderAt, size int64) (bool, error) {
	// Read magic flag first
	magicBytes := make([]byte, 4)
	_, err := readerAt.ReadAt(magicBytes, size-4)
	if err != nil {
		return false, errors.Wrap(err, "failed to read magic flag")
	}

	magic := binary.LittleEndian.Uint32(magicBytes)
	if magic != MagicFlag {
		return false, nil
	}

	return true, nil
}

func GetTOCFromReaderAt(readerAt io.ReaderAt, size int64) (*TarTOC, error) {
	isSeekable, err := IsSeekableTar(readerAt, size)
	if err != nil {
		return nil, errors.Wrap(err, "failed to check if seekable tar")
	}

	if !isSeekable {
		return nil, errors.New("file is not a seekable tar")
	}

	// Read TOC size
	tocBytes := make([]byte, 4)
	_, err = readerAt.ReadAt(tocBytes, size-8)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read toc bytes")
	}
	tocSize := binary.LittleEndian.Uint32(tocBytes)
	if tocSize == 0 {
		return nil, errors.New("toc size is 0")
	}
	tocBytes = make([]byte, tocSize)
	_, err = readerAt.ReadAt(tocBytes, size-int64(tocSize)-8)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read toc bytes")
	}
	return UnmarshalTOC(tocBytes)
}

func GetTOCFromSeekableTar(file *os.File) (*TarTOC, error) {
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get file info")
	}

	size := fileInfo.Size()
	if size < 8 {
		return nil, errors.New("file is too small")
	}

	return GetTOCFromReaderAt(file, size)
}
