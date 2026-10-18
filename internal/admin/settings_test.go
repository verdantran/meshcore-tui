package admin

import (
	"strings"
	"testing"

	"github.com/verdantran/meshcore-tui/internal/radio"
)

func TestSchemaIsSound(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range schema {
		if s.Key == "" || s.Label == "" || s.Group == "" {
			t.Errorf("incomplete setting: %+v", s)
		}
		if seen[s.Key] {
			t.Errorf("duplicate key %s", s.Key)
		}
		seen[s.Key] = true
		if (s.Kind == KindInt || s.Kind == KindFloat) && (s.Min == nil || s.Max == nil) {
			t.Errorf("%s needs a range", s.Key)
		}
		if s.Kind == KindChoice && len(s.Options) == 0 {
			t.Errorf("%s needs options", s.Key)
		}
		// Every default must pass the setting's own checks, or the simulator would start invalid.
		if !s.ReadOnly && s.Default != "" {
			if err := s.Validate(s.Default); err != nil {
				t.Errorf("%s default %q: %v", s.Key, s.Default, err)
			}
		}
	}
	rep, room := Settings(radio.TypeRepeater), Settings(radio.TypeRoom)
	has := func(list []Setting, key string) bool {
		for _, s := range list {
			if s.Key == key {
				return true
			}
		}
		return false
	}
	if !has(rep, "repeat") || has(room, "repeat") || !has(room, "allow.read.only") || has(rep, "allow.read.only") {
		t.Error("role filtering")
	}
	if _, ok := Lookup("tx"); !ok {
		t.Error("lookup")
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("unknown key found")
	}
}

func TestValidate(t *testing.T) {
	get := func(k string) Setting { s, _ := Lookup(k); return s }
	cases := []struct {
		key, value string
		ok         bool
		msg        string
	}{
		{"tx", "22", true, ""},
		{"tx", "30", false, "between 1 and 22 dBm"},
		{"tx", "high", false, "whole number"},
		{"txdelay", "0.75", true, ""},
		{"txdelay", "x", false, "a number"},
		{"repeat", "on", true, ""},
		{"repeat", "yes", false, "on or off"},
		{"loop.detect", "strict", true, ""},
		{"loop.detect", "max", false, "one of off, minimal"},
		{"name", "  ", false, "can't be empty"},
		{"role", "room", false, "can't be changed"},
		{"radio", "869.618,62.5,8,8", true, ""},
		{"radio", "869.618,62.5,8", false, "frequency,bandwidth"},
		{"radio", "2400,62.5,8,8", false, "outside"},
		{"radio", "869.618,60,8,8", false, "isn't a LoRa bandwidth"},
		{"radio", "869.618,62.5,13,8", false, "spreading factor"},
		{"radio", "869.618,62.5,8,9", false, "coding rate"},
		{"radio", "a,b,c,d", false, "must be numbers"},
		{"guest.password", "", true, ""},
	}
	for _, c := range cases {
		err := get(c.key).Validate(c.value)
		if (err == nil) != c.ok || err != nil && !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s=%q: %v (want ok=%v %q)", c.key, c.value, err, c.ok, c.msg)
		}
	}
}

func TestWireValues(t *testing.T) {
	src, _ := Lookup("bridge.source")
	if src.Wire("logRx") != "rx" || src.FromWire("tx") != "logTx" || src.Wire("x") != "x" || src.FromWire("x") != "x" {
		t.Error("bridge.source is set with rx/tx but reads back as logRx/logTx")
	}
}

func TestParseReplies(t *testing.T) {
	if v, err := ParseGet("> 869.618,62.5,8,8"); err != nil || v != "869.618,62.5,8,8" {
		t.Errorf("get = %q %v", v, err)
	}
	if _, err := ParseGet("Unknown command"); err == nil || err.Error() != "Unknown command" {
		t.Errorf("get error = %v", err)
	}
	if ParseSet("OK - reboot to apply") != nil || ParseSet("Error: interval range is 3-168 hours") == nil {
		t.Error("set replies")
	}
}

func TestValidateRefusesNonFiniteNumbers(t *testing.T) {
	lat, _ := Lookup("lat")
	radioSt, _ := Lookup("radio")
	for _, v := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity"} {
		if lat.Validate(v) == nil {
			t.Errorf("lat %q accepted", v)
		}
		if radioSt.Validate(v+",62.5,8,8") == nil {
			t.Errorf("radio frequency %q accepted", v)
		}
	}
	if lat.Validate("51.45") != nil {
		t.Error("a real latitude is refused")
	}
}
