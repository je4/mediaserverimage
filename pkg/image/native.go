//go:build (!(imagick && !vips) && !(!imagick && vips)) || !cgo

package image

import (
	"emperror.dev/errors"
	"github.com/je4/utils/v2/pkg/zLogger"
	"github.com/nfnt/resize"
	"github.com/oliamb/cutter"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	_ "golang.org/x/image/vp8"
	_ "golang.org/x/image/vp8l"
	_ "golang.org/x/image/webp"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type nativeImage struct {
	img image.Image
}

func NewImageHandler(logger zLogger.ZLogger) ImageHandler {
	return &nativeImageHandler{
		logger: logger,
	}
}

type nativeImageHandler struct {
	logger zLogger.ZLogger
}

func (ni *nativeImageHandler) Decode(in io.Reader, _, _ int64, _ string) (any, error) {
	img, format, err := image.Decode(in)
	ni.logger.Debug().Msgf("format: %s", format)
	res := &nativeImage{
		img: img,
	}
	return res, errors.Wrap(err, "cannot decode image")
}

func (*nativeImageHandler) Sharpen(_ any, _ string) error {
	return errors.New("not implemented")
}

var sizeRegexp = regexp.MustCompile(`(\d+)x(\d+)`)

func (ni *nativeImageHandler) Resize(imgAny any, size string, resizeType ResizeType) error {
	nImg, ok := imgAny.(*nativeImage)
	if !ok {
		return errors.Errorf("cannot convert %T to *nativeImage", imgAny)
	}
	img := nImg.img
	rect := img.Bounds()
	sizeParts := sizeRegexp.FindStringSubmatch(size)
	if len(sizeParts) != 3 {
		return errors.Errorf("invalid size format '%s'", size)
	}
	width, err := strconv.Atoi(sizeParts[1])
	if err != nil {
		return errors.Wrapf(err, "invalid width '%s'", sizeParts[1])
	}
	height, err := strconv.Atoi(sizeParts[2])
	if err != nil {
		return errors.Wrapf(err, "invalid height '%s'", sizeParts[2])
	}

	var newWidth, newHeight uint
	switch resizeType {
	case ResizeTypeAspect:
		rectAspect := rect.Dx() / rect.Dy()
		thumbAspect := int(width) / int(height)
		newHeight = uint(height)
		newWidth = uint(width)
		if rectAspect > thumbAspect {
			newHeight = uint(rect.Dy() * width / rect.Dx())
		} else {
			newWidth = uint(rect.Dx() * height / rect.Dy())
		}
	case ResizeTypeStretch:
		newWidth = uint(width)
		newHeight = uint(height)
	case ResizeTypeCrop:
		rectAspect := rect.Dx() / rect.Dy()
		thumbAspect := int(width) / int(height)
		newHeight = uint(height)
		newWidth = uint(width)
		if rectAspect < thumbAspect {
			newHeight = uint(rect.Dy() * width / rect.Dx())
		} else {
			newWidth = uint(rect.Dx() * height / rect.Dy())
		}
	default:
		return errors.Errorf("unsupported resize type %d", resizeType)
	}
	nImg.img = resize.Resize(newWidth, newHeight, img, resize.Lanczos3)
	i, ok := nImg.img.(image.Image)
	if !ok {
		return errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	if resizeType == ResizeTypeCrop {
		nImg.img, err = cutter.Crop(i, cutter.Config{
			Width:  int(width),
			Height: int(height),
			Mode:   cutter.Centered,
		})
		if err != nil {
			return errors.Wrapf(err, "cannot crop image(%dx%d) to %dx%d", newWidth, newHeight, width, height)
		}
	}
	return nil
}

func (ni *nativeImageHandler) Encode(imgAny any, writer io.Writer, format string) (uint64, string, error) {
	nImg, ok := imgAny.(*nativeImage)
	if !ok {
		return 0, "", errors.Errorf("cannot convert %T to *nativeImage", imgAny)
	}
	img := nImg.img
	var mimetype string
	out := NewCounterWriter(writer)
	var err error
	switch strings.ToLower(format) {
	case "jpeg":
		err = jpeg.Encode(out, img, nil)
		mimetype = "image/jpeg"
	case "png":
		err = png.Encode(out, img)
		mimetype = "image/png"
	case "bmp":
		err = bmp.Encode(out, img)
		mimetype = "image/bmp"
	case "tiff":
		err = tiff.Encode(out, img, nil)
		mimetype = "image/tiff"
	default:
		return 0, "", errors.Errorf("unsupported format %s", format)
	}
	if err != nil {
		return 0, "", errors.Wrapf(err, "cannot encode image to %s", strings.ToLower(format))
	}
	return out.Bytes(), mimetype, nil
}

func (ni *nativeImageHandler) GetDimension(imgAny any) (int, int) {
	nImg, ok := imgAny.(*nativeImage)
	if !ok {
		return 0, 0
	}
	img := nImg.img
	return img.Bounds().Dx(), img.Bounds().Dy()
}

func (ni *nativeImageHandler) Release(imgAny any) error {
	nImg, ok := imgAny.(*nativeImage)
	if !ok {
		return errors.Errorf("cannot convert %T to *nativeImage", imgAny)
	}
	nImg.img = nil
	return nil
}

func (ni *nativeImageHandler) Close() error {
	return nil
}

var _ ImageHandler = &nativeImageHandler{}
