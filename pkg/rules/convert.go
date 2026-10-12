package rules

import (
	"fmt"
	"time"

	rpb "github.com/infinimesh/proto/node/rules"
	"google.golang.org/protobuf/types/known/structpb"
)

func FromProto(in *rpb.Rule) (*Rule, error) {
	if in == nil {
		return nil, fmt.Errorf("rule is empty")
	}
	r := &Rule{
		Name:    in.GetName(),
		When:    in.GetWhen(),
		Clear:   in.GetClear(),
		OnClear: in.GetOnClear(),
	}
	if in.GetCooldown() != "" {
		d, err := time.ParseDuration(in.GetCooldown())
		if err != nil {
			return nil, fmt.Errorf("cooldown: %w", err)
		}
		r.Cooldown = duration(d)
	}
	if in.GetRepeat() != "" {
		d, err := time.ParseDuration(in.GetRepeat())
		if err != nil {
			return nil, fmt.Errorf("repeat: %w", err)
		}
		r.Repeat = duration(d)
	}
	for _, a := range in.GetActions() {
		act, err := actionFromProto(a)
		if err != nil {
			return nil, err
		}
		r.Actions = append(r.Actions, act)
	}
	if _, err := NewEngine([]*Rule{r}, nil, &Actions{sleep: func(time.Duration) {}}); err != nil {
		return nil, err
	}
	return r, nil
}

func actionFromProto(a *rpb.Action) (Action, error) {
	if a == nil {
		return Action{}, fmt.Errorf("empty action")
	}
	n := 0
	var out Action
	if a.GetLog() != nil {
		n++
		out.Log = &LogAction{Message: a.GetLog().GetMessage()}
	}
	if a.GetWebhook() != nil {
		n++
		out.Webhook = &WebhookAction{URL: a.GetWebhook().GetUrl(), Secret: a.GetWebhook().GetSecret()}
	}
	if a.GetMqtt() != nil {
		n++
		out.MQTT = &MQTTAction{Topic: a.GetMqtt().GetTopic(), QoS: byte(a.GetMqtt().GetQos()), Retain: a.GetMqtt().GetRetain()}
	}
	if a.GetDesired() != nil {
		n++
		out.Desired = a.GetDesired().AsMap()
	}
	if n != 1 {
		return Action{}, fmt.Errorf("set exactly one of log, webhook, mqtt, desired")
	}
	return out, nil
}

func DesiredStruct(data map[string]any) (*structpb.Struct, error) {
	if data == nil {
		return nil, nil
	}
	return structpb.NewStruct(data)
}
