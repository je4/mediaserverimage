package main

import (
	"flag"
	"fmt"
	"github.com/je4/filesystem/v3/pkg/vfsrw"
	"github.com/je4/mediaserverimage/v2/configs"
	"github.com/je4/mediaserverimage/v2/pkg/service"
	mediaserverproto "github.com/je4/mediaserverproto/v2/pkg/mediaserver/proto"
	resolver "github.com/je4/miniresolver/v2/pkg/resolver"
	"github.com/je4/trustutil/v2/pkg/grpchelper"
	"github.com/je4/trustutil/v2/pkg/loader"
	"github.com/je4/utils/v2/pkg/zLogger"
	"github.com/rs/zerolog"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

var cfg = flag.String("config", "", "location of toml configuration file")

func main() {
	flag.Parse()
	var cfgFS fs.FS
	var cfgFile string
	if *cfg != "" {
		cfgFS = os.DirFS(filepath.Dir(*cfg))
		cfgFile = filepath.Base(*cfg)
	} else {
		cfgFS = configs.ConfigFS
		cfgFile = "mediaserverimage.toml"
	}
	conf := &MediaserverImageConfig{
		LocalAddr:   "localhost:8443",
		LogLevel:    "DEBUG",
		Concurrency: 3,
	}
	if err := LoadMediaserverImageConfig(cfgFS, cfgFile, conf); err != nil {
		log.Fatalf("cannot load toml from [%v] %s: %v", cfgFS, cfgFile, err)
	}
	// create logger instance
	var out io.Writer = os.Stdout
	if conf.LogFile != "" {
		fp, err := os.OpenFile(conf.LogFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("cannot open logfile %s: %v", conf.LogFile, err)
		}
		defer fp.Close()
		out = fp
	}

	/*
		addrs, err := net.InterfaceAddrs()
		if err != nil {
			log.Fatalf("cannot get interface addresses: %v", err)
		}
		addrStr := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			addrStr = append(addrStr, addr.String())
		}
	*/
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("cannot get hostname: %v", err)
	}

	output := zerolog.ConsoleWriter{Out: out, TimeFormat: time.RFC3339}
	_logger := zerolog.New(output).With().Timestamp().Str("service", "mediaserverimage"). /*.Array("addrs", zLogger.StringArray(addrStr))*/ Str("host", hostname).Str("addr", conf.LocalAddr).Logger()
	_logger.Level(zLogger.LogLevel(conf.LogLevel))
	var logger zLogger.ZLogger = &_logger
	//	var dbLogger = zerologadapter.NewLogger(_logger)

	vfs, err := vfsrw.NewFS(conf.VFS, logger)
	if err != nil {
		logger.Panic().Err(err).Msg("cannot create vfs")
	}
	defer func() {
		if err := vfs.Close(); err != nil {
			logger.Error().Err(err).Msg("cannot close vfs")
		}
	}()

	// create TLS Certificate.
	// the certificate MUST contain <package>.<service> as DNS name
	serverTLSConfig, serverLoader, err := loader.CreateServerLoader(true, &conf.ServerTLS, nil, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create server loader")
	}
	defer serverLoader.Close()

	// create client TLS certificate
	// the certificate MUST contain "grpc:miniresolverproto.MiniResolver" or "*" in URIs
	clientTLSConfig, clientLoader, err := loader.CreateClientLoader(&conf.ClientTLS, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create client loader")
	}
	defer clientLoader.Close()

	// create resolver client
	resolverClient, err := resolver.NewMiniresolverClient(conf.ResolverAddr, conf.GRPCClient, clientTLSConfig, serverTLSConfig, time.Duration(conf.ResolverTimeout), time.Duration(conf.ResolverNotFoundTimeout), logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create resolver client")
	}
	defer resolverClient.Close()

	actionDispatcherClient, err := resolver.NewClient[mediaserverproto.ActionDispatcherClient](resolverClient, mediaserverproto.NewActionDispatcherClient, mediaserverproto.ActionDispatcher_ServiceDesc.ServiceName)
	if err != nil {
		logger.Panic().Msgf("cannot create mediaserveractiondispatcher grpc client: %v", err)
	}
	resolver.DoPing(actionDispatcherClient, logger)

	dbClient, err := resolver.NewClient[mediaserverproto.DatabaseClient](resolverClient, mediaserverproto.NewDatabaseClient, mediaserverproto.Database_ServiceDesc.ServiceName)
	if err != nil {
		logger.Panic().Msgf("cannot create mediaserverdb grpc client: %v", err)
	}
	resolver.DoPing(dbClient, logger)

	host, portStr, err := net.SplitHostPort(conf.LocalAddr)
	if err != nil {
		logger.Fatal().Err(err).Msgf("invalid addr '%s' in config", conf.LocalAddr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		logger.Fatal().Err(err).Msgf("invalid port '%s'", portStr)
	}
	srv, err := service.NewActionService(actionDispatcherClient, host, uint32(port), conf.Concurrency, time.Duration(conf.ResolverNotFoundTimeout), vfs, dbClient, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create service")
	}
	if err := srv.Start(); err != nil {
		logger.Fatal().Err(err).Msg("cannot start service")
	}
	defer srv.GracefulStop()

	// create grpc server with resolver for name resolution
	grpcServer, err := grpchelper.NewServer(conf.LocalAddr, serverTLSConfig, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("cannot create server")
	}
	// register the server
	mediaserverproto.RegisterActionServer(grpcServer, srv)

	grpcServer.Startup()

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	fmt.Println("press ctrl+c to stop server")
	s := <-done
	fmt.Println("got signal:", s)

	defer grpcServer.Shutdown()

}
