// Package admin is remote administration of repeaters and room servers: logins, status, settings,
// and the command console, queued so the radio only ever has one request outstanding.
package admin

import (
	_ "embed"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

//go:embed settings.toml
var settingsTOML string

type Kind string

const (
	KindText     Kind = "text"
	KindPassword Kind = "password"
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindFloat    Kind = "float"
	KindChoice   Kind = "choice"
	KindRadio    Kind = "radio"
)

// Setting is one entry in the schema: what it's called, what values it takes, and what changing it does.
type Setting struct {
	Key       string            `toml:"key"`
	Label     string            `toml:"label"`
	Group     string            `toml:"group"`
	Kind      Kind              `toml:"kind"`
	Min       *float64          `toml:"min"`
	Max       *float64          `toml:"max"`
	Options   []string          `toml:"options"`
	Unit      string            `toml:"unit"`
	Reboot    bool              `toml:"reboot"`
	Dangerous bool              `toml:"dangerous"`
	ReadOnly  bool              `toml:"readonly"`
	Roles     []string          `toml:"roles"`
	Default   string            `toml:"default"`
	Help      string            `toml:"help"`
	Write     map[string]string `toml:"write"`
}

// Wire is what `set` takes for a value, which isn't always what `get` prints.
func (s Setting) Wire(v string) string {
	if w, ok := s.Write[v]; ok {
		return w
	}
	return v
}

// FromWire turns what `set` was sent back into the value `get` prints.
func (s Setting) FromWire(w string) string {
	for v, wire := range s.Write {
		if wire == w {
			return v
		}
	}
	return w
}

var schema = func() []Setting {
	var doc struct {
		Setting []Setting `toml:"setting"`
	}
	if _, err := toml.Decode(settingsTOML, &doc); err != nil {
		panic("admin: settings.toml: " + err.Error())
	}
	return doc.Setting
}()

// Settings lists the schema for a node type, in file order.
func Settings(t radio.ContactType) []Setting {
	role := map[radio.ContactType]string{radio.TypeRepeater: "repeater", radio.TypeRoom: "room"}[t]
	var out []Setting
	for _, s := range schema {
		if len(s.Roles) == 0 || slices.Contains(s.Roles, role) {
			out = append(out, s)
		}
	}
	return out
}

// Lookup finds a setting by key.
func Lookup(key string) (Setting, bool) {
	for _, s := range schema {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// Validate checks a value before it goes over the air, so mistakes are caught without a round trip.
func (s Setting) Validate(v string) error {
	if s.ReadOnly {
		return fmt.Errorf("%s can't be changed", s.Label)
	}
	v = strings.TrimSpace(v)
	inRange := func(f float64) error {
		if s.Min != nil && f < *s.Min || s.Max != nil && f > *s.Max {
			return fmt.Errorf("%s must be between %s and %s%s", s.Label, trim(*s.Min), trim(*s.Max), unit(s.Unit))
		}
		return nil
	}
	switch s.Kind {
	case KindBool:
		if v != "on" && v != "off" {
			return fmt.Errorf("%s is on or off", s.Label)
		}
	case KindInt:
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s must be a whole number", s.Label)
		}
		return inRange(float64(n))
	case KindFloat:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%s must be a number", s.Label)
		}
		return inRange(f)
	case KindChoice:
		if !slices.Contains(s.Options, v) {
			return fmt.Errorf("%s is one of %s", s.Label, strings.Join(s.Options, ", "))
		}
	case KindRadio:
		return validRadio(v)
	case KindText:
		if v == "" {
			return fmt.Errorf("%s can't be empty", s.Label)
		}
	}
	return nil
}

func validRadio(v string) error {
	parts := strings.Split(v, ",")
	if len(parts) != 4 {
		return fmt.Errorf("radio is frequency,bandwidth,spreading factor,coding rate, e.g. 869.618,62.5,8,8")
	}
	freq, err1 := strconv.ParseFloat(parts[0], 64)
	bw, err2 := strconv.ParseFloat(parts[1], 64)
	sf, err3 := strconv.Atoi(parts[2])
	cr, err4 := strconv.Atoi(parts[3])
	switch {
	case err1 != nil || err2 != nil || err3 != nil || err4 != nil:
		return fmt.Errorf("radio values must be numbers")
	case freq < 137 || freq > 1020:
		return fmt.Errorf("frequency %s MHz is outside what LoRa radios tune to", trim(freq))
	case !slices.Contains([]float64{7.8, 10.4, 15.6, 20.8, 31.25, 41.7, 62.5, 125, 250, 500}, bw):
		return fmt.Errorf("bandwidth %s kHz isn't a LoRa bandwidth", trim(bw))
	case sf < 5 || sf > 12:
		return fmt.Errorf("spreading factor is 5 to 12")
	case cr < 5 || cr > 8:
		return fmt.Errorf("coding rate is 5 to 8")
	}
	return nil
}

func trim(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func unit(u string) string {
	if u == "" {
		return ""
	}
	return " " + u
}

// ParseGet reads a console reply to `get key`: "> value", or an error the node sent back.
func ParseGet(reply string) (string, error) {
	reply = strings.TrimSpace(reply)
	if v, ok := strings.CutPrefix(reply, ">"); ok {
		return strings.TrimSpace(v), nil
	}
	return "", fmt.Errorf("%s", reply)
}

// ParseSet reads a console reply to `set key value`: anything starting OK succeeded.
func ParseSet(reply string) error {
	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "OK") {
		return nil
	}
	return fmt.Errorf("%s", reply)
}
