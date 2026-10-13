package rules

import (
	"strings"
	"testing"

	rpb "github.com/infinimesh/proto/node/rules"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestFromProtoRejectsBadCEL(t *testing.T) {
	_, err := FromProto(&rpb.Rule{Name: "hot", When: "1 + 2"})
	if err == nil || !strings.Contains(err.Error(), "reported") || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("error should name the valid fields: %v", err)
	}
	_, err = FromProto(&rpb.Rule{Name: "hot", When: "temperature > 1"})
	if err == nil || !strings.Contains(err.Error(), conditionFields) {
		t.Fatalf("error should name the valid fields: %v", err)
	}
}

func TestFromProtoKeepsUUID(t *testing.T) {
	r, err := FromProto(&rpb.Rule{Uuid: "id-a", Name: "hot", When: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "id-a" || r.Name != "hot" {
		t.Fatalf("%+v", r)
	}
}

func TestFromProtoWebhook(t *testing.T) {
	body, err := structpb.NewStruct(map[string]any{"fan": "on"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := FromProto(&rpb.Rule{
		Name: "hot", When: "reported.temp > 80", Cooldown: "10m",
		Actions: []*rpb.Action{{Desired: body}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.DesiredZero() {
		t.Fatal("missing desired")
	}
	if _, err := FromProto(&rpb.Rule{Name: "hot", When: "true", Actions: []*rpb.Action{{Log: &rpb.LogAction{}, Webhook: &rpb.WebhookAction{Url: "https://example.com"}}}}); err == nil {
		t.Fatal("two actions should fail")
	}
}

func (r *Rule) DesiredZero() bool {
	return len(r.Actions) != 1 || r.Actions[0].Desired["fan"] != "on"
}
