package service

import (
	"context"
	"fmt"
	"github.com/je4/filesystem/v2/pkg/writefs"
	generic "github.com/je4/genericproto/v2/pkg/generic/proto"
	"github.com/je4/mediaserveraction/v2/pkg/actionCache"
	"github.com/je4/mediaserveraction/v2/pkg/actionController"
	"github.com/je4/mediaserverimage/v2/pkg/image"
	pb "github.com/je4/mediaserverproto/v2/pkg/mediaserveraction/proto"
	mediaserverdbproto "github.com/je4/mediaserverproto/v2/pkg/mediaserverdb/proto"
	"github.com/je4/utils/v2/pkg/zLogger"
	"golang.org/x/exp/maps"
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
	"resize": []string{"size", "format"},
}

func NewActionService(adClient pb.ActionDispatcherClient, host string, port uint32, concurrency uint32, refreshErrorTimeout time.Duration, vfs fs.FS, db mediaserverdbproto.DBControllerClient, logger zLogger.ZLogger) (*imageAction, error) {
	return &imageAction{
		actionDispatcherClient: adClient,
		done:                   make(chan bool),
		host:                   host,
		port:                   port,
		refreshErrorTimeout:    refreshErrorTimeout,
		vFS:                    vfs,
		db:                     db,
		logger:                 logger,
		image:                  image.NewImage(logger),
		concurrency:            concurrency,
	}, nil
}

type imageAction struct {
	pb.UnimplementedActionControllerServer
	actionDispatcherClient pb.ActionDispatcherClient
	logger                 zLogger.ZLogger
	done                   chan bool
	host                   string
	port                   uint32
	refreshErrorTimeout    time.Duration
	vFS                    fs.FS
	db                     mediaserverdbproto.DBControllerClient
	image                  image.Image
	concurrency            uint32
}

func (ia *imageAction) Start() error {
	go func() {
		for {
			waitDuration := ia.refreshErrorTimeout
			if resp, err := ia.actionDispatcherClient.AddController(context.Background(), &pb.ActionDispatcherParam{
				Type:        Type,
				Action:      maps.Keys(Params),
				Host:        &ia.host,
				Port:        ia.port,
				Concurrency: ia.concurrency,
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
	ia.done <- true
}

func (ia *imageAction) Ping(context.Context, *emptypb.Empty) (*generic.DefaultResponse, error) {
	return &generic.DefaultResponse{
		Status:  generic.ResultStatus_OK,
		Message: "pong",
		Data:    nil,
	}, nil
}
func (ia *imageAction) GetParams(ctx context.Context, param *pb.ParamsParam) (*generic.StringList, error) {
	params, ok := Params[param.GetAction()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "action %s::%s not found", param.GetType(), param.GetAction())
	}
	return &generic.StringList{
		Values: params,
	}, nil
}

var isUrlRegexp = regexp.MustCompile(`^[a-z]+://`)

func (ia *imageAction) Action(ctx context.Context, ap *pb.ActionParam) (*mediaserverdbproto.Cache, error) {
	item := ap.GetItem()
	if item == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no item defined")
	}
	itemIdentifier := item.GetIdentifier()
	storage := ap.GetStorage()
	if storage == nil {
		return nil, status.Errorf(codes.InvalidArgument, "no storage defined")
	}
	imagePath := item.GetUrn()
	if !isUrlRegexp.MatchString(imagePath) {
		imagePath = fmt.Sprintf("%s/%s/%s", storage.GetFilebase(), storage.GetDatadir(), strings.TrimPrefix(imagePath, "/"))
	}
	action := ap.GetAction()
	if action == "" {
		return nil, status.Errorf(codes.InvalidArgument, "no action defined")
	}
	var params actionCache.ActionParams = ap.GetParams()
	size := params.Get("size")
	if size == "" {
		return nil, status.Errorf(codes.InvalidArgument, "no size defined")
	}
	format := params.Get("format")
	if format == "" {
		format = "jpeg"
	}
	ia.logger.Info().Msgf("action %s/%s/%s/%s", itemIdentifier.GetCollection(), itemIdentifier.GetSignature(), ap.GetAction(), params.String())
	fp, err := ia.vFS.Open(imagePath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "cannot open %s: %v", imagePath, err)
	}
	defer fp.Close()
	img, err := ia.image.Decode(fp)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot decode %s: %v", imagePath, err)
	}
	defer ia.image.Release(img)
	if err := ia.image.Resize(img, size); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot resize %s: %v", imagePath, err)
	}
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
	defer target.Close()
	filesize, mime, err := ia.image.Encode(img, target, format)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cannot encode %s: %v", targetPath, err)
	}
	width, height := ia.image.GetDimension(img)
	storageName := storage.GetName()
	resp := &mediaserverdbproto.Cache{
		Identifier: &mediaserverdbproto.ItemIdentifier{
			Collection: itemIdentifier.GetCollection(),
			Signature:  itemIdentifier.GetSignature(),
		},
		Metadata: &mediaserverdbproto.CacheMetadata{
			Action:      action,
			Params:      params.String(),
			Width:       int64(width),
			Height:      int64(height),
			Duration:    0,
			Size:        int64(filesize),
			MimeType:    mime,
			Path:        fmt.Sprintf("%s/%s", storage.GetDatadir(), cacheName),
			StorageName: &storageName,
		},
	}
	return resp, nil
}
