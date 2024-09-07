package image

import "io"

type ResizeType int

const (
	ResizeTypeAspect ResizeType = iota
	ResizeTypeStretch
	ResizeTypeCrop
)

type ImageHandler interface {
	Decode(in io.Reader, width, height int64, format string) (any, error)
	Resize(img any, size string, resizeType ResizeType) error
	Encode(img any, out io.Writer, format, compress string, quality int, tile string) (uint64, string, error)
	Sharpen(img any, sigmaRadius string) error
	Blur(img any, sigma string) error
	Release(img any) error
	GetDimension(img any) (width int, height int)
	Close() error
}
