//go:build (!(imagick && !vips) && !(!imagick && vips)) || !cgo

package image

import (
	"emperror.dev/errors"
	"github.com/je4/utils/v2/pkg/zLogger"
	"github.com/nfnt/resize"
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

func NewImage(logger zLogger.ZLogger) Image {
	return &nativeImage{
		logger: logger,
	}
}

type nativeImage struct {
	logger zLogger.ZLogger
}

func (ni *nativeImage) Decode(in io.Reader) (any, error) {
	img, format, err := image.Decode(in)
	ni.logger.Debug().Msgf("format: %s", format)
	return img, errors.Wrap(err, "cannot decode image")
}

var sizeRegexp = regexp.MustCompile(`(\d+)x(\d+)`)

func (ni *nativeImage) Resize(imgAny any, size string) error {
	img, ok := imgAny.(image.Image)
	if !ok {
		return errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
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

	rectAspect := rect.Dx() / rect.Dy()
	thumbAspect := int(width) / int(height)
	newHeight := uint(height)
	newWidth := uint(width)
	if rectAspect > thumbAspect {
		newHeight = uint(rect.Dy() * width / rect.Dx())
	} else {
		newWidth = uint(rect.Dx() * height / rect.Dy())
	}

	img = resize.Resize(newWidth, newHeight, img, resize.Lanczos3)
	return nil
}

func (ni *nativeImage) Encode(imgAny any, writer io.Writer, format string) (uint64, string, error) {
	img, ok := imgAny.(image.Image)
	if !ok {
		return 0, "", errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
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

func (ni *nativeImage) GetDimension(img any) (int, int) {
	i, ok := img.(image.Image)
	if !ok {
		return 0, 0
	}
	return i.Bounds().Dx(), i.Bounds().Dy()
}

func (ni *nativeImage) Release(imgAny any) error {
	img, ok := imgAny.(image.Image)
	if !ok {
		return errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	_ = img
	img = nil
	return nil
}

var _ Image = &nativeImage{}
