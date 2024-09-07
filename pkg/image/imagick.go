//go:build imagick && !vips && cgo

package image

import (
	"emperror.dev/errors"
	"fmt"
	"github.com/je4/utils/v2/pkg/zLogger"
	"gopkg.in/gographics/imagick.v3/imagick"
	"io"
	"math"
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
	mw := imagick.NewMagickWand()
	defer mw.Destroy()
	imageFormats = mw.QueryFormats("*")
	_logger := logger.With().Str("class", "imagickImageHandler").Logger()
	_logger.Debug().Msgf("supported formats: %s", strings.Join(imageFormats, ", "))
	return &imagickImageHandler{
		logger: zLogger.ZLogger(&_logger),
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

func (ni *imagickImageHandler) Sharpen(img any, sigma string) error {
	nImg, ok := img.(*imagickImage)
	if !ok {
		return errors.Errorf("cannot convert %T to *imagickImage", img)
	}
	sig, err := strconv.ParseFloat(sigma, 64)
	if err != nil {
		return errors.Wrapf(err, "invalid sigma '%s'", sigma)
	}
	if err := nImg.mw.SharpenImage(0, sig); err != nil {
		return errors.Wrap(err, "cannot sharpen image")
	}
	return nil
}

func (ni *imagickImageHandler) Blur(img any, sigma string) error {
	nImg, ok := img.(*imagickImage)
	if !ok {
		return errors.Errorf("cannot convert %T to *imagickImage", img)
	}
	sig, err := strconv.ParseFloat(sigma, 64)
	if err != nil {
		return errors.Wrapf(err, "invalid sigma '%s'", sigma)
	}
	if err := nImg.mw.BlurImage(0, sig); err != nil {
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
	if cols == 0 || rows == 0 {
		return errors.New("image size is 0")
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

	if width == 0 && height == 0 {
		return errors.New("both width and height are 0")
	}
	var newWidth, newHeight uint
	switch resizeType {
	case ResizeTypeAspect:
		if width == 0 {
			width = math.MaxInt
		}
		if height == 0 {
			height = math.MaxInt
		}
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
		if width == 0 || height == 0 {
			return errors.New("both width and height must be set for stretch resize")
		}
		newWidth = uint(width)
		newHeight = uint(height)
	case ResizeTypeCrop:
		if width == 0 || height == 0 {
			return errors.New("both width and height must be set for crop resize")
		}
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

var tileRegexp = regexp.MustCompile(`^(\d+)x(\d+)$`)

func (ni *imagickImageHandler) Encode(imgAny any, writer io.Writer, format, compress string, quality int, tile string) (uint64, string, error) {
	img, ok := imgAny.(*imagickImage)
	if !ok {
		return 0, "", errors.Errorf("cannot convert %T to image.Image", imgAny)
	}
	var mimetype string

	if compress != "" {
		compression, ok := compresionNames[compress]
		if !ok {
			return 0, "", errors.Errorf("unsupported compression '%s'", compress)
		}
		if err := img.mw.SetCompression(compression); err != nil {
			return 0, "", errors.Wrapf(err, "cannot set compression to %s", compression)
		}
	}
	if quality >= 0 && quality <= 100 {
		if err := img.mw.SetCompressionQuality(uint(quality)); err != nil {
			return 0, "", errors.Wrap(err, "cannot set compression quality to 85")
		}
	}
	switch strings.ToLower(format) {
	case "jp2":
		mimetype = "image/jp2"
		if tile != "" {
			parts := tileRegexp.FindStringSubmatch(tile)
			if parts == nil {
				return 0, "", errors.Errorf("invalid tile format '%s'", tile)
			}
			if err := img.mw.SetOption("jp2:tilewidth", parts[1]); err != nil {
				return 0, "", errors.Wrap(err, "cannot set tile width option")
			}
			if err := img.mw.SetOption("jp2:tileheight", parts[2]); err != nil {
				return 0, "", errors.Wrap(err, "cannot set tile height option")
			}
			if err := img.mw.SetExtract(tile); err != nil {
				return 0, "", errors.Wrap(err, "cannot set tile option")
			}
		}
		if err := img.mw.SetFormat(strings.ToUpper(format)); err != nil {
			return 0, "", errors.Wrapf(err, "cannot set format to %s", format)
		}
	case "ptif":
		mimetype = fmt.Sprintf("image/tiff", format)
		if err := img.mw.SetFormat(strings.ToUpper("ptif")); err != nil {
			return 0, "", errors.Wrapf(err, "cannot set format to %s", format)
		}
		if tile != "" {
			if err := img.mw.SetOption("tiff:tile-geometry", tile); err != nil {
				return 0, "", errors.Wrapf(err, "cannot set tile geometry '%s'", tile)
			}
		}
	default:
		mimetype = fmt.Sprintf("image/%s", format)
		if err := img.mw.SetFormat(strings.ToUpper(format)); err != nil {
			return 0, "", errors.Wrapf(err, "cannot set format to %s", format)
		}
	}
	data, err := img.mw.GetImageBlob()
	if err != nil {
		return 0, "", errors.Wrap(err, "cannot get image data")
	}
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
