package main

import (
	"encoding/json"
	"testing"
)

func TestClimateModeMapping(t *testing.T) {
	got := climateModes([]string{"auto", "on", "off", "eco"})
	want := []string{"auto", "heat", "off"} // eco has no HA climate mode
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if m, ok := haToEEBUSClimate("heat"); !ok || m != "on" {
		t.Errorf("heat -> %q %v, want on", m, ok)
	}
	if _, ok := haToEEBUSClimate("cool"); ok {
		t.Error("cool must not map to anything")
	}
}

func TestValidSetpoint(t *testing.T) {
	cases := []struct {
		v  float64
		ok bool
	}{{55, true}, {35, true}, {70, true}, {34, false}, {71, false}, {55.5, false}}
	for _, c := range cases {
		err := validSetpoint(c.v, 35, 70, 1)
		if (err == nil) != c.ok {
			t.Errorf("validSetpoint(%g) err=%v, want ok=%v", c.v, err, c.ok)
		}
	}
	if err := validSetpoint(20.5, 5, 30, 0.5); err != nil {
		t.Errorf("20.5 with step 0.5: %v", err)
	}
}

func newTestPublisher() *Publisher {
	return NewPublisher(Config{MQTTHost: "127.0.0.1", MQTTPort: 1, TopicPrefix: "slimhuys_eebus", DiscoveryPrefix: "homeassistant"},
		DeviceInfo{SKI: "5859fe434472f43bfd73ee53122a60ab25626c04", Brand: "Vaillant", Model: "VR940"}, nil)
}

func TestAnnounceOnceTracksComponent(t *testing.T) {
	p := newTestPublisher()
	c := p.baseConfig("Hot water mode", "hot_water_mode", p.topics.dhwAvail())
	p.announceOnce("sensor", "hot_water_mode", c)
	p.announceOnce("select", "hot_water_mode", c)
	if p.components["hot_water_mode"] != "select" {
		t.Errorf("component = %q, want select", p.components["hot_water_mode"])
	}
	if _, ok := p.announced["homeassistant/select/slimhuys_eebus_5859fe43/hot_water_mode/config"]; !ok {
		t.Error("select config not recorded")
	}
}

func TestClimateConfig(t *testing.T) {
	p := newTestPublisher()
	room := RoomState{ModeKnown: true, Mode: "auto", Modes: []string{"auto", "on", "off"}, ModeWritable: true,
		SetpointKnown: true, Setpoint: 21, Min: 5, Max: 30, Step: 0.5, SetpointWritable: true}
	p.HVAC(DHWState{}, room)
	raw := p.announced["homeassistant/climate/slimhuys_eebus_5859fe43/heating/config"]
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("climate config not announced: %v (%q)", err, raw)
	}
	if cfg["mode_command_topic"] != "slimhuys_eebus/5859fe43/heating/mode/set" ||
		cfg["temperature_command_topic"] != "slimhuys_eebus/5859fe43/heating/temperature/set" ||
		cfg["min_temp"] != 5.0 || cfg["max_temp"] != 30.0 || cfg["precision"] != 0.5 {
		t.Errorf("climate config: %s", raw)
	}
}

func TestReadOnlyDHWBecomesSensor(t *testing.T) {
	p := newTestPublisher()
	p.HVAC(DHWState{ModeKnown: true, Mode: "auto", Modes: []string{"auto", "on", "off"}, ModeWritable: false}, RoomState{})
	if p.components["hot_water_mode"] != "sensor" {
		t.Errorf("read-only mode must be a sensor, got %q", p.components["hot_water_mode"])
	}
}
