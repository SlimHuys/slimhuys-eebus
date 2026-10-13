package main

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

func (t Topics) dhwAvail() string      { return t.base() + "/hot_water/availability" }
func (t Topics) roomAvail() string     { return t.base() + "/heating/availability" }
func (t Topics) topic(s string) string { return t.base() + "/" + s }

// HA climate knows "heat", EEBUS calls the same thing "on".
var eebusToHAClimate = map[string]string{"auto": "auto", "on": "heat", "off": "off"}

func haToEEBUSClimate(mode string) (string, bool) {
	for k, v := range eebusToHAClimate {
		if v == mode {
			return k, true
		}
	}
	return "", false
}

func climateModes(eebus []string) []string {
	out := []string{}
	for _, m := range eebus {
		if ha, ok := eebusToHAClimate[m]; ok {
			out = append(out, ha)
		}
	}
	return out
}

func climatePrecision(step float64) float64 {
	for _, p := range []float64{0.1, 0.5, 1} {
		if step == p {
			return p
		}
	}
	return 0.1
}

// announceOnce publishes a discovery config only when it changed. When an
// entity switches component (e.g. a read-only select becomes a sensor), the
// old config is removed so Home Assistant does not keep a dead entity.
func (p *Publisher) announceOnce(component, key string, cfg discoveryConfig) {
	b, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	if old, ok := p.components[key]; ok && old != component {
		p.publish(p.topics.config(old, key), "", true)
	}
	topic := p.topics.config(component, key)
	if p.announced[topic] == string(b) {
		return
	}
	p.components[key] = component
	p.announced[topic] = string(b)
	p.publish(topic, string(b), true)
}

func (p *Publisher) groupAvailability(group string) []availability {
	return []availability{{Topic: p.topics.bridgeStatus()}, {Topic: p.topics.deviceAvail()}, {Topic: group}}
}

func (p *Publisher) baseConfig(name, key, group string) discoveryConfig {
	return discoveryConfig{
		Name:             name,
		UniqueID:         fmt.Sprintf("%s_%s_%s", p.topics.prefix, p.device.SKI, key),
		Availability:     p.groupAvailability(group),
		AvailabilityMode: "all",
		Device:           deviceBlock(p.device),
		Origin:           origin(),
	}
}

// HVAC publishes hot water and heating state.
func (p *Publisher) HVAC(dhw DHWState, room RoomState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dhw, p.room = &dhw, &room
	p.hvacLocked()
}

func (p *Publisher) hvacLocked() {
	if p.dhw != nil {
		p.dhwLocked(*p.dhw)
	}
	if p.room != nil {
		p.roomLocked(*p.room)
	}
}

func (p *Publisher) dhwLocked(st DHWState) {
	t := p.topics
	online := st.ModeKnown || st.BoostKnown || st.Setpoint != nil
	p.publish(t.dhwAvail(), onOff(online, "online", "offline"), true)
	if !online {
		return
	}

	if st.ModeKnown {
		c := p.baseConfig("Hot water mode", "hot_water_mode", t.dhwAvail())
		c.StateTopic, c.Icon = t.topic("hot_water_mode/state"), "mdi:water-boiler"
		component := "sensor"
		if st.ModeWritable {
			component, c.CommandTopic, c.Options = "select", t.topic("hot_water_mode/set"), st.Modes
		}
		p.announceOnce(component, "hot_water_mode", c)
		p.publish(c.StateTopic, st.Mode, true)
	}

	if st.BoostKnown {
		c := p.baseConfig("Hot water boost", "hot_water_boost", t.dhwAvail())
		c.StateTopic, c.Icon = t.topic("hot_water_boost/state"), "mdi:water-boiler-alert"
		c.PayloadOn, c.PayloadOff = "ON", "OFF"
		component := "binary_sensor"
		if st.BoostWritable {
			component, c.CommandTopic = "switch", t.topic("hot_water_boost/set")
		}
		p.announceOnce(component, "hot_water_boost", c)
		p.publish(c.StateTopic, onOff(st.BoostActive, "ON", "OFF"), true)

		if st.BoostStatus != "" {
			s := p.baseConfig("Hot water boost status", "hot_water_boost_status", t.dhwAvail())
			s.StateTopic, s.EntityCategory = t.topic("hot_water_boost_status/state"), "diagnostic"
			p.announceOnce("sensor", "hot_water_boost_status", s)
			p.publish(s.StateTopic, st.BoostStatus, true)
		}
	}

	if sp := st.Setpoint; sp != nil {
		c := p.baseConfig("Hot water setpoint", "hot_water_setpoint", t.dhwAvail())
		c.StateTopic, c.Unit, c.DeviceClass = t.topic("hot_water_setpoint/state"), "°C", "temperature"
		component := "sensor"
		if sp.Writable {
			min, max := sp.Min, sp.Max
			component, c.CommandTopic = "number", t.topic("hot_water_setpoint/set")
			c.Min, c.Max, c.Step, c.Mode = &min, &max, sp.Step, "box"
		}
		p.announceOnce(component, "hot_water_setpoint", c)
		p.publish(c.StateTopic, formatValue(sp.Value), true)
	}
}

func (p *Publisher) roomTemperatureTopic() string {
	for key := range p.known {
		if strings.Contains(key, "roomairtemperature") {
			return p.topics.state(key)
		}
	}
	return ""
}

func (p *Publisher) roomLocked(st RoomState) {
	t := p.topics
	modes := climateModes(st.Modes)
	online := (st.ModeKnown && len(modes) > 0) || st.SetpointKnown
	p.publish(t.roomAvail(), onOff(online, "online", "offline"), true)
	if !online {
		return
	}

	c := p.baseConfig("Heating", "heating", t.roomAvail())
	c.CurrentTemperatureTopic = p.roomTemperatureTopic()
	c.TemperatureUnit = "C"
	if st.ModeKnown && len(modes) > 0 {
		c.Modes, c.ModeStateTopic = modes, t.topic("heating/mode/state")
		if st.ModeWritable {
			c.ModeCommandTopic = t.topic("heating/mode/set")
		}
	} else {
		// HA requires at least one mode; without mode data show a heater
		// that is simply on.
		c.Modes = []string{"heat"}
	}
	if st.SetpointKnown {
		min, max := st.Min, st.Max
		c.TemperatureStateTopic, c.MinTemp, c.MaxTemp = t.topic("heating/temperature/state"), &min, &max
		c.TempStep, c.Precision = st.Step, climatePrecision(st.Step)
		if st.SetpointWritable {
			c.TemperatureCommandTopic = t.topic("heating/temperature/set")
		}
	}
	p.announceOnce("climate", "heating", c)
	if st.ModeKnown {
		if ha, ok := eebusToHAClimate[st.Mode]; ok {
			p.publish(t.topic("heating/mode/state"), ha, true)
		}
	}
	if st.SetpointKnown {
		p.publish(t.topic("heating/temperature/state"), formatValue(st.Setpoint), true)
	}
}

// OperatingState publishes the device's diagnosis state (normalOperation,
// failure, ...).
func (p *Publisher) OperatingState(state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opState = state
	p.opStateLocked()
}

func (p *Publisher) opStateLocked() {
	if p.opState == "" {
		return
	}
	c := discoveryConfig{
		Name:             "Operating state",
		UniqueID:         fmt.Sprintf("%s_%s_operating_state", p.topics.prefix, p.device.SKI),
		StateTopic:       p.topics.topic("operating_state/state"),
		EntityCategory:   "diagnostic",
		Icon:             "mdi:heat-pump-outline",
		Availability:     []availability{{Topic: p.topics.bridgeStatus()}, {Topic: p.topics.deviceAvail()}},
		AvailabilityMode: "all",
		Device:           deviceBlock(p.device),
		Origin:           origin(),
	}
	p.announceOnce("sensor", "operating_state", c)
	p.publish(c.StateTopic, p.opState, true)
}

// GridSent shows what was last offered to the device as grid power.
func (p *Publisher) GridSent(power float64, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := discoveryConfig{
		Name:            "Grid power sent to device",
		UniqueID:        fmt.Sprintf("%s_%s_grid_power_sent", p.topics.prefix, p.device.SKI),
		StateTopic:      p.topics.topic("grid_power_sent/state"),
		AttributesTopic: p.topics.topic("grid_power_sent/attributes"),
		Unit:            "W",
		DeviceClass:     "power",
		StateClass:      "measurement",
		EntityCategory:  "diagnostic",
		Availability:    []availability{{Topic: p.topics.bridgeStatus()}},
		Device:          deviceBlock(p.device),
		Origin:          origin(),
	}
	p.announceOnce("sensor", "grid_power_sent", c)
	p.publish(c.StateTopic, formatValue(power), true)
	p.publishJSON(c.AttributesTopic, map[string]any{"last_sent": at.UTC().Format(time.RFC3339)})
}

// subscribeHVAC wires the HVAC command topics to the handler.
func (p *Publisher) subscribeHVAC(c paho.Client) {
	t := p.topics
	str := func(msg paho.Message) string { return strings.TrimSpace(string(msg.Payload())) }
	num := func(msg paho.Message) (float64, bool) {
		v, err := strconv.ParseFloat(str(msg), 64)
		if err != nil {
			log.Printf("Ignoring %q on %s: not a number", msg.Payload(), msg.Topic())
		}
		return v, err == nil
	}

	c.Subscribe(t.topic("hot_water_mode/set"), 1, func(_ paho.Client, msg paho.Message) {
		mode := str(msg)
		p.mu.Lock()
		ok := p.dhw != nil && slices.Contains(p.dhw.Modes, mode)
		p.mu.Unlock()
		if !ok {
			log.Printf("Ignoring hot water mode %q: not offered by the device", mode)
			return
		}
		p.handler.SetDHWMode(mode)
	})
	c.Subscribe(t.topic("hot_water_boost/set"), 1, func(_ paho.Client, msg paho.Message) {
		switch strings.ToUpper(str(msg)) {
		case "ON":
			p.handler.SetDHWBoost(true)
		case "OFF":
			p.handler.SetDHWBoost(false)
		}
	})
	c.Subscribe(t.topic("hot_water_setpoint/set"), 1, func(_ paho.Client, msg paho.Message) {
		if v, ok := num(msg); ok {
			p.handler.SetDHWSetpoint(v)
		}
	})
	c.Subscribe(t.topic("heating/mode/set"), 1, func(_ paho.Client, msg paho.Message) {
		if mode, ok := haToEEBUSClimate(str(msg)); ok {
			p.handler.SetRoomMode(mode)
		} else {
			log.Printf("Ignoring heating mode %q", str(msg))
		}
	})
	c.Subscribe(t.topic("heating/temperature/set"), 1, func(_ paho.Client, msg paho.Message) {
		if v, ok := num(msg); ok {
			p.handler.SetRoomSetpoint(v)
		}
	})
}
