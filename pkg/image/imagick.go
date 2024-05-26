//go:build imagick && !vips && cgo

package image

import (
	"emperror.dev/errors"
	"fmt"
	"github.com/je4/utils/v2/pkg/zLogger"
	"gopkg.in/gographics/imagick.v3/imagick"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type imagickImage struct {
	mw *imagick.MagickWand
}

func NewImageHandler(logger zLogger.ZLogger) ImageHandler {
	imagick.Initialize()
	return &imagickImageHandler{
		logger: logger,
	}
}

type imagickImageHandler struct {
	logger zLogger.ZLogger
}

func (ni *imagickImageHandler) Close() error {
	imagick.Terminate()
	return nil
}

func (ni *imagickImageHandler) Decode(in io.Reader, width, height int64, format string) (any, error) {
	if !slices.Contains(imageFormats, strings.ToUpper(format)) {
		return nil, errors.Errorf("unsupported format '%s'", format)
	}
	res := &imagickImage{
		mw: imagick.NewMagickWand(),
	}
	data, err := io.ReadAll(in)
	if err != nil {
		res.mw.Destroy()
		return nil, errors.Wrap(err, "cannot read image data")
	}
	if err := res.mw.ReadImageBlob(data); err != nil {
		res.mw.Destroy()
		return nil, errors.Wrap(err, "cannot read image")
	}
	res.mw.SetSize(uint(width), uint(height))
	res.mw.SetFormat(strings.ToUpper(format))
	data = nil
	format = res.mw.GetFormat()
	descr, ok := imageFormatDescription[strings.ToUpper(format)]
	if !ok {
		ni.logger.Debug().Msgf("format: %s", res.mw.GetFormat())
	} else {
		ni.logger.Debug().Msgf("format: %s (%s)", res.mw.GetFormat(), descr)
	}
	return res, nil
}

var sharpenRegexp = regexp.MustCompile(`^([0-9.]+)$`)

func (ni *imagickImageHandler) Sharpen(img any, sigmaRadius string) error {
	nImg, ok := img.(*imagickImage)
	if !ok {
		return errors.Errorf("cannot convert %T to *imagickImage", img)
	}
	parts := sharpenRegexp.FindStringSubmatch(sigmaRadius)
	if len(parts) != 2 {
		return errors.Errorf("invalid sigma/radius format '%s'", sigmaRadius)
	}
	sigma, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return errors.Wrapf(err, "invalid sigma '%s'", parts[1])
	}
	if err := nImg.mw.SharpenImage(0, sigma); err != nil {
		return errors.Wrap(err, "cannot sharpen image")
	}
	return nil
}

var sizeRegexp = regexp.MustCompile(`(\d+)x(\d+)`)

func (ni *imagickImageHandler) Resize(imgAny any, size string, resizeType ResizeType) error {
	img, ok := imgAny.(*imagickImage)
	if !ok {
		return errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	cols, rows, err := img.mw.GetSize()
	if err != nil {
		return errors.Wrap(err, "cannot get image size")
	}
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
		rectAspect := cols / rows
		thumbAspect := uint(width) / uint(height)
		newHeight = uint(height)
		newWidth = uint(width)
		if rectAspect > thumbAspect {
			newHeight = rows * uint(width) / cols
		} else {
			newWidth = cols * uint(height) / rows
		}
	case ResizeTypeStretch:
		newWidth = uint(width)
		newHeight = uint(height)
	case ResizeTypeCrop:
		rectAspect := cols / rows
		thumbAspect := uint(width) / uint(height)
		newHeight = uint(height)
		newWidth = uint(width)
		if rectAspect < thumbAspect {
			newHeight = rows * uint(width) / cols
		} else {
			newWidth = cols * uint(height) / rows
		}
	default:
		return errors.Errorf("unsupported resize type %d", resizeType)
	}
	if err := img.mw.ResizeImage(newWidth, newHeight, imagick.FILTER_LANCZOS); err != nil {
		return errors.Wrapf(err, "cannot resize image to %dx%d", newWidth, newHeight)
	}
	if resizeType == ResizeTypeCrop {
		if err := img.mw.CropImage(uint(width), uint(height), int((newWidth-uint(width))/2), int((newHeight-uint(height))/2)); err != nil {
			return errors.Wrapf(err, "cannot crop image to %dx%d", width, height)
		}
	}
	return nil
}

func (ni *imagickImageHandler) Encode(imgAny any, writer io.Writer, format string) (uint64, string, error) {
	img, ok := imgAny.(*imagickImage)
	if !ok {
		return 0, "", errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	var mimetype string

	switch strings.ToLower(format) {
	default:
		mimetype = fmt.Sprintf("image/%s", format)
		if err := img.mw.SetFormat(strings.ToUpper(format)); err != nil {
			return 0, "", errors.Wrapf(err, "cannot set format to %s", format)
		}
	}
	data := img.mw.GetImageBlob()
	size, err := io.Copy(writer, strings.NewReader(string(data)))
	if err != nil {
		return 0, "", errors.Wrap(err, "cannot write image data")
	}
	data = nil
	return uint64(size), mimetype, nil
}

func (ni *imagickImageHandler) GetDimension(imgAny any) (int, int) {
	img, ok := imgAny.(*imagickImage)
	if !ok {
		return 0, 0
	}
	cols, rows, err := img.mw.GetSize()
	if err != nil {
		return 0, 0
	}
	return int(cols), int(rows)
}

func (ni *imagickImageHandler) Release(imgAny any) error {
	img, ok := imgAny.(*imagickImage)
	if !ok {
		return errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	img.mw.Destroy()
	return nil
}

var _ ImageHandler = &imagickImageHandler{}
