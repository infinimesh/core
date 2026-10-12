package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/graph/schema"
	logger "github.com/infinimesh/core/pkg/log"
	"github.com/infinimesh/core/pkg/observe"
	"github.com/infinimesh/core/pkg/rules"
	rpb "github.com/infinimesh/proto/node/rules"
	pb "github.com/infinimesh/proto/shadow"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func main() {
	viper.AutomaticEnv()
	viper.SetDefault("DB_HOST", "db:8529")
	viper.SetDefault("DB_CRED", "root:openSesame")
	viper.SetDefault("INF_DEFAULT_ROOT_PASS", "infinimesh")
	viper.SetDefault("REDIS_HOST", "redis:6379")
	viper.SetDefault("SHADOW_HOST", "shadow:8000")
	log := logger.NewLogger()
	stop := observe.Start("rules")
	defer stop()

	db := schema.InitDB(log, viper.GetString("DB_HOST"), viper.GetString("DB_CRED"), viper.GetString("INF_DEFAULT_ROOT_PASS"), false)
	rdb := redis.NewClient(&redis.Options{Addr: viper.GetString("REDIS_HOST")})
	conn, err := grpc.Dial(viper.GetString("SHADOW_HOST"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal("shadow", zap.Error(err))
	}
	client := pb.NewShadowServiceClient(conn)
	actions := rules.NewActions()
	if broker := viper.GetString("RULES_MQTT_BROKER"); broker != "" {
		actions.SetMQTT(&mqttPub{
			addr: broker, user: viper.GetString("RULES_MQTT_USERNAME"), pass: viper.GetString("RULES_MQTT_PASSWORD"),
		})
	}
	actions.SetDesired(&desiredPatch{client: client})

	ctx := context.Background()
	running := map[string]context.CancelFunc{}
	for {
		names, err := namespaces(ctx, db)
		if err != nil {
			log.Warn("rules", zap.Error(err))
		}
		seen := map[string]bool{}
		for _, ns := range names {
			seen[ns] = true
			if _, ok := running[ns]; ok {
				continue
			}
			nsCtx, cancel := context.WithCancel(ctx)
			running[ns] = cancel
			go follow(nsCtx, log, db, rdb, client, actions, ns)
		}
		for ns, cancel := range running {
			if !seen[ns] {
				cancel()
				delete(running, ns)
			}
		}
		time.Sleep(15 * time.Second)
	}
}

func namespaces(ctx context.Context, db driver.Database) ([]string, error) {
	cr, err := db.Query(ctx, `FOR r IN @@col RETURN DISTINCT r.namespace`, map[string]interface{}{"@col": schema.RULES_COL})
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	var out []string
	for {
		var ns string
		_, err := cr.ReadDocument(ctx, &ns)
		if driver.IsNoMoreDocuments(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if ns != "" {
			out = append(out, ns)
		}
	}
}

func follow(ctx context.Context, log *zap.Logger, db driver.Database, rdb *redis.Client, client pb.ShadowServiceClient, actions *rules.Actions, ns string) {
	store, _ := rules.OpenStore("")
	for ctx.Err() == nil {
		stream, err := client.StreamShadow(ctx, &pb.StreamShadowRequest{Namespace: ns})
		if err != nil {
			log.Warn("stream", zap.String("namespace", ns), zap.Error(err))
			time.Sleep(2 * time.Second)
			continue
		}
		for {
			sh, err := stream.Recv()
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("stream closed", zap.String("namespace", ns), zap.Error(err))
				}
				break
			}
			if sh.GetDevice() == "" {
				continue
			}
			set, err := loadRules(ctx, db, ns)
			if err != nil {
				log.Warn("load rules", zap.Error(err))
				continue
			}
			if len(set) == 0 {
				continue
			}
			current, _ := loadAlarms(ctx, db, ns)
			store.Replace(current)
			eng, err := rules.NewEngine(set, store, actions)
			if err != nil {
				log.Warn("engine", zap.Error(err))
				continue
			}
			rules.Apply(eng, sh)
			if err := saveAlarms(ctx, db, rdb, ns, store.List()); err != nil {
				log.Warn("save alarms", zap.Error(err))
			}
		}
	}
}

func loadRules(ctx context.Context, db driver.Database, ns string) ([]*rules.Rule, error) {
	cr, err := db.Query(ctx, `FOR r IN @@col FILTER r.namespace == @ns RETURN r`, map[string]interface{}{
		"@col": schema.RULES_COL, "ns": ns,
	})
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	var out []*rules.Rule
	for {
		var in rpb.Rule
		_, err := cr.ReadDocument(ctx, &in)
		if driver.IsNoMoreDocuments(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		r, err := rules.FromProto(&in)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
}

func loadAlarms(ctx context.Context, db driver.Database, ns string) ([]*rules.Alarm, error) {
	cr, err := db.Query(ctx, `FOR a IN @@col FILTER a.namespace == @ns RETURN a`, map[string]interface{}{
		"@col": schema.ALARMS_COL, "ns": ns,
	})
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	var out []*rules.Alarm
	for {
		var a rpb.Alarm
		_, err := cr.ReadDocument(ctx, &a)
		if driver.IsNoMoreDocuments(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, fromProtoAlarm(&a))
	}
}

type alarmDoc struct {
	*rpb.Alarm
	driver.DocumentMeta
}

func saveAlarms(ctx context.Context, db driver.Database, rdb *redis.Client, ns string, list []*rules.Alarm) error {
	col, err := db.Collection(ctx, schema.ALARMS_COL)
	if err != nil {
		return err
	}
	for _, a := range list {
		doc := toProtoAlarm(a, ns)
		key := a.Rule + "_" + a.Device
		_, err := col.ReplaceDocument(ctx, key, doc)
		if err != nil {
			_, err = col.CreateDocument(ctx, alarmDoc{Alarm: doc, DocumentMeta: driver.DocumentMeta{Key: key}})
		}
		if err != nil {
			return err
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if err := rdb.Set(ctx, fmt.Sprintf("rules:alarm:%s:%s:%s", ns, a.Rule, a.Device), raw, 0).Err(); err != nil {
			return err
		}
	}
	return nil
}

func fromProtoAlarm(a *rpb.Alarm) *rules.Alarm {
	out := &rules.Alarm{Rule: a.GetRule(), Device: a.GetDevice(), Title: a.GetTitle(), Count: int(a.GetCount())}
	switch a.GetState() {
	case rpb.AlarmState_ACKED:
		out.State = rules.Acked
	case rpb.AlarmState_CLEARED:
		out.State = rules.Cleared
	default:
		out.State = rules.Raised
	}
	if t := a.GetRaisedAt(); t != nil {
		out.RaisedAt = t.AsTime()
	}
	if t := a.GetFiredAt(); t != nil {
		out.FiredAt = t.AsTime()
	}
	if t := a.GetAckedAt(); t != nil {
		out.AckedAt = t.AsTime()
	}
	if t := a.GetClearedAt(); t != nil {
		out.ClearedAt = t.AsTime()
	}
	return out
}

func toProtoAlarm(a *rules.Alarm, ns string) *rpb.Alarm {
	st := rpb.AlarmState_RAISED
	switch a.State {
	case rules.Acked:
		st = rpb.AlarmState_ACKED
	case rules.Cleared:
		st = rpb.AlarmState_CLEARED
	}
	out := &rpb.Alarm{
		Rule: a.Rule, Device: a.Device, Title: a.Title, State: st, Count: int32(a.Count), Namespace: ns,
	}
	if !a.RaisedAt.IsZero() {
		out.RaisedAt = timestamppb.New(a.RaisedAt)
	}
	if !a.FiredAt.IsZero() {
		out.FiredAt = timestamppb.New(a.FiredAt)
	}
	if !a.AckedAt.IsZero() {
		out.AckedAt = timestamppb.New(a.AckedAt)
	}
	if !a.ClearedAt.IsZero() {
		out.ClearedAt = timestamppb.New(a.ClearedAt)
	}
	return out
}

type desiredPatch struct {
	client pb.ShadowServiceClient
}

func (p *desiredPatch) PatchDesired(device string, data map[string]any) error {
	st, err := structpb.NewStruct(data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = p.client.Patch(ctx, &pb.Shadow{Device: device, Desired: &pb.State{Data: st}})
	return err
}
