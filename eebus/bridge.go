package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/features/client"
	"github.com/enbility/eebus-go/service"
	shipapi "github.com/enbility/ship-go/api"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

const (
	// Right after connecting, poll quickly until the first values arrive;
	// detailed discovery of the remote entities takes a few seconds.
	initialPollEvery = 15 * time.Second
	tickEvery        = 15 * time.Second
	statsEvery       = time.Hour
	// How often a stale connection is torn down before the process gives up
	// and lets the Supervisor restart it.
	maxReconnects = 3
)

type pendingValue struct {
	value      float64
	valueState string
	source     string
	at         time.Time
}

// Bridge connects one EEBUS device to MQTT.
type Bridge struct {
	cfg   Config
	svc   *service.Service
	local spineapi.EntityLocalInterface

	mu            sync.Mutex
	ski           string
	device        DeviceInfo
	pub           *Publisher
	connected     bool
	connectedAt   time.Time
	lastData      time.Time
	lastPoll      time.Time
	reconnects    int
	lastRejectLog time.Time
	// counters for the hourly "still alive" log line
	statValues, statPushed int
	statSince              time.Time
	descs                  map[string]Description           // "<address>/<id>"
	pending                map[string]pendingValue          // values that arrived before their description
	measures               map[string]*Measurement          // "<address>/<id>"
	descAsked              map[string]time.Time             // "<address>" -> last description request
	candidates             map[string]shipapi.RemoteService // discovered devices, by SKI
}

func NewBridge(cfg Config) *Bridge {
	return &Bridge{
		cfg:        cfg,
		ski:        cfg.DeviceSKI,
		device:     DeviceInfo{SKI: cfg.DeviceSKI},
		descs:      map[string]Description{},
		pending:    map[string]pendingValue{},
		measures:   map[string]*Measurement{},
		descAsked:  map[string]time.Time{},
		candidates: map[string]shipapi.RemoteService{},
	}
}

func entityAddress(e spineapi.EntityRemoteInterface) string {
	if e == nil || e.Address() == nil {
		return ""
	}
	parts := make([]string, 0, len(e.Address().Entity))
	for _, a := range e.Address().Entity {
		parts = append(parts, fmt.Sprint(uint(a)))
	}
	return strings.Join(parts, ":")
}

// useSKI fixes the device we talk to and brings up MQTT for it.
func (b *Bridge) useSKI(ski string) {
	b.mu.Lock()
	if b.pub != nil {
		b.mu.Unlock()
		return
	}
	b.ski = ski
	b.device.SKI = ski
	if c, ok := b.candidates[ski]; ok {
		b.device.Brand, b.device.Model, b.device.Name = c.Brand, c.Model, c.Name
	}
	b.pub = NewPublisher(b.cfg, b.device)
	pub := b.pub
	b.mu.Unlock()

	log.Printf("Using EEBUS device %s (%s)", ski, b.device.displayName())
	broker := fmt.Sprintf("%s:%d", b.cfg.MQTTHost, b.cfg.MQTTPort)
	log.Printf("MQTT broker: %s (user %q)", broker, b.cfg.MQTTUsername)
	pub.Connect(broker)
	b.svc.RegisterRemoteSKI(ski)
}

func (b *Bridge) publisher() *Publisher {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pub
}

// ---- SPINE events ----

func (b *Bridge) HandleEvent(ev spineapi.EventPayload) {
	if ev.EventType != spineapi.EventTypeDataChange || ev.Entity == nil {
		return
	}
	b.mu.Lock()
	target := b.ski
	b.mu.Unlock()
	if target == "" || normalizeSKI(ev.Ski) != target {
		return
	}

	source := "?"
	if ev.CmdClassifier != nil {
		source = string(*ev.CmdClassifier)
	}

	switch d := ev.Data.(type) {
	case *model.MeasurementDescriptionListDataType:
		b.onDescriptions(ev.Entity, d)
	case *model.MeasurementListDataType:
		b.onValues(ev.Entity, d, source)
	}
}

func (b *Bridge) onDescriptions(e spineapi.EntityRemoteInterface, d *model.MeasurementDescriptionListDataType) {
	addr := entityAddress(e)
	b.mu.Lock()
	var flush []Measurement
	for _, item := range d.MeasurementDescriptionData {
		if item.MeasurementId == nil {
			continue
		}
		desc := Description{}
		if item.MeasurementType != nil {
			desc.MeasurementType = string(*item.MeasurementType)
		}
		if item.ScopeType != nil {
			desc.Scope = string(*item.ScopeType)
		}
		if item.Unit != nil {
			desc.Unit = string(*item.Unit)
		}
		key := fmt.Sprintf("%s/%d", addr, *item.MeasurementId)
		b.descs[key] = desc
		if pv, ok := b.pending[key]; ok {
			delete(b.pending, key)
			if m := b.updateLocked(e, addr, uint(*item.MeasurementId), pv); m != nil {
				flush = append(flush, *m)
			}
		}
	}
	pub := b.pub
	b.mu.Unlock()

	if b.cfg.Debug {
		log.Printf("descriptions from %s(%s): %d", e.EntityType(), addr, len(d.MeasurementDescriptionData))
	}
	if pub != nil {
		for _, m := range flush {
			pub.Measurement(m)
		}
	}
}

func (b *Bridge) onValues(e spineapi.EntityRemoteInterface, d *model.MeasurementListDataType, source string) {
	addr := entityAddress(e)
	now := time.Now()
	var out []Measurement
	askDescriptions := false

	b.mu.Lock()
	for _, item := range d.MeasurementData {
		if item.MeasurementId == nil || item.Value == nil {
			continue
		}
		pv := pendingValue{value: item.Value.GetValue(), source: source, at: now}
		if item.ValueState != nil {
			pv.valueState = string(*item.ValueState)
		}
		id := uint(*item.MeasurementId)
		key := fmt.Sprintf("%s/%d", addr, id)
		if _, ok := b.descs[key]; !ok {
			b.pending[key] = pv
			if now.Sub(b.descAsked[addr]) > time.Minute {
				b.descAsked[addr] = now
				askDescriptions = true
			}
			continue
		}
		if m := b.updateLocked(e, addr, id, pv); m != nil {
			out = append(out, *m)
		}
	}
	pub := b.pub
	b.mu.Unlock()

	if askDescriptions {
		if m, err := client.NewMeasurement(b.local, e); err == nil {
			_, _ = m.RequestDescriptions(nil, nil)
		}
	}
	for _, m := range out {
		if b.cfg.Debug {
			log.Printf("%-6s %s %s = %s %s", m.Source, m.Entity, m.Name, formatValue(m.Value), m.Unit)
		}
		if pub != nil {
			pub.Measurement(m)
		}
	}
}

// updateLocked stores a value for which the description is known.
func (b *Bridge) updateLocked(e spineapi.EntityRemoteInterface, addr string, id uint, pv pendingValue) *Measurement {
	key := fmt.Sprintf("%s/%d", addr, id)
	m, ok := b.measures[key]
	if !ok {
		m = newMeasurement(string(e.EntityType()), addr, id, b.descs[key])
		b.measures[key] = m
		log.Printf("New sensor: %s (%s, %s)", m.Name, m.Entity, m.Unit)
	}
	m.Value = pv.value
	m.ValueState = pv.valueState
	m.Source = pv.source
	m.Updated = pv.at
	b.lastData = pv.at
	b.reconnects = 0
	b.statValues++
	if pv.source == "notify" {
		b.statPushed++
	}
	cp := *m
	return &cp
}

// ---- polling and watchdog ----

func (b *Bridge) poll() {
	b.mu.Lock()
	ski := b.ski
	b.lastPoll = time.Now()
	b.mu.Unlock()

	dev := b.svc.LocalDevice().RemoteDeviceForSki(ski)
	if dev == nil {
		return
	}
	for _, e := range dev.Entities() {
		if e.FeatureOfTypeAndRole(model.FeatureTypeTypeMeasurement, model.RoleTypeServer) == nil {
			continue
		}
		m, err := client.NewMeasurement(b.local, e)
		if err != nil {
			continue
		}
		if !m.HasSubscription() {
			if _, err := m.Subscribe(); err != nil && b.cfg.Debug {
				log.Printf("subscribe %s: %v", e.EntityType(), err)
			}
			_, _ = m.RequestDescriptions(nil, nil)
		}
		if _, err := m.RequestData(nil, nil); err != nil && b.cfg.Debug {
			log.Printf("request %s: %v", e.EntityType(), err)
		}
	}
}

func (b *Bridge) loop() {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for now := range t.C {
		b.mu.Lock()
		connected := b.connected
		gotData := !b.lastData.IsZero() && b.lastData.After(b.connectedAt)
		sincePoll := now.Sub(b.lastPoll)
		staleFor := now.Sub(b.lastData)
		if !gotData {
			staleFor = now.Sub(b.connectedAt)
		}
		ski := b.ski
		pub := b.pub
		var stats string
		if b.statSince.IsZero() {
			b.statSince = now
		} else if now.Sub(b.statSince) >= statsEvery {
			// A quiet log must not look the same as a stuck bridge.
			stats = fmt.Sprintf("Last hour: %d values received (%d pushed by the device), connected: %v",
				b.statValues, b.statPushed, connected)
			b.statValues, b.statPushed, b.statSince = 0, 0, now
		}
		b.mu.Unlock()

		if stats != "" {
			log.Print(stats)
		}
		if pub != nil {
			pub.Refresh(now, b.cfg.staleAfter())
		}
		if !connected {
			continue
		}

		if sincePoll >= b.cfg.pollInterval() || (!gotData && sincePoll >= initialPollEvery) {
			b.poll()
		}

		if staleFor > b.cfg.staleAfter() {
			b.mu.Lock()
			b.reconnects++
			n := b.reconnects
			b.connectedAt = now
			b.mu.Unlock()
			if n > maxReconnects {
				log.Printf("No data after %d reconnects, exiting so the Supervisor restarts the add-on", maxReconnects)
				os.Exit(1)
			}
			log.Printf("No data for %s, reconnecting (attempt %d/%d)", staleFor.Round(time.Second), n, maxReconnects)
			b.svc.DisconnectSKI(ski, "stale data")
		}
	}
}

// ---- api.ServiceReaderInterface ----

func (b *Bridge) RemoteSKIConnected(_ api.ServiceInterface, ski string) {
	if normalizeSKI(ski) != b.currentSKI() {
		return
	}
	log.Printf("Connected to %s", ski)
	b.mu.Lock()
	b.connected = true
	b.connectedAt = time.Now()
	b.lastPoll = time.Time{}
	pub := b.pub
	b.mu.Unlock()
	if pub != nil {
		pub.SetConnected(true)
	}
}

func (b *Bridge) RemoteSKIDisconnected(_ api.ServiceInterface, ski string) {
	if normalizeSKI(ski) != b.currentSKI() {
		return
	}
	b.mu.Lock()
	was := b.connected
	b.connected = false
	pub := b.pub
	b.mu.Unlock()
	if was {
		log.Printf("Disconnected from %s", ski)
	}
	if pub != nil {
		pub.SetConnected(false)
	}
}

func (b *Bridge) currentSKI() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ski
}

func (b *Bridge) VisibleRemoteServicesUpdated(_ api.ServiceInterface, entries []shipapi.RemoteService) {
	b.mu.Lock()
	for _, e := range entries {
		ski := normalizeSKI(e.Ski)
		if _, seen := b.candidates[ski]; !seen {
			log.Printf("Found EEBUS service: %s %s (%s) ski=%s", e.Brand, e.Model, e.Type, ski)
		}
		b.candidates[ski] = e
	}
	target := b.ski
	pub := b.pub
	var info *DeviceInfo
	if c, ok := b.candidates[target]; ok && target != "" {
		b.device.Brand, b.device.Model, b.device.Name = c.Brand, c.Model, c.Name
		d := b.device
		info = &d
	}
	b.mu.Unlock()

	if target != "" {
		if pub != nil && info != nil {
			pub.SetDevice(*info)
		}
		return
	}

	// No device_ski configured: only pick a device when the choice is
	// unambiguous. Energy managers (other bridges, evcc, ...) are skipped.
	var picks []shipapi.RemoteService
	for _, e := range entries {
		if e.Type == string(model.DeviceTypeTypeEnergyManagementSystem) {
			continue
		}
		picks = append(picks, e)
	}
	switch len(picks) {
	case 1:
		b.useSKI(normalizeSKI(picks[0].Ski))
	case 0:
	default:
		log.Printf("Found %d EEBUS devices; set device_ski to choose one", len(picks))
	}
}

func (b *Bridge) ServiceShipIDUpdate(string, string) {}

func (b *Bridge) ServicePairingDetailUpdate(ski string, d *shipapi.ConnectionStateDetail) {
	if normalizeSKI(ski) != b.currentSKI() {
		return
	}
	switch d.State() {
	case shipapi.ConnectionStateRemoteDeniedTrust:
		log.Printf("%s denied trust: trust this bridge (SKI %s) in the manufacturer app", ski, b.svc.LocalService().SKI())
	case shipapi.ConnectionStateError:
		if d.Error() != nil && strings.Contains(d.Error().Error(), "4452") {
			// The device retries every ~20 s while untrusted; say it once
			// every few minutes instead of flooding the log.
			b.mu.Lock()
			quiet := time.Since(b.lastRejectLog) < 5*time.Minute
			if !quiet {
				b.lastRejectLog = time.Now()
			}
			b.mu.Unlock()
			if !quiet {
				log.Printf("%s rejected the connection. Either this bridge is not trusted yet (SKI %s), or another EEBUS manager is connected; most devices accept only one.", ski, b.svc.LocalService().SKI())
			}
		} else if b.cfg.Debug {
			log.Printf("connection error with %s: %v", ski, d.Error())
		}
	}
}

func (b *Bridge) AllowWaitingForTrust(ski string) bool {
	return normalizeSKI(ski) == b.currentSKI()
}
