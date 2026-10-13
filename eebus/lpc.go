package main

import (
	"log"
	"math"
	"time"

	"github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/features/client"
	ucapi "github.com/enbility/eebus-go/usecases/api"
	eglpc "github.com/enbility/eebus-go/usecases/eg/lpc"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

// LPCState is the power limit (EEBUS "Limitation of Power Consumption") as
// the device reports it. Home Assistant always shows this, never what we
// last sent, so a rejected write is visible.
type LPCState struct {
	Supported        bool
	Active           bool
	Limit            float64
	NominalMax       float64
	Failsafe         float64
	FailsafeDuration time.Duration
	Expires          time.Time // zero = no automatic end
}

// A limit below this, left in the device while inactive, is treated as a
// leftover: switching it on would stop the heat pump outright.
const lpcLeftoverBelow = 500

// fallback for the number entity's maximum when the device does not report
// its nominal power
const lpcDefaultMax = 10000

func (b *Bridge) setupLPC(local spineapi.EntityLocalInterface) {
	b.lpc = eglpc.NewLPC(local, b.onLPCEvent)
	b.svc.AddUseCase(b.lpc)
	// The device only accepts limits from an energy guard that sends
	// heartbeats; when they stop it falls back to its failsafe limit.
	if err := local.HeartbeatManager().StartHeartbeat(); err != nil {
		log.Printf("Could not start EEBUS heartbeat: %v", err)
	}
}

func (b *Bridge) onLPCEvent(ski string, _ spineapi.DeviceRemoteInterface, _ spineapi.EntityRemoteInterface, event api.EventType) {
	if normalizeSKI(ski) != b.currentSKI() {
		return
	}
	switch event {
	case eglpc.UseCaseSupportUpdate, eglpc.DataUpdateLimit,
		eglpc.DataUpdateFailsafeConsumptionActivePowerLimit, eglpc.DataUpdateFailsafeDurationMinimum:
		b.refreshLPC()
	}
}

// lpcEntity returns the remote entity of our device that supports LPC
// scenario 1. A gateway like the VR940 exposes several entities; only the
// heat pump appliance accepts limits.
func (b *Bridge) lpcEntity() spineapi.EntityRemoteInterface {
	if b.lpc == nil {
		return nil
	}
	target := b.currentSKI()
	for _, rs := range b.lpc.RemoteEntitiesScenarios() {
		if rs.Entity == nil || rs.Entity.Device() == nil || normalizeSKI(rs.Entity.Device().Ski()) != target {
			continue
		}
		for _, s := range rs.Scenarios {
			if s == 1 {
				return rs.Entity
			}
		}
	}
	return nil
}

func (b *Bridge) refreshLPC() {
	e := b.lpcEntity()

	b.mu.Lock()
	st := b.lpcState
	b.mu.Unlock()

	if e == nil {
		st.Supported = false
		b.publishLPC(st)
		return
	}
	// The device announces LPC support before it sends the values. Until the
	// limit and failsafe are actually known, keep the entities unavailable
	// and do not act on empty data.
	l, errLimit := b.lpc.ConsumptionLimit(e)
	failsafe, errFailsafe := b.lpc.FailsafeConsumptionActivePowerLimit(e)
	if errLimit != nil || errFailsafe != nil {
		if b.cfg.Debug {
			log.Printf("power limit data not complete yet (limit: %v, failsafe: %v)", errLimit, errFailsafe)
		}
		st.Supported = false
		b.publishLPC(st)
		return
	}
	st.Supported = true
	st.Limit, st.Active, st.Failsafe = l.Value, l.IsActive, failsafe
	if v, err := b.lpc.ConsumptionNominalMax(e); err == nil && v > 0 {
		st.NominalMax = v
	}
	if d, err := b.lpc.FailsafeDurationMinimum(e); err == nil {
		st.FailsafeDuration = d
	}
	b.mu.Lock()
	first := !b.lpcChecked
	b.lpcChecked = true
	switch {
	case !st.Active:
		st.Expires = time.Time{}
	case b.lpcState.Expires.IsZero() && b.cfg.PowerLimitMaxMinutes > 0:
		// Active limit we did not set in this run (e.g. after a restart):
		// give it the same maximum duration, so it can never stick forever.
		st.Expires = time.Now().Add(time.Duration(b.cfg.PowerLimitMaxMinutes) * time.Minute)
	default:
		st.Expires = b.lpcState.Expires
	}
	b.lpcState = st
	b.mu.Unlock()

	if first {
		log.Printf("Power limit supported: limit %s W (active: %v), failsafe %s W for %s",
			formatValue(st.Limit), st.Active, formatValue(st.Failsafe), st.FailsafeDuration)
		if st.Failsafe > 0 && st.Failsafe < b.lpcMax(st) {
			log.Printf("Note: the device's failsafe limit is %s W. If this add-on stops, the heat pump is held at that limit for at least %s.",
				formatValue(st.Failsafe), st.FailsafeDuration)
		}
	}
	b.publishLPC(st)
}

func (b *Bridge) lpcMax(st LPCState) float64 {
	if st.NominalMax > 0 {
		return st.NominalMax
	}
	return lpcDefaultMax
}

func (b *Bridge) publishLPC(st LPCState) {
	if pub := b.publisher(); pub != nil {
		pub.LPC(st, b.lpcMax(st))
	}
}

func (b *Bridge) writeLPC(e spineapi.EntityRemoteInterface, value float64, active bool) {
	limit := ucapi.LoadLimit{Value: value, IsActive: active}
	_, err := b.lpc.WriteConsumptionLimit(e, limit, func(res model.ResultDataType, _ model.MsgCounterType) {
		if res.ErrorNumber != nil && *res.ErrorNumber != model.ErrorNumberTypeNoError {
			desc := ""
			if res.Description != nil {
				desc = string(*res.Description)
			}
			log.Printf("Device rejected power limit %s W (active: %v): error %d %s", formatValue(value), active, *res.ErrorNumber, desc)
		}
		// The VR940 does not always notify a change it made on our request
		// (switching a limit off went unreported), so ask for the result.
		b.requestLPCData(e)
		b.refreshLPC()
	})
	if err != nil {
		log.Printf("Writing power limit failed: %v", err)
	}
}

// requestLPCData asks the device for its current limits; the answer arrives
// as an LPC data update event.
func (b *Bridge) requestLPCData(e spineapi.EntityRemoteInterface) {
	lc, err := client.NewLoadControl(b.local, e)
	if err != nil {
		return
	}
	if _, err := lc.RequestLimitData(nil, nil); err != nil && b.cfg.Debug {
		log.Printf("requesting power limit data: %v", err)
	}
}

// SetLPCLimit is called from MQTT when the number entity changes.
func (b *Bridge) SetLPCLimit(value float64) {
	e := b.lpcEntity()
	if e == nil {
		log.Printf("Power limit not available (yet)")
		return
	}
	b.mu.Lock()
	st := b.lpcState
	b.mu.Unlock()
	value = math.Max(0, math.Min(value, b.lpcMax(st)))
	b.mu.Lock()
	b.lpcLimitSet = true
	// Remember what was asked: the device confirms a new value only after
	// ~10 s, and switching on in between must use this value, not the old
	// confirmed one (that is how a 1000 W test became an active 10000 W
	// limit on 13 October 2026).
	b.lpcWant = value
	b.mu.Unlock()
	log.Printf("Power limit set to %s W (active: %v)", formatValue(value), st.Active)
	b.writeLPC(e, value, st.Active)
}

// SetLPCActive is called from MQTT when the switch changes.
func (b *Bridge) SetLPCActive(on bool) {
	e := b.lpcEntity()
	if e == nil {
		log.Printf("Power limit not available (yet)")
		return
	}
	b.mu.Lock()
	st := b.lpcState
	if b.lpcLimitSet {
		st.Limit = b.lpcWant
	}
	if on && st.Limit < lpcLeftoverBelow && !b.lpcLimitSet {
		b.mu.Unlock()
		log.Printf("Not switching the power limit on: it is %s W, which would stop the heat pump, and it was not set from Home Assistant. Set the Power limit first (0 W is allowed when set on purpose).", formatValue(st.Limit))
		b.publishLPC(st) // put the switch back to OFF in Home Assistant
		return
	}
	if on && b.cfg.PowerLimitMaxMinutes > 0 {
		b.lpcState.Expires = time.Now().Add(time.Duration(b.cfg.PowerLimitMaxMinutes) * time.Minute)
	} else {
		b.lpcState.Expires = time.Time{}
	}
	expires := b.lpcState.Expires
	b.mu.Unlock()

	if on {
		msg := "no automatic end"
		if !expires.IsZero() {
			msg = "ends automatically at " + expires.Local().Format("15:04")
		}
		log.Printf("Power limit ON at %s W (%s)", formatValue(st.Limit), msg)
	} else {
		log.Printf("Power limit OFF")
	}
	b.writeLPC(e, st.Limit, on)
}

// checkLPCExpiry switches an expired limit off. Called from the main loop.
func (b *Bridge) checkLPCExpiry(now time.Time) {
	b.mu.Lock()
	st := b.lpcState
	b.mu.Unlock()
	if st.Active && !st.Expires.IsZero() && now.After(st.Expires) {
		log.Printf("Power limit reached its maximum duration of %d minutes, switching it off", b.cfg.PowerLimitMaxMinutes)
		b.SetLPCActive(false)
	}
}

// Wait this long after connecting before touching the limit: the VR940 only
// accepts limits from an energy guard whose heartbeat it already receives,
// and that subscription is set up a moment after the connection (a write one
// second too early came back as "error 7 Write failed").
const lpcLeftoverAfter = 30 * time.Second

const lpcLeftoverMaxTries = 5

// fixLPCLeftover raises an inactive limit below lpcLeftoverBelow to the
// maximum, so switching the limit on cannot stop the heat pump by accident.
// Called from the poller; retried until the device accepts it.
func (b *Bridge) fixLPCLeftover() {
	b.mu.Lock()
	st := b.lpcState
	due := !b.lpcLeftoverDone && b.lpcChecked && st.Supported &&
		time.Since(b.connectedAt) >= lpcLeftoverAfter && b.lpcLeftoverTries < lpcLeftoverMaxTries
	if due && (st.Active || st.Limit >= lpcLeftoverBelow || b.lpcLimitSet) {
		// Nothing to fix (or the user already chose a value).
		b.lpcLeftoverDone, due = true, false
	}
	if due {
		b.lpcLeftoverTries++
	}
	tries := b.lpcLeftoverTries
	b.mu.Unlock()
	if !due {
		return
	}
	e := b.lpcEntity()
	if e == nil {
		return
	}
	max := b.lpcMax(st)
	log.Printf("Inactive power limit of %s W found; setting it to %s W so switching it on cannot stop the heat pump by accident (attempt %d/%d)",
		formatValue(st.Limit), formatValue(max), tries, lpcLeftoverMaxTries)
	b.writeLPC(e, max, false)
}
