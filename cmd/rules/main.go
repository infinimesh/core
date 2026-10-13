package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arangodb/go-driver"
	"github.com/go-redis/redis/v8"
	"github.com/infinimesh/core/pkg/graph"
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
	serve(ctx, log, dbSource{db: db, rdb: rdb}, client, actions)
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
		var doc graph.StoredAlarm
		_, err := cr.ReadDocument(ctx, &doc)
		if driver.IsNoMoreDocuments(err) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, doc.Alarm())
	}
}

func saveAlarms(ctx context.Context, db driver.Database, rdb *redis.Client, ns string, list []*rules.Alarm) error {
	col, err := db.Collection(ctx, schema.ALARMS_COL)
	if err != nil {
		return err
	}
	for _, a := range list {
		doc := graph.AlarmDocument(a, ns)
		_, err := col.ReplaceDocument(ctx, doc.Key, doc)
		if err != nil {
			_, err = col.CreateDocument(ctx, doc)
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
		if a.Title != "" && a.Title != a.Rule {
			_ = rdb.Del(ctx, fmt.Sprintf("rules:alarm:%s:%s:%s", ns, a.Title, a.Device)).Err()
		}
	}
	return removeOtherAlarms(ctx, db, ns, list)
}

func removeOtherAlarms(ctx context.Context, db driver.Database, ns string, list []*rules.Alarm) error {
	rulesKept := make([]string, 0, len(list))
	seen := map[string]struct{}{}
	for _, a := range list {
		if a == nil || a.Rule == "" {
			continue
		}
		if _, ok := seen[a.Rule]; ok {
			continue
		}
		seen[a.Rule] = struct{}{}
		rulesKept = append(rulesKept, a.Rule)
	}
	q := `FOR a IN @@col FILTER a.namespace == @ns REMOVE a IN @@col`
	bind := map[string]interface{}{"@col": schema.ALARMS_COL, "ns": ns}
	if len(rulesKept) > 0 {
		q = `FOR a IN @@col FILTER a.namespace == @ns AND a.rule NOT IN @rules REMOVE a IN @@col`
		bind["rules"] = rulesKept
	}
	cr, err := db.Query(ctx, q, bind)
	if err != nil {
		return err
	}
	return cr.Close()
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
