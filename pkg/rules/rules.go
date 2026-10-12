package rules

import (
	"fmt"
	"os"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"gopkg.in/yaml.v3"
)

// File is the rules file.
type File struct {
	Rules []*Rule `yaml:"rules"`
}

// Rule raises an alarm for a device when When is true, and clears it when
// Clear is true (or, without Clear, when When is false again).
type Rule struct {
	Name     string   `yaml:"name"`
	When     string   `yaml:"when"`
	Clear    string   `yaml:"clear"`
	Cooldown duration `yaml:"cooldown"`
	Repeat   duration `yaml:"repeat"`
	OnClear  bool     `yaml:"on_clear"`
	Actions  []Action `yaml:"actions"`

	when  cel.Program
	clear cel.Program
}

// Action is one thing to do when an alarm is raised (or cleared, with
// on_clear). Exactly one field is set.
type Action struct {
	Log     *LogAction     `yaml:"log"`
	Webhook *WebhookAction `yaml:"webhook"`
	MQTT    *MQTTAction    `yaml:"mqtt"`
	Desired map[string]any `yaml:"desired"`
}

type LogAction struct {
	Message string `yaml:"message"`
}

type WebhookAction struct {
	URL    string `yaml:"url"`
	Secret string `yaml:"secret"`
}

type MQTTAction struct {
	Topic  string `yaml:"topic"`
	QoS    byte   `yaml:"qos"`
	Retain bool   `yaml:"retain"`
}

type duration time.Duration

func (d *duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = duration(v)
	return nil
}

func newEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("reported", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("desired", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("connected", cel.BoolType),
		cel.Variable("device", cel.StringType),
		cel.Variable("title", cel.StringType),
		cel.Variable("tags", cel.ListType(cel.StringType)),
		cel.Variable("ns", cel.StringType),
		cel.CrossTypeNumericComparisons(true),
	)
}

const conditionFields = "reported, desired, connected, device, title, tags, ns"

func compile(env *cel.Env, expr string) (cel.Program, error) {
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, fmt.Errorf("%w; valid fields: %s", iss.Err(), conditionFields)
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return nil, fmt.Errorf("%q must be true or false, not %s; valid fields: %s", expr, ast.OutputType(), conditionFields)
	}
	return env.Program(ast)
}

// expandEnv replaces ${VAR} in the file, so secrets can stay out of it.
func expandEnv(b []byte, getenv func(string) string) []byte {
	return []byte(os.Expand(string(b), func(k string) string {
		if k == "$" {
			return "$"
		}
		return getenv(k)
	}))
}

// Parse reads and checks a rules file.
func Parse(b []byte, getenv func(string) string) (*File, error) {
	var f File
	if err := yaml.Unmarshal(expandEnv(b, getenv), &f); err != nil {
		return nil, err
	}
	env, err := newEnv()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i, r := range f.Rules {
		if r == nil {
			return nil, fmt.Errorf("rule %d is empty", i+1)
		}
		if r.Name == "" || strings.ContainsAny(r.Name, "/ ") {
			return nil, fmt.Errorf("rule %d: name is required and cannot hold spaces or /", i+1)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("rule %s: name used twice", r.Name)
		}
		seen[r.Name] = true
		if r.When == "" {
			return nil, fmt.Errorf("rule %s: when is required", r.Name)
		}
		if r.when, err = compile(env, r.When); err != nil {
			return nil, fmt.Errorf("rule %s: when: %w", r.Name, err)
		}
		if r.Clear != "" {
			if r.clear, err = compile(env, r.Clear); err != nil {
				return nil, fmt.Errorf("rule %s: clear: %w", r.Name, err)
			}
		}
		if len(r.Actions) == 0 {
			r.Actions = []Action{{Log: &LogAction{}}}
		}
		for j, a := range r.Actions {
			n := 0
			if a.Log != nil {
				n++
			}
			if a.Webhook != nil {
				n++
				if !strings.HasPrefix(a.Webhook.URL, "http://") && !strings.HasPrefix(a.Webhook.URL, "https://") {
					return nil, fmt.Errorf("rule %s action %d: webhook url must start with http:// or https://", r.Name, j+1)
				}
			}
			if a.MQTT != nil {
				n++
				if a.MQTT.Topic == "" || a.MQTT.QoS > 2 {
					return nil, fmt.Errorf("rule %s action %d: mqtt needs a topic and qos 0, 1 or 2", r.Name, j+1)
				}
			}
			if a.Desired != nil {
				n++
			}
			if n != 1 {
				return nil, fmt.Errorf("rule %s action %d: set exactly one of log, webhook, mqtt, desired", r.Name, j+1)
			}
		}
	}
	return &f, nil
}

// Vars is what a condition can see.
type Vars struct {
	Reported      map[string]any
	Desired       map[string]any
	Connected     bool
	Device, Title string
	Tags          []string
	Namespace     string
}

func (v Vars) activation() map[string]any {
	r, d, t := v.Reported, v.Desired, v.Tags
	if r == nil {
		r = map[string]any{}
	}
	if d == nil {
		d = map[string]any{}
	}
	if t == nil {
		t = []string{}
	}
	return map[string]any{"reported": r, "desired": d, "connected": v.Connected,
		"device": v.Device, "title": v.Title, "tags": t, "ns": v.Namespace}
}

// eval runs a program. ok is false when it cannot be decided, for example
// when a field it reads is missing.
func eval(p cel.Program, v Vars) (result, ok bool) {
	out, _, err := p.Eval(v.activation())
	if err != nil {
		return false, false
	}
	b, isBool := out.Value().(bool)
	return b, isBool
}
