package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Measurement is one value the device publishes through the SPINE
// Measurement feature, enriched with what Home Assistant needs to show it.
type Measurement struct {
	Key         string // stable, unique per device: <entitytype>_<address>_<scope>_<id>
	Name        string
	Unit        string
	DeviceClass string
	StateClass  string

	Entity     string // e.g. "HeatPumpAppliance(3)"
	ID         uint
	Value      float64
	ValueState string
	Source     string // "notify" (pushed by the device) or "reply" (answer to our poll)
	Updated    time.Time
}

// Description is what the device tells us about a measurement id.
type Description struct {
	MeasurementType string
	Scope           string
	Unit            string
}

// nameOverrides gives the common scopes a friendlier name than the
// generated one. Anything not listed here still gets a readable name.
var nameOverrides = map[string]string{
	"acPowerTotal":          "Power",
	"acEnergyConsumed":      "Energy consumed",
	"acEnergyProduced":      "Energy produced",
	"dhwTemperature":        "Hot water temperature",
	"roomAirTemperature":    "Room temperature",
	"outsideAirTemperature": "Outdoor temperature",
}

var unitMap = map[string]string{
	"W":    "W",
	"kW":   "kW",
	"Wh":   "Wh",
	"kWh":  "kWh",
	"degC": "°C",
	"K":    "K",
	"A":    "A",
	"V":    "V",
	"Hz":   "Hz",
	"pct":  "%",
	"bar":  "bar",
	"l/h":  "L/h",
}

func haUnit(spineUnit string) string {
	if u, ok := unitMap[spineUnit]; ok {
		return u
	}
	return spineUnit
}

func deviceClassFor(unit string) string {
	switch unit {
	case "W", "kW":
		return "power"
	case "Wh", "kWh":
		return "energy"
	case "°C", "K":
		return "temperature"
	case "A":
		return "current"
	case "V":
		return "voltage"
	case "Hz":
		return "frequency"
	case "bar":
		return "pressure"
	}
	return ""
}

func stateClassFor(deviceClass string) string {
	if deviceClass == "energy" {
		return "total_increasing"
	}
	return "measurement"
}

// humanize turns a SPINE identifier like "outsideAirTemperature" into
// "Outside air temperature".
func humanize(s string) string {
	if s == "" {
		return ""
	}
	var words []string
	var cur []rune
	runes := []rune(s)
	for i, r := range runes {
		boundary := i > 0 && unicode.IsUpper(r) &&
			(unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1])))
		if boundary && len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
		cur = append(cur, r)
	}
	words = append(words, string(cur))
	for i, w := range words {
		// keep acronyms such as "DHW" and "AC" intact
		if strings.ToUpper(w) == w && len(w) > 1 {
			continue
		}
		if i == 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		} else {
			words[i] = strings.ToLower(w)
		}
	}
	return strings.Join(words, " ")
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ':' || r == '_' || r == ' ' || r == '-':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// newMeasurement derives the Home Assistant metadata for a measurement.
// entityType is the SPINE entity type ("HeatPumpAppliance"), address its
// entity address ("3" or "5:1:1").
func newMeasurement(entityType, address string, id uint, d Description) *Measurement {
	label := d.Scope
	if label == "" {
		label = d.MeasurementType
	}
	name := nameOverrides[label]
	if name == "" {
		name = humanize(label)
	}
	if name == "" {
		name = fmt.Sprintf("Measurement %d", id)
	}

	unit := haUnit(d.Unit)
	dc := deviceClassFor(unit)

	return &Measurement{
		Key:         slug(fmt.Sprintf("%s_%s_%s_%d", entityType, address, label, id)),
		Name:        name,
		Unit:        unit,
		DeviceClass: dc,
		StateClass:  stateClassFor(dc),
		Entity:      fmt.Sprintf("%s(%s)", entityType, address),
		ID:          id,
	}
}

// valid reports whether the device vouches for the value. The VR940 sends
// e.g. a room temperature of 0 with valueState "error" right after its EEBUS
// service restarts; that must not show up as a real 0 °C.
func (m *Measurement) valid() bool {
	return m.ValueState == "" || m.ValueState == "normal"
}

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
