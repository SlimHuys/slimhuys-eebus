package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// DeviceInfo describes the remote EEBUS device in Home Assistant.
type DeviceInfo struct {
	SKI   string
	Brand string
	Model string
	Name  string
}

func (d DeviceInfo) shortSKI() string {
	if len(d.SKI) >= 8 {
		return d.SKI[:8]
	}
	return d.SKI
}

func (d DeviceInfo) displayName() string {
	switch {
	case d.Brand != "" && d.Model != "":
		return d.Brand + " " + d.Model
	case d.Model != "":
		return d.Model
	case d.Name != "":
		return d.Name
	}
	return "EEBUS device " + d.shortSKI()
}

// Topics keeps all topic names in one place.
type Topics struct {
	discovery string
	prefix    string
	ski8      string
}

func (t Topics) bridgeStatus() string { return t.prefix + "/status" }
func (t Topics) base() string         { return t.prefix + "/" + t.ski8 }
func (t Topics) deviceAvail() string  { return t.base() + "/availability" }
func (t Topics) connected() string    { return t.base() + "/connected" }
func (t Topics) lastUpdate() string   { return t.base() + "/last_update" }
func (t Topics) state(key string) string {
	return t.base() + "/" + key + "/state"
}
func (t Topics) attributes(key string) string {
	return t.base() + "/" + key + "/attributes"
}
func (t Topics) avail(key string) string {
	return t.base() + "/" + key + "/availability"
}
func (t Topics) config(component, key string) string {
	return fmt.Sprintf("%s/%s/%s_%s/%s/config", t.discovery, component, t.prefix, t.ski8, key)
}
func (t Topics) haStatus() string { return t.discovery + "/status" }

type discoveryDevice struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer,omitempty"`
	Model        string   `json:"model,omitempty"`
}

type discoveryOrigin struct {
	Name       string `json:"name"`
	SWVersion  string `json:"sw_version"`
	SupportURL string `json:"support_url"`
}

type availability struct {
	Topic string `json:"topic"`
}

type discoveryConfig struct {
	Name              string          `json:"name"`
	UniqueID          string          `json:"unique_id"`
	StateTopic        string          `json:"state_topic"`
	AttributesTopic   string          `json:"json_attributes_topic,omitempty"`
	Unit              string          `json:"unit_of_measurement,omitempty"`
	DeviceClass       string          `json:"device_class,omitempty"`
	StateClass        string          `json:"state_class,omitempty"`
	EntityCategory    string          `json:"entity_category,omitempty"`
	PayloadOn         string          `json:"payload_on,omitempty"`
	PayloadOff        string          `json:"payload_off,omitempty"`
	Availability      []availability  `json:"availability"`
	AvailabilityMode  string          `json:"availability_mode,omitempty"`
	Device            discoveryDevice `json:"device"`
	Origin            discoveryOrigin `json:"origin"`
	SuggestedDisplayP *int            `json:"suggested_display_precision,omitempty"`
}

func origin() discoveryOrigin {
	return discoveryOrigin{
		Name:       "SlimHuys EEBUS",
		SWVersion:  version,
		SupportURL: "https://github.com/SlimHuys/slimhuys-eebus",
	}
}

func deviceBlock(d DeviceInfo) discoveryDevice {
	return discoveryDevice{
		Identifiers:  []string{"slimhuys_eebus_" + d.SKI},
		Name:         d.displayName(),
		Manufacturer: d.Brand,
		Model:        d.Model,
	}
}

func measurementConfig(t Topics, d DeviceInfo, m *Measurement) discoveryConfig {
	return discoveryConfig{
		Name:            m.Name,
		UniqueID:        fmt.Sprintf("%s_%s_%s", t.prefix, d.SKI, m.Key),
		StateTopic:      t.state(m.Key),
		AttributesTopic: t.attributes(m.Key),
		Unit:            m.Unit,
		DeviceClass:     m.DeviceClass,
		StateClass:      m.StateClass,
		// All three must be online: the add-on runs, the device is connected
		// and this particular value is fresh.
		Availability: []availability{
			{Topic: t.bridgeStatus()},
			{Topic: t.deviceAvail()},
			{Topic: t.avail(m.Key)},
		},
		AvailabilityMode: "all",
		Device:           deviceBlock(d),
		Origin:           origin(),
	}
}

func connectedConfig(t Topics, d DeviceInfo) discoveryConfig {
	return discoveryConfig{
		Name:           "Connected",
		UniqueID:       fmt.Sprintf("%s_%s_connected", t.prefix, d.SKI),
		StateTopic:     t.connected(),
		DeviceClass:    "connectivity",
		EntityCategory: "diagnostic",
		PayloadOn:      "ON",
		PayloadOff:     "OFF",
		Availability:   []availability{{Topic: t.bridgeStatus()}},
		Device:         deviceBlock(d),
		Origin:         origin(),
	}
}

func lastUpdateConfig(t Topics, d DeviceInfo) discoveryConfig {
	return discoveryConfig{
		Name:           "Last update",
		UniqueID:       fmt.Sprintf("%s_%s_last_update", t.prefix, d.SKI),
		StateTopic:     t.lastUpdate(),
		DeviceClass:    "timestamp",
		EntityCategory: "diagnostic",
		Availability:   []availability{{Topic: t.bridgeStatus()}},
		Device:         deviceBlock(d),
		Origin:         origin(),
	}
}

// Publisher is the MQTT side of the bridge.
type Publisher struct {
	client paho.Client
	topics Topics

	mu        sync.Mutex
	device    DeviceInfo
	known     map[string]*Measurement
	fresh     map[string]bool
	connected bool
	online    bool
	lastData  time.Time
}

func NewPublisher(cfg Config, device DeviceInfo) *Publisher {
	p := &Publisher{
		topics: Topics{discovery: cfg.DiscoveryPrefix, prefix: cfg.TopicPrefix, ski8: device.shortSKI()},
		device: device,
		known:  map[string]*Measurement{},
		fresh:  map[string]bool{},
	}

	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", cfg.MQTTHost, cfg.MQTTPort)).
		SetClientID(fmt.Sprintf("%s_%s", cfg.TopicPrefix, device.shortSKI())).
		SetUsername(cfg.MQTTUsername).
		SetPassword(cfg.MQTTPassword).
		SetWill(p.topics.bridgeStatus(), "offline", 1, true).
		SetAutoReconnect(true).
		SetOnConnectHandler(p.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			log.Printf("MQTT connection lost: %v", err)
		})
	p.client = paho.NewClient(opts)
	return p
}

// Connect keeps trying until the broker accepts us. paho's own connect
// retry is silent, which hides a wrong host or password; this loop logs why.
func (p *Publisher) Connect(broker string) {
	go func() {
		for attempt := 1; ; attempt++ {
			tok := p.client.Connect()
			tok.Wait()
			if err := tok.Error(); err == nil {
				return
			} else if attempt == 1 || attempt%30 == 0 {
				log.Printf("MQTT: cannot connect to %s: %v (retrying every 10s)", broker, err)
			}
			time.Sleep(10 * time.Second)
		}
	}()
}

func (p *Publisher) Close() {
	p.publish(p.topics.bridgeStatus(), "offline", true)
	p.client.Disconnect(1000)
}

func (p *Publisher) onConnect(c paho.Client) {
	log.Printf("MQTT connected")
	c.Subscribe(p.topics.haStatus(), 0, func(_ paho.Client, msg paho.Message) {
		if string(msg.Payload()) == "online" {
			log.Printf("Home Assistant came online, re-announcing")
			p.republishAll()
		}
	})
	p.republishAll()
}

func (p *Publisher) publish(topic, payload string, retain bool) {
	if !p.client.IsConnected() {
		return
	}
	p.client.Publish(topic, 1, retain, payload)
}

func (p *Publisher) publishJSON(topic string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("marshal %s: %v", topic, err)
		return
	}
	p.publish(topic, string(b), true)
}

func onOff(b bool, on, off string) string {
	if b {
		return on
	}
	return off
}

// republishAll sends every discovery config and the current states. It is
// called after (re)connecting to the broker and when HA restarts.
func (p *Publisher) republishAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publish(p.topics.bridgeStatus(), "online", true)
	p.publishJSON(p.topics.config("binary_sensor", "connected"), connectedConfig(p.topics, p.device))
	p.publishJSON(p.topics.config("sensor", "last_update"), lastUpdateConfig(p.topics, p.device))
	p.publish(p.topics.connected(), onOff(p.connected, "ON", "OFF"), true)
	p.publish(p.topics.deviceAvail(), onOff(p.online, "online", "offline"), true)
	if !p.lastData.IsZero() {
		p.publish(p.topics.lastUpdate(), p.lastData.UTC().Format(time.RFC3339), true)
	}
	for _, m := range p.known {
		p.announceLocked(m)
		p.stateLocked(m)
	}
}

// SetDevice updates brand/model once the device announced itself.
func (p *Publisher) SetDevice(d DeviceInfo) {
	p.mu.Lock()
	changed := d.Brand != p.device.Brand || d.Model != p.device.Model || d.Name != p.device.Name
	p.device = d
	p.mu.Unlock()
	if changed {
		p.republishAll()
	}
}

func (p *Publisher) announceLocked(m *Measurement) {
	p.publishJSON(p.topics.config("sensor", m.Key), measurementConfig(p.topics, p.device, m))
}

func (p *Publisher) stateLocked(m *Measurement) {
	p.publish(p.topics.avail(m.Key), onOff(p.fresh[m.Key], "online", "offline"), true)
	if m.Updated.IsZero() {
		return
	}
	p.publish(p.topics.state(m.Key), formatValue(m.Value), true)
	p.publishJSON(p.topics.attributes(m.Key), map[string]any{
		"last_update":    m.Updated.UTC().Format(time.RFC3339),
		"source":         m.Source,
		"spine_entity":   m.Entity,
		"measurement_id": m.ID,
		"value_state":    m.ValueState,
	})
}

// Measurement publishes one (possibly new) measurement.
func (p *Publisher) Measurement(m Measurement) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := m
	_, seen := p.known[m.Key]
	p.known[m.Key] = &cp
	p.fresh[m.Key] = true
	if !seen {
		p.announceLocked(&cp)
	}
	p.stateLocked(&cp)
	if m.Updated.After(p.lastData) {
		p.lastData = m.Updated
		p.publish(p.topics.lastUpdate(), p.lastData.UTC().Format(time.RFC3339), true)
	}
}

// SetConnected reports the EEBUS connection state.
func (p *Publisher) SetConnected(connected bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.connected == connected {
		return
	}
	p.connected = connected
	p.publish(p.topics.connected(), onOff(connected, "ON", "OFF"), true)
}

// Refresh re-evaluates device and per-value freshness.
func (p *Publisher) Refresh(now time.Time, staleAfter time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	online := p.connected && !p.lastData.IsZero() && now.Sub(p.lastData) < staleAfter
	if online != p.online {
		p.online = online
		p.publish(p.topics.deviceAvail(), onOff(online, "online", "offline"), true)
	}
	for key, m := range p.known {
		fresh := now.Sub(m.Updated) < staleAfter
		if fresh != p.fresh[key] {
			p.fresh[key] = fresh
			p.publish(p.topics.avail(key), onOff(fresh, "online", "offline"), true)
			if !fresh {
				log.Printf("%s (%s) is stale: last update %s ago", m.Name, m.Key, now.Sub(m.Updated).Round(time.Second))
			}
		}
	}
}
