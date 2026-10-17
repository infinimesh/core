package main

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/infinimesh/core/v3/pkg/ingest"
	pb "github.com/infinimesh/proto/shadow"
	"google.golang.org/protobuf/encoding/protojson"
)

func mountIngest(router *mux.Router, client pb.ShadowServiceClient, key []byte, rates func(context.Context, string) (float64, float64, string)) {
	h := &ingest.Handler{
		Key:   key,
		Quota: rates,
		Patch: func(ctx context.Context, device string, reported *pb.State) error {
			_, err := client.Patch(ctx, &pb.Shadow{Device: device, Reported: reported})
			return err
		},
		Desired: func(ctx context.Context, device string) ([]byte, uint64, error) {
			res, err := client.Get(ctx, &pb.GetRequest{Device: device, Devices: []string{device}})
			if err != nil {
				return nil, 0, err
			}
			for _, sh := range res.GetShadows() {
				if sh.GetDevice() != device || sh.GetDesired() == nil || sh.GetDesired().GetData() == nil {
					continue
				}
				raw, err := protojson.Marshal(sh.GetDesired().GetData())
				if err != nil {
					return nil, 0, err
				}
				return raw, sh.GetDesired().GetVersion(), nil
			}
			return []byte("{}"), 0, nil
		},
	}
	h.Register(func(pattern string, handler http.HandlerFunc) {
		router.HandleFunc(pattern, handler)
	})
}
