package seekabletar

import (
	"io"
)

type readReaderAt interface {
	io.Reader
	io.ReaderAt
}

type readCounterIface interface {
	io.Reader
	Count() int64
}

type readCounter struct {
	io.Reader
	off int64
}

func (cr *readCounter) Read(p []byte) (n int, err error) {
	n, err = cr.Reader.Read(p)
	cr.off += int64(n)
	return
}

func (cr *readCounter) Count() int64 {
	return cr.off
}

type readSeekCounter struct {
	io.ReadSeeker
	off int64
}

func (cr *readSeekCounter) Read(p []byte) (n int, err error) {
	n, err = cr.ReadSeeker.Read(p)
	cr.off += int64(n)
	return
}

func (cr *readSeekCounter) Seek(offset int64, whence int) (abs int64, err error) {
	abs, err = cr.ReadSeeker.Seek(offset, whence)
	cr.off = abs
	return
}

func (cr *readSeekCounter) Count() int64 {
	return cr.off
}
