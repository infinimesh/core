package main

import (
	"context"
	"net/http"
	"time"

	"github.com/infinimesh/core/pkg/graph"
	"github.com/infinimesh/core/pkg/graph/schema"
	"github.com/infinimesh/core/pkg/httpfs"
	logger "github.com/infinimesh/core/pkg/log"
	"github.com/infinimesh/core/pkg/observe"
	"github.com/infinimesh/proto/node/access"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func main() {
	viper.AutomaticEnv()
	viper.SetDefault("DB_HOST", "db:8529")
	viper.SetDefault("DB_CRED", "root:openSesame")
	viper.SetDefault("INF_DEFAULT_ROOT_PASS", "infinimesh")
	viper.SetDefault("HTTP_FS_ADDR", ":8000")
	log := logger.NewLogger()
	stop := observe.Start("http-fs")
	defer stop()

	cfg, err := httpfs.ConfigFromEnv()
	if err != nil {
		log.Fatal("config", zap.Error(err))
	}
	signing := viper.GetString("SIGNING_KEY")
	if signing == "" {
		log.Fatal("SIGNING_KEY is empty")
	}
	db := schema.InitDB(log, viper.GetString("DB_HOST"), viper.GetString("DB_CRED"), viper.GetString("INF_DEFAULT_ROOT_PASS"), false)
	store, err := httpfs.Open(cfg)
	if err != nil {
		log.Fatal("storage", zap.Error(err))
	}
	handler := httpfs.New(store, graphAccess{repo: graph.NewInfinimeshCommonActionsRepo(db)}, []byte(signing), cfg)
	addr := viper.GetString("HTTP_FS_ADDR")
	log.Info("http-fs", zap.String("addr", addr), zap.Bool("s3", cfg.S3 != nil))
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal("listen", zap.Error(srv.ListenAndServe()))
}

type graphAccess struct {
	repo graph.InfinimeshCommonActionsRepo
}

func (g graphAccess) Level(ctx context.Context, account, namespace string) (access.Level, error) {
	_, level := g.repo.AccessLevel(ctx, graph.NewBlankAccountDocument(account), graph.NewBlankNamespaceDocument(namespace))
	return level, nil
}
