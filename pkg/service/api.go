package service

import (
	"context"
	"fmt"
	"github.com/je4/filesystem/v3/pkg/writefs"
	"github.com/je4/utils/v2/pkg/zLogger"
	generic "go.ub.unibas.ch/cloud/genericproto/v2/pkg/generic/proto"
	"go.ub.unibas.ch/mediaserver/mediaserveraction/v2/pkg/actionController"
	actionParams "go.ub.unibas.ch/mediaserver/mediaserverhelper/v2/pkg/actionParams"
	"go.ub.unibas.ch/mediaserver/mediaserverimage/v2/pkg/image"
	mediaserverproto "go.ub.unibas.ch/mediaserver/mediaserverproto/v2/pkg/mediaserver/proto"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/vp8"
	_ "golang.org/x/image/vp8l"
	_ "golang.org/x/image/webp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var Type = "image"
var Params = map[string][]string{
	"resize":  {"size", "format", "stretch", "crop", "aspect", "sharpen", "blur", "tile", "compress", "quality"},
	"convert": {"format", "tile", "compress", "quality"},
}

func NewActionService(adClients map[string]mediaserverproto.ActionDispatcherClient, instance string, domains []string, concurrency, queueSize uint32, refreshErrorTimeout time.Duration, vfs fs.FS, dbs map[string]mediaserverproto.DatabaseClient, logger zLogger.ZLogger) (*imageAction, error) {
	_logger := logger.With().Str("rpcService", "imageAction").Logger()
	return &imageAction{
		actionDispatcherClients: adClients,
		done:                    make(chan bool),
		instance:                instance,
		domains:                 domains,
		refreshErrorTimeout:     refreshErrorTimeout,
		vFS:                     vfs,
		dbs:                     dbs,
		logger:                  &_logger,
		image:                   image.NewImageHandler(logger),
		concurrency:             concurrency,
		queueSize:               queueSize,
	}, nil
}

type imageAction struct {
	mediaserverproto.UnimplementedActionServer
	actionDispatcherClients map[string]mediaserverproto.ActionDispatcherClient
	logger                  zLogger.ZLogger
	done                    chan bool
	refreshErrorTimeout     time.Duration
	vFS                     fs.FS
	dbs                     map[string]mediaserverproto.DatabaseClient
	image                   image.ImageHandler
	concurrency             uint32
	queueSize               uint32
	instance                string
	domains                 []string
}

func (ia *imageAction) Start() error {
	actionParams := map[string]*mediaserverproto.StringListMap{Type: {Values: map[string]*generic.StringList{}}}
	for action, params := range Params {
		actionParams[Type].Values[action] = &generic.StringList{
			Values: params,
		}
	}
	go func() {
		for {
			waitDuration := ia.refreshErrorTimeout
			for _, adClient := range ia.actionDispatcherClients {
				if resp, err := adClient.AddController(context.Background(), &mediaserverproto.ActionDispatcherParam{
					Actions:     actionParams,
					Domains:     ia.domains,
					Name:        ia.instance,
					Concurrency: ia.concurrency,
					QueueSize:   ia.queueSize,
				}); err != nil {
					ia.logger.Error().Err(err).Msg("cannot add controller")
				} else {
					if resp.GetResponse().GetStatus() != generic.ResultStatus_OK {
						ia.logger.Error().Err(err).Msgf("cannot add controller: %s", resp.GetResponse().GetMessage())
					} else {
						waitDuration = time.Duration(resp.GetNextCallWait()) * time.Second
						ia.logger.Info().Msgf("controller %s added", ia.instance)
					}
				}
			}
			select {
			case <-time.After(waitDuration):
				continue
			case <-ia.done:
				return
			}
		}
	}()
	return nil
}

func (ia *imageAction) GracefulStop() {
	if err := ia.image.Close(); err != nil {
		ia.logger.Error().Err(err).Msg("cannot close image handler")
	}
	actionParams := map[string]*mediaserverproto.StringListMap{Type: {Values: map[string]*generic.StringList{}}}
	for action, params := range Params {
		actionParams[Type].Values[action] = &generic.StringList{
			Values: params,
		}
	}
	for _, adClient := range ia.actionDispatcherClients {
		if resp, err := adClient.RemoveController(context.Background(), &mediaserverproto.ActionDispatcherParam{
			Actions:     actionParams,
			Name:        ia.instance,
			Concurrency: ia.concurrency,
		}); err != nil {
			ia.logger.Error().Err(err).Msg("cannot remove controller")
		} else {
			if resp.GetStatus() != generic.ResultStatus_OK {
				ia.logger.Error().Err(err).Msgf("cannot remove controller: %s", resp.GetMessage())
			} else {
				ia.logger.Info().Msgf("controller %s removed", ia.instance)
			}

		}
	}
	ia.done <- true
}

func (ia *imageAction) Ping(context.Context, *emptypb.Empty) (*generic.DefaultResponse, error) {
	return &generic.DefaultResponse{
		Status:  generic.ResultStatus_OK,
		Message: "pong",
		Data:    nil,
	}, nil
}
func (ia *imageAction) GetParams(ctx context.Context, param *mediaserverproto.ParamsParam) (*generic.StringList, error) {
	params, ok := Params[param.GetAction()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "action %s::%s not found", param.GetType(), param.GetAction())
	}
	return &generic.StringList{
		Values: params,
	}, nil
}

var isUrlRegexp = regexp.MustCompile(`^[a-z]+://`)

func (ia *imageAction) loadImage(imagePath string, width, height int64, imgType string) (any, error) {
	fp, err := ia.vFS.Open(imagePath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cannot open %s: %v", imagePath, err)
	}
	defer fp.Close()
	img, err := ia.image.Decode(fp, width, height, imgType)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot decode %s: %v", imagePath, err)
	}
	return img, nil
}

func (ia *imageAction) storeImage(img any, action string, item *mediaserverproto.Item, itemCache *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionParams.ActionParams, format, compress string, quality int, tile string) (*mediaserverproto.Cache, error) {
	itemIdentifier := item.GetIdentifier()
	cacheName := actionController.CreateCacheName(itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), action, params.String(), format)
	targetPath := fmt.Sprintf(
		"%s/%s/%s",
		storage.GetFilebase(),
		storage.GetDatadir(),
		cacheName)
	target, err := writefs.Create(ia.vFS, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cannot open %s: %v", targetPath, err)
	}
	defer func() {
		if err := target.Close(); err != nil {
			ia.logger.Error().Err(err).Msgf("cannot close %s/%s", ia.vFS, targetPath)
		} else {
			ia.logger.Info().Msgf("stored %s/%s", ia.vFS, targetPath)
		}
	}()
	filesize, mime, err := ia.image.Encode(img, target, format, compress, quality, tile)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot encode %s: %v", targetPath, err)
	}
	width, height := ia.image.GetDimension(img)
	resp := &mediaserverproto.Cache{
		Identifier: &mediaserverproto.ItemIdentifier{
			Collection: itemIdentifier.GetCollection(),
			Signature:  itemIdentifier.GetSignature(),
		},
		Metadata: &mediaserverproto.CacheMetadata{
			Action:   action,
			Params:   params.String(),
			Width:    int64(width),
			Height:   int64(height),
			Duration: 0,
			Size:     int64(filesize),
			MimeType: mime,
			Path:     fmt.Sprintf("%s/%s", storage.GetDatadir(), cacheName),
			Storage:  storage,
		},
	}
	return resp, nil

}

func (ia *imageAction) resize(item *mediaserverproto.Item, itemCache *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionParams.ActionParams) (*mediaserverproto.Cache, error) {
	var err error
	itemIdentifier := item.GetIdentifier()

	cacheItemMetadata := itemCache.GetMetadata()
	size := params.Get("size")
	if size == "" {
		return nil, status.Errorf(codes.InvalidArgument, "no size defined")
	}
	format := params.Get("format")
	if format == "" {
		format = "jpeg"
	}
	qualityStr := params.Get("quality")
	quality := 100
	if qualityStr != "" {
		quality, err = strconv.Atoi(qualityStr)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid quality %s", qualityStr)
		}
		if quality < 0 || quality > 100 {
			return nil, status.Errorf(codes.InvalidArgument, "quality %d not >= 0 and <= 100", quality)
		}
	}
	compress := params.Get("compress")
	tile := params.Get("tile")
	var resizeType = image.ResizeTypeAspect
	if params.Has("stretch") {
		resizeType = image.ResizeTypeStretch
	} else if params.Has("crop") {
		resizeType = image.ResizeTypeCrop
	}
	ia.logger.Info().Msgf("action %s/%s/%s/%s", itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), "resize", params.String())
	itemImagePath := cacheItemMetadata.GetPath()
	if !isUrlRegexp.MatchString(itemImagePath) {
		itemImagePath = fmt.Sprintf("%s/%s", storage.GetFilebase(), strings.TrimPrefix(itemImagePath, "/"))
	}
	img, err := ia.loadImage(itemImagePath, cacheItemMetadata.GetWidth(), cacheItemMetadata.GetHeight(), item.GetMetadata().GetSubtype())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot decode %s: %v", itemImagePath, err)
	}
	defer ia.image.Release(img)
	if err := ia.image.Resize(img, size, resizeType); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot resize %s: %v", itemImagePath, err)
	}

	if params.Has("blur") {
		if err := ia.image.Blur(img, params.Get("blur")); err != nil {
			return nil, status.Errorf(codes.Internal, "cannot blur %s: %v", itemImagePath, err)
		}
	}

	if params.Has("sharpen") {
		if err := ia.image.Sharpen(img, params.Get("sharpen")); err != nil {
			return nil, status.Errorf(codes.Internal, "cannot sharpen %s: %v", itemImagePath, err)
		}
	}

	return ia.storeImage(img, "resize", item, itemCache, storage, params, format, compress, quality, tile)
}

func (ia *imageAction) convert(item *mediaserverproto.Item, itemCache *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionParams.ActionParams) (*mediaserverproto.Cache, error) {
	var err error
	itemIdentifier := item.GetIdentifier()
	cacheItemMetadata := itemCache.GetMetadata()
	format := params.Get("format")
	if format == "" {
		format = "jpeg"
	}
	qualityStr := params.Get("quality")
	quality := 100
	if qualityStr != "" {
		quality, err = strconv.Atoi(qualityStr)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid quality %s", qualityStr)
		}
		if quality < 0 || quality > 100 {
			return nil, status.Errorf(codes.InvalidArgument, "quality %d not >= 0 and <= 100", quality)
		}
	}
	compress := params.Get("compress")
	tile := params.Get("tile")
	ia.logger.Info().Msgf("action %s/%s/%s/%s", itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), "convert", params.String())
	itemImagePath := cacheItemMetadata.GetPath()
	if !isUrlRegexp.MatchString(itemImagePath) {
		itemImagePath = fmt.Sprintf("%s/%s", storage.GetFilebase(), strings.TrimPrefix(itemImagePath, "/"))
	}
	img, err := ia.loadImage(itemImagePath, cacheItemMetadata.GetWidth(), cacheItemMetadata.GetHeight(), item.GetMetadata().GetSubtype())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot decode %s: %v", itemImagePath, err)
	}
	defer ia.image.Release(img)
	return ia.storeImage(img, "convert", item, itemCache, storage, params, format, compress, quality, tile)
}

func (ia *imageAction) Action(ctx context.Context, ap *mediaserverproto.ActionParam) (*mediaserverproto.Cache, error) {
	domains := metadata.ValueFromIncomingContext(ctx, "domain")
	var domain string
	if len(domains) > 0 {
		domain = domains[0]
	}
	item := ap.GetItem()
	if item == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no item defined")
	}
	itemIdentifier := item.GetIdentifier()
	storage := ap.GetStorage()
	if storage == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no storage defined")
	}
	db, ok := ia.dbs[domain]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "no database for domain %s", domain)
	}
	cacheItem, err := db.GetCache(context.Background(), &mediaserverproto.CacheRequest{
		Identifier: itemIdentifier,
		Action:     "item",
		Params:     "",
	})
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cannot get cache %s/%s/item: %v", itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), err)
	}
	action := ap.GetAction()
	switch strings.ToLower(action) {
	case "resize":
		return ia.resize(item, cacheItem, storage, ap.GetParams())
	case "convert":
		return ia.convert(item, cacheItem, storage, ap.GetParams())
	default:
		return nil, status.Errorf(codes.InvalidArgument, "no action defined")

	}
}
