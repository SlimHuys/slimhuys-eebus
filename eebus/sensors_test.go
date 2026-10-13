package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"outsideAirTemperature": "Outside air temperature",
		"acPowerTotal":          "Ac power total",
		"dhwTemperature":        "Dhw temperature",
		"flowTemperature":       "Flow temperature",
		"DHWCircuit":            "DHW circuit",
		"power":                 "Power",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

// The four values the Vaillant VR940 actually reports (captured 2026-10-13).
func TestNewMeasurementVR940(t *testing.T) {
	cases := []struct {
		entity, addr string
		id           uint
		d            Description
		key, name    string
		unit, dc, sc string
	}{
		{"HeatPumpAppliance", "3", 1, Description{"power", "acPowerTotal", "W"},
			"heatpumpappliance_3_acpowertotal_1", "Power", "W", "power", "measurement"},
		{"DHWCircuit", "4", 1, Description{"temperature", "dhwTemperature", "degC"},
			"dhwcircuit_4_dhwtemperature_1", "Hot water temperature", "°C", "temperature", "measurement"},
		{"HVACRoom", "5:1:1", 1, Description{"temperature", "roomAirTemperature", "degC"},
			"hvacroom_5_1_1_roomairtemperature_1", "Room temperature", "°C", "temperature", "measurement"},
		{"TemperatureSensor", "6", 1, Description{"temperature", "outsideAirTemperature", "degC"},
			"temperaturesensor_6_outsideairtemperature_1", "Outdoor temperature", "°C", "temperature", "measurement"},
		{"HeatPumpAppliance", "3", 7, Description{"energy", "acEnergyConsumed", "Wh"},
			"heatpumpappliance_3_acenergyconsumed_7", "Energy consumed", "Wh", "energy", "total_increasing"},
	}
	for _, c := range cases {
		m := newMeasurement(c.entity, c.addr, c.id, c.d)
		if m.Key != c.key || m.Name != c.name || m.Unit != c.unit || m.DeviceClass != c.dc || m.StateClass != c.sc {
			t.Errorf("%s/%s: got key=%q name=%q unit=%q dc=%q sc=%q", c.entity, c.d.Scope, m.Key, m.Name, m.Unit, m.DeviceClass, m.StateClass)
		}
	}
}

func TestNewMeasurementWithoutScopeOrUnit(t *testing.T) {
	m := newMeasurement("Compressor", "3:1", 4, Description{MeasurementType: "percentage", Unit: "pct"})
	if m.Name != "Percentage" || m.Unit != "%" || m.DeviceClass != "" || m.StateClass != "measurement" {
		t.Errorf("got %+v", m)
	}
	m = newMeasurement("Compressor", "3:1", 5, Description{})
	if m.Name != "Measurement 5" {
		t.Errorf("got name %q", m.Name)
	}
}

func TestDiscoveryConfig(t *testing.T) {
	topics := Topics{discovery: "homeassistant", prefix: "slimhuys_eebus", ski8: "5859fe43"}
	dev := DeviceInfo{SKI: "5859fe434472f43bfd73ee53122a60ab25626c04", Brand: "Vaillant", Model: "VR940"}
	m := newMeasurement("HeatPumpAppliance", "3", 1, Description{"power", "acPowerTotal", "W"})

	if got, want := topics.config("sensor", m.Key), "homeassistant/sensor/slimhuys_eebus_5859fe43/heatpumpappliance_3_acpowertotal_1/config"; got != want {
		t.Fatalf("config topic %q, want %q", got, want)
	}

	b, err := json.Marshal(measurementConfig(topics, dev, m))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(b, &got)

	if got["unique_id"] != "slimhuys_eebus_5859fe434472f43bfd73ee53122a60ab25626c04_heatpumpappliance_3_acpowertotal_1" {
		t.Errorf("unique_id = %v", got["unique_id"])
	}
	if got["state_topic"] != "slimhuys_eebus/5859fe43/heatpumpappliance_3_acpowertotal_1/state" {
		t.Errorf("state_topic = %v", got["state_topic"])
	}
	if got["availability_mode"] != "all" || len(got["availability"].([]any)) != 3 {
		t.Errorf("availability = %v / %v", got["availability_mode"], got["availability"])
	}
	device := got["device"].(map[string]any)
	if device["name"] != "Vaillant VR940" || device["manufacturer"] != "Vaillant" {
		t.Errorf("device = %v", device)
	}
	if strings.Contains(string(b), `"entity_category"`) {
		t.Errorf("measurement must not be a diagnostic entity: %s", b)
	}
}

func TestDisplayNameFallback(t *testing.T) {
	if got := (DeviceInfo{SKI: "5859fe434472"}).displayName(); got != "EEBUS device 5859fe43" {
		t.Errorf("got %q", got)
	}
}

func TestPublisherFreshness(t *testing.T) {
	// No broker: publish is a no-op when not connected, but state tracking
	// must still work.
	p := NewPublisher(Config{MQTTHost: "127.0.0.1", MQTTPort: 1, TopicPrefix: "x", DiscoveryPrefix: "homeassistant"}, DeviceInfo{SKI: "abcdef0123"}, nil)
	now := time.Now()
	m := newMeasurement("HeatPumpAppliance", "3", 1, Description{"power", "acPowerTotal", "W"})
	m.Value, m.Updated = 360, now.Add(-30*time.Minute)
	p.Measurement(*m)
	p.SetConnected(true)

	p.Refresh(now, 12*time.Minute)
	if p.fresh[m.Key] {
		t.Error("a value from 30 minutes ago must be stale")
	}
	if p.online {
		t.Error("device must be offline when its newest value is stale")
	}

	m.Updated = now
	p.Measurement(*m)
	p.Refresh(now, 12*time.Minute)
	if !p.fresh[m.Key] || !p.online {
		t.Errorf("fresh value: fresh=%v online=%v", p.fresh[m.Key], p.online)
	}

	p.SetConnected(false)
	p.Refresh(now, 12*time.Minute)
	if p.online {
		t.Error("device must be offline when EEBUS is disconnected")
	}
}

func TestLPCConfigs(t *testing.T) {
	topics := Topics{discovery: "homeassistant", prefix: "slimhuys_eebus", ski8: "5859fe43"}
	dev := DeviceInfo{SKI: "5859fe434472f43bfd73ee53122a60ab25626c04", Brand: "Vaillant", Model: "VR940"}
	cfgs := lpcConfigs(topics, dev, 4200)

	b, _ := json.Marshal(cfgs["number/power_limit"])
	var num map[string]any
	_ = json.Unmarshal(b, &num)
	// min 0 must be sent explicitly: HA's default minimum for an MQTT number is 1
	if num["min"] != 0.0 || num["max"] != 4200.0 || num["command_topic"] != "slimhuys_eebus/5859fe43/power_limit/set" {
		t.Errorf("number config: %s", b)
	}

	b, _ = json.Marshal(cfgs["switch/power_limit_active"])
	var sw map[string]any
	_ = json.Unmarshal(b, &sw)
	if sw["payload_on"] != "ON" || sw["state_topic"] != "slimhuys_eebus/5859fe43/power_limit_active/state" {
		t.Errorf("switch config: %s", b)
	}
	if len(cfgs) != 4 {
		t.Errorf("want 4 entities, got %d", len(cfgs))
	}
}
