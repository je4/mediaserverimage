package service

import (
	"context"
	"fmt"
	"github.com/je4/filesystem/v3/pkg/writefs"
	generic "github.com/je4/genericproto/v2/pkg/generic/proto"
	"github.com/je4/mediaserveraction/v2/pkg/actionCache"
	"github.com/je4/mediaserveraction/v2/pkg/actionController"
	"github.com/je4/mediaserverimage/v2/pkg/image"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/vp8"
	_ "golang.org/x/image/vp8l"
	_ "golang.org/x/image/webp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"io/fs"
	"regexp"
	"strings"
	"time"
)

var Type = "image"
var Params = map[string][]string{
	"resize":  {"size", "format", "stretch", "crop", "aspect", "sharpen", "blur", "tile"},
	"convert": {"format", "tile"},
}

func NewActionService(adClient mediaserverproto.ActionDispatcherClient, host string, port uint32, concurrency, queueSize uint32, refreshErrorTimeout time.Duration, vfs fs.FS, db mediaserverproto.DatabaseClient, logger zLogger.ZLogger) (*imageAction, error) {
	_logger := logger.With().Str("rpcService", "imageAction").Logger()
	return &imageAction{
		actionDispatcherClient: adClient,
		done:                   make(chan bool),
		host:                   host,
		port:                   port,
		refreshErrorTimeout:    refreshErrorTimeout,
		vFS:                    vfs,
		db:                     db,
		logger:                 &_logger,
		image:                  image.NewImageHandler(logger),
		concurrency:            concurrency,
		queueSize:              queueSize,
	}, nil
}

type imageAction struct {
	mediaserverproto.UnimplementedActionServer
	actionDispatcherClient mediaserverproto.ActionDispatcherClient
	logger                 zLogger.ZLogger
	done                   chan bool
	host                   string
	port                   uint32
	refreshErrorTimeout    time.Duration
	vFS                    fs.FS
	db                     mediaserverproto.DatabaseClient
	image                  image.ImageHandler
	concurrency            uint32
	queueSize              uint32
}

func (ia *imageAction) Start() error {
	actionParams := map[string]*generic.StringList{}
	for action, params := range Params {
		actionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	go func() {
		for {
			waitDuration := ia.refreshErrorTimeout
			if resp, err := ia.actionDispatcherClient.AddController(context.Background(), &mediaserverproto.ActionDispatcherParam{
				Type:        Type,
				Actions:     actionParams,
				Host:        &ia.host,
				Port:        ia.port,
				Concurrency: ia.concurrency,
				QueueSize:   ia.queueSize,
			}); err != nil {
				ia.logger.Error().Err(err).Msg("cannot add controller")
			} else {
				if resp.GetResponse().GetStatus() != generic.ResultStatus_OK {
					ia.logger.Error().Err(err).Msgf("cannot add controller: %s", resp.GetResponse().GetMessage())
				} else {
					waitDuration = time.Duration(resp.GetNextCallWait()) * time.Second
					ia.logger.Info().Msgf("controller %s:%d added", ia.host, ia.port)
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
	actionParams := map[string]*generic.StringList{}
	for action, params := range Params {
		actionParams[action] = &generic.StringList{
			Values: params,
		}
	}
	if resp, err := ia.actionDispatcherClient.RemoveController(context.Background(), &mediaserverproto.ActionDispatcherParam{
		Type:        Type,
		Actions:     actionParams,
		Host:        &ia.host,
		Port:        ia.port,
		Concurrency: ia.concurrency,
	}); err != nil {
		ia.logger.Error().Err(err).Msg("cannot remove controller")
	} else {
		if resp.GetStatus() != generic.ResultStatus_OK {
			ia.logger.Error().Err(err).Msgf("cannot remove controller: %s", resp.GetMessage())
		} else {
			ia.logger.Info().Msgf("controller %s:%d removed", ia.host, ia.port)
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

func (ia *imageAction) storeImage(img any, action string, item *mediaserverproto.Item, itemCache *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionCache.ActionParams, format, tile string) (*mediaserverproto.Cache, error) {
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
	filesize, mime, err := ia.image.Encode(img, target, format, tile)
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

func (ia *imageAction) resize(item *mediaserverproto.Item, itemCache *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionCache.ActionParams) (*mediaserverproto.Cache, error) {
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

	return ia.storeImage(img, "resize", item, itemCache, storage, params, format, tile)
}

func (ia *imageAction) convert(item *mediaserverproto.Item, itemCache *mediaserverproto.Cache, storage *mediaserverproto.Storage, params actionCache.ActionParams) (*mediaserverproto.Cache, error) {
	itemIdentifier := item.GetIdentifier()
	cacheItemMetadata := itemCache.GetMetadata()
	format := params.Get("format")
	if format == "" {
		format = "jpeg"
	}
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
	return ia.storeImage(img, "convert", item, itemCache, storage, params, format, tile)
}

func (ia *imageAction) Action(ctx context.Context, ap *mediaserverproto.ActionParam) (*mediaserverproto.Cache, error) {
	item := ap.GetItem()
	if item == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no item defined")
	}
	itemIdentifier := item.GetIdentifier()
	storage := ap.GetStorage()
	if storage == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no storage defined")
	}
	cacheItem, err := ia.db.GetCache(context.Background(), &mediaserverproto.CacheRequest{
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
