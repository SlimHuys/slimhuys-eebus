package main

// Grid connection point data for the device (EEBUS MGCP, provider side).
//
// The VR940 can use the grid power at the connection point for Vaillant's own
// energy management: charging the hot water and buffer cylinders with solar
// surplus. We read the grid meter from Home Assistant and offer it as a
// GridConnectionPointOfPremises entity. Whether the heat pump actually acts
// on it is decided by the device (and the "Energy management" sliders in the
// myVAILLANT app), so this is experimental.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/enbility/eebus-go/api"
	ucapi "github.com/enbility/eebus-go/usecases/api"
	gcpmgcp "github.com/enbility/eebus-go/usecases/gcp/mgcp"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
	"github.com/enbility/spine-go/util"
)

// Sending every 5 s (0.3.0–0.3.5) coincided with the VR940's EEBUS side
// slowly degrading and then refusing sessions; with grid data off it stayed
// healthy. Send rarely and only on a real change.
const (
	gridEvery    = 30 * time.Second
	gridMinDelta = 50.0            // W
	gridMaxQuiet = 5 * time.Minute // send at least this often
)

// haReader reads entity states from Home Assistant: through the Supervisor
// proxy inside the add-on, or HA_URL/HA_TOKEN when running locally.
type haReader struct {
	base, token string
	client      *http.Client
}

func newHAReader() (*haReader, error) {
	if t := os.Getenv("SUPERVISOR_TOKEN"); t != "" {
		return &haReader{base: "http://supervisor/core/api", token: t, client: &http.Client{Timeout: 5 * time.Second}}, nil
	}
	if u, t := os.Getenv("HA_URL"), os.Getenv("HA_TOKEN"); u != "" && t != "" {
		return &haReader{base: strings.TrimRight(u, "/") + "/api", token: t, client: &http.Client{Timeout: 5 * time.Second}}, nil
	}
	return nil, errors.New("no Home Assistant API available (SUPERVISOR_TOKEN, or HA_URL and HA_TOKEN)")
}

// value returns the state as a number, scaled to the base unit (W or Wh).
func (h *haReader) value(entity string) (float64, error) {
	req, err := http.NewRequest(http.MethodGet, h.base+"/states/"+entity, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", entity, resp.Status)
	}
	var s struct {
		State      string `json:"state"`
		Attributes struct {
			Unit string `json:"unit_of_measurement"`
		} `json:"attributes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(s.State, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is %q", entity, s.State)
	}
	return v * unitFactor(s.Attributes.Unit), nil
}

func unitFactor(unit string) float64 {
	switch unit {
	case "kW", "kWh":
		return 1000
	case "MW", "MWh":
		return 1e6
	}
	return 1
}

type grid struct {
	uc *gcpmgcp.MGCP
	ha *haReader
}

func (b *Bridge) setupGrid() {
	if b.cfg.GridPowerEntity == "" {
		return
	}
	ha, err := newHAReader()
	if err != nil {
		log.Printf("Grid data disabled: %v", err)
		return
	}
	local := b.svc.LocalDevice().EntityForType(model.EntityTypeTypeGridConnectionPointOfPremises)
	if local == nil {
		log.Printf("Grid data disabled: no grid connection point entity")
		return
	}
	measured := util.Ptr(model.MeasurementValueSourceTypeMeasuredValue)
	uc, err := gcpmgcp.NewMGCP(local,
		func(ski string, _ spineapi.DeviceRemoteInterface, _ spineapi.EntityRemoteInterface, event api.EventType) {
			if normalizeSKI(ski) == b.currentSKI() && event == gcpmgcp.UseCaseSupportUpdate {
				log.Printf("The device announced interest in our grid data")
			}
		},
		nil,
		&gcpmgcp.MonitorPowerConfig{ValueSource: measured},
		&gcpmgcp.MonitorEnergyConfig{ValueSourceProduction: measured, ValueSourceConsumption: measured},
		nil, nil, nil,
	)
	if err != nil {
		log.Printf("Grid data disabled: %v", err)
		return
	}
	if err := b.svc.AddUseCase(uc); err != nil {
		log.Printf("Grid data disabled: %v", err)
		return
	}
	b.grid = &grid{uc: uc, ha: ha}
	log.Printf("Offering grid data from %s to the device (experimental)", b.cfg.GridPowerEntity)
	go b.gridLoop()
}

func (b *Bridge) gridLoop() {
	t := time.NewTicker(gridEvery)
	defer t.Stop()
	failing := false
	lastPower, lastSent := math.Inf(1), time.Time{}
	for range t.C {
		power, err := b.grid.ha.value(b.cfg.GridPowerEntity)
		if err != nil {
			if !failing {
				log.Printf("Grid data: %v", err)
				failing = true
			}
			continue
		}
		if failing {
			log.Printf("Grid data: reading %s again", b.cfg.GridPowerEntity)
			failing = false
		}

		now := time.Now()
		if math.Abs(power-lastPower) < gridMinDelta && now.Sub(lastSent) < gridMaxQuiet {
			continue
		}
		lastPower, lastSent = power, now
		u := b.grid.uc
		updates := []ucapi.UpdateData{u.UpdateDataPowerTotal(power, &now, nil)}
		if e := b.cfg.GridEnergyConsumedEntity; e != "" {
			if v, err := b.grid.ha.value(e); err == nil {
				updates = append(updates, u.UpdateDataEnergyConsumed(v, &now, nil, nil, nil))
			}
		}
		if e := b.cfg.GridEnergyFeedInEntity; e != "" {
			if v, err := b.grid.ha.value(e); err == nil {
				updates = append(updates, u.UpdateDataEnergyFeedIn(v, &now, nil, nil, nil))
			}
		}
		if err := u.Update(updates...); err != nil && b.cfg.Debug {
			log.Printf("grid update: %v", err)
		}
		if pub := b.publisher(); pub != nil {
			pub.GridSent(power, now)
		}
	}
}
