package rules

import (
	"testing"
	"time"

	rpb "github.com/infinimesh/proto/node/rules"
	"github.com/infinimesh/proto/shadow"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestParseEdges(t *testing.T) {
	if _, err := Parse([]byte("["), func(string) string { return "" }); err == nil {
		t.Fatal("yaml")
	}
	if _, err := Parse([]byte("rules: [null]"), func(string) string { return "" }); err == nil {
		t.Fatal("nil rule")
	}
	cases := []string{
		"rules: [{name: 'a b', when: 'true'}]",
		"rules: [{name: 'a/b', when: 'true'}]",
		"rules: [{name: hot, when: 'true'}, {name: hot, when: 'true'}]",
		"rules: [{name: hot}]",
		"rules: [{name: hot, when: '1+2'}]",
		"rules: [{name: hot, when: 'true', clear: '1+2'}]",
		"rules: [{name: hot, when: 'true', actions: [{webhook: {url: 'ftp://x'}}]}]",
		"rules: [{name: hot, when: 'true', actions: [{mqtt: {qos: 1}}]}]",
		"rules: [{name: hot, when: 'true', actions: [{mqtt: {topic: t, qos: 3}}]}]",
		"rules: [{name: hot, when: 'true', actions: [{log: {message: hi}, webhook: {url: 'https://x'}}]}]",
		"rules: [{name: hot, when: 'true', actions: [{}]}]",
		"rules: [{name: hot, when: 'true', cooldown: yesterday}]",
	}
	for _, raw := range cases {
		if _, err := Parse([]byte(raw), func(string) string { return "" }); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	file, err := Parse([]byte("rules: [{name: hot, when: 'true', cooldown: 10m, repeat: 1s, actions: [{log: {message: '${WHO} $$'}}]}]"), func(k string) string {
		if k == "WHO" {
			return "ops"
		}
		return ""
	})
	if err != nil || file.Rules[0].Actions[0].Log.Message != "ops $" {
		t.Fatalf("%v %+v", err, file)
	}
	if (*Rule)(nil).alarmKey() != "" || (&Rule{ID: "id", Name: "n"}).alarmKey() != "id" {
		t.Fatal("alarm key")
	}
	if _, err := NewEngine([]*Rule{{Name: "", When: "true"}}, nil, nil); err == nil {
		t.Fatal("empty name")
	}
	if _, err := NewEngine([]*Rule{nil}, nil, &Actions{sleep: func(time.Duration) {}}); err == nil {
		t.Fatal("nil rule")
	}
	if _, err := NewEngine([]*Rule{{Name: "a", When: "true"}, {Name: "a", When: "true"}}, nil, nil); err == nil {
		t.Fatal("dup")
	}
	if _, err := NewEngine([]*Rule{{Name: "a", When: "true", Clear: "1+2"}}, nil, nil); err == nil {
		t.Fatal("clear")
	}
	eng, err := NewEngine([]*Rule{{Name: "a", When: "true"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetNow(nil)
	eng.rules[0].Actions = nil
	eng.actions.client = nil
	eng.Check(Vars{Device: "d"}, nil)
	eng.actions = nil
	eng.Check(Vars{Device: "d"}, nil)
	Apply(nil, &shadow.Shadow{Device: "d"})
	Apply(eng, nil)
	Apply(eng, &shadow.Shadow{})
}

func TestConvertEdges(t *testing.T) {
	if _, err := FromProto(nil); err == nil {
		t.Fatal("nil")
	}
	if _, err := FromProto(&rpb.Rule{Name: "hot", When: "true", Cooldown: "nope"}); err == nil {
		t.Fatal("cooldown")
	}
	if _, err := FromProto(&rpb.Rule{Name: "hot", When: "true", Repeat: "nope"}); err == nil {
		t.Fatal("repeat")
	}
	if _, err := actionFromProto(nil); err == nil {
		t.Fatal("nil action")
	}
	act, err := actionFromProto(&rpb.Action{Mqtt: &rpb.MQTTAction{Topic: "t", Qos: 1, Retain: true}})
	if err != nil || act.MQTT == nil || act.MQTT.Topic != "t" || act.MQTT.QoS != 1 || !act.MQTT.Retain {
		t.Fatalf("%+v %v", act, err)
	}
	if _, err := actionFromProto(&rpb.Action{}); err == nil {
		t.Fatal("empty action")
	}
	if _, err := actionFromProto(&rpb.Action{Log: &rpb.LogAction{}, Webhook: &rpb.WebhookAction{Url: "https://x"}}); err == nil {
		t.Fatal("two")
	}
	body, err := structpb.NewStruct(map[string]any{"fan": "on"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := actionFromProto(&rpb.Action{Desired: body})
	if err != nil || got.Desired["fan"] != "on" {
		t.Fatal(err)
	}
	logAct, err := actionFromProto(&rpb.Action{Log: &rpb.LogAction{Message: "hi"}})
	if err != nil || logAct.Log.Message != "hi" {
		t.Fatal(err)
	}
	if st, err := DesiredStruct(nil); st != nil || err != nil {
		t.Fatalf("%v %v", st, err)
	}
	st, err := DesiredStruct(map[string]any{"a": 1})
	if err != nil || st.AsMap()["a"] != float64(1) {
		t.Fatal(err)
	}
	if _, err := DesiredStruct(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("struct")
	}
	r, err := FromProto(&rpb.Rule{Name: "hot", When: "true", Repeat: "1s", Actions: []*rpb.Action{{Webhook: &rpb.WebhookAction{Url: "https://example.com", Secret: "s"}}}})
	if err != nil || r.Actions[0].Webhook.URL == "" || time.Duration(r.Repeat) != time.Second {
		t.Fatalf("%+v %v", r, err)
	}
}
