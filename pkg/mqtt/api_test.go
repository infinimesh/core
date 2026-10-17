package mqtt

import "testing"

func TestMessageTypes(t *testing.T) {
	in := IncomingMessage{ProtoLevel: 4, SourceTopic: "a", SourceDevice: "d", Data: []byte("x")}
	out := OutgoingMessage{DeviceID: "d", SubPath: "s", Data: []byte("y")}
	msg := Message{Topic: "t", Data: map[string]interface{}{"k": 1}}
	p := Payload{Timestamp: "now", Message: []Message{msg}}
	if in.SourceTopic != "a" || out.SubPath != "s" || p.Message[0].Topic != "t" {
		t.Fatalf("%+v %+v %+v", in, out, p)
	}
}
