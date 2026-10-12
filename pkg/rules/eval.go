package rules

import (
	"github.com/infinimesh/proto/shadow"
)

func shadowMap(st *shadow.State) map[string]any {
	if st == nil || st.GetData() == nil {
		return map[string]any{}
	}
	return st.GetData().AsMap()
}

// Apply checks one shadow against the engine.
func Apply(e *Engine, sh *shadow.Shadow) {
	if e == nil || sh == nil || sh.GetDevice() == "" {
		return
	}
	v := Vars{
		Reported:  shadowMap(sh.GetReported()),
		Desired:   shadowMap(sh.GetDesired()),
		Connected: sh.GetConnection().GetConnected(),
		Device:    sh.GetDevice(),
		Title:     sh.GetTitle(),
		Tags:      append([]string(nil), sh.GetTags()...),
		Namespace: sh.GetNamespace(),
	}
	e.Check(v, map[string]any{
		"reported": v.Reported, "desired": v.Desired, "connected": v.Connected,
	})
}
