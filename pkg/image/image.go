package image

import "io"

type Image interface {
	Open(in io.Reader) (any, error)
	Resize(img any, size string) error
	Write(img any, out io.Writer, format string) error
	Close(img any) error
}
