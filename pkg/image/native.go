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

func (ni *nativeImage) Open(in io.Reader) (any, error) {
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

func (ni *nativeImage) Write(imgAny any, out io.Writer, format string) error {
	img, ok := imgAny.(image.Image)
	if !ok {
		return errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	switch strings.ToLower(format) {
	case "jpeg":
		return errors.Wrap(jpeg.Encode(out, img, nil), "cannot encode image as jpeg")
	case "png":
		return errors.Wrap(png.Encode(out, img), "cannot encode image as png")
	case "bmp":
		return errors.Wrap(bmp.Encode(out, img), "cannot encode image as bmp")
	case "tiff":
		return errors.Wrap(tiff.Encode(out, img, nil), "cannot encode image as tiff")
	case "":
		return errors.Wrap(png.Encode(out, img), "cannot encode image as png")
	default:
		return errors.Errorf("unsupported format %s", format)
	}
}

func (ni *nativeImage) Close(imgAny any) error {
	img, ok := imgAny.(image.Image)
	if !ok {
		return errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	_ = img
	img = nil
	return nil
}

var _ Image = &nativeImage{}
