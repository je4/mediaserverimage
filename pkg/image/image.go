package image

import "io"

type Image interface {
	Decode(in io.Reader) (any, error)
	Resize(img any, size string) error
	Encode(img any, out io.Writer, format string) (uint64, string, error)
	Release(img any) error
	GetDimension(img any) (width int, height int)
}
