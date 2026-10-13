package main

import (
	"log"
	"slices"

	"github.com/enbility/eebus-go/api"
	ucapi "github.com/enbility/eebus-go/usecases/api"
	cacdsf "github.com/enbility/eebus-go/usecases/ca/cdsf"
	cacrhsf "github.com/enbility/eebus-go/usecases/ca/crhsf"
	cacrht "github.com/enbility/eebus-go/usecases/ca/crht"
	mamdsf "github.com/enbility/eebus-go/usecases/ma/mdsf"
	mamrhsf "github.com/enbility/eebus-go/usecases/ma/mrhsf"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

// DHWState is the hot water circuit as the device reports it.
type DHWState struct {
	ModeKnown     bool
	Mode          string
	Modes         []string
	ModeWritable  bool
	BoostKnown    bool
	BoostActive   bool
	BoostStatus   string
	BoostWritable bool
	Setpoint      *DHWSetpoint
}

// RoomState is the space heating circuit as the device reports it.
type RoomState struct {
	ModeKnown        bool
	Mode             string
	Modes            []string
	ModeWritable     bool
	SetpointKnown    bool
	Setpoint         float64
	Min, Max, Step   float64
	SetpointWritable bool
}

type hvac struct {
	mdsf  *mamdsf.MDSF
	cdsf  *cacdsf.CDSF
	dhwSP *dhwSetpointUC
	mrhsf *mamrhsf.MRHSF
	crhsf *cacrhsf.CRHSF
	crht  *cacrht.CRHT
}

func (b *Bridge) setupHVAC(local spineapi.EntityLocalInterface) {
	cb := func(string, spineapi.DeviceRemoteInterface, spineapi.EntityRemoteInterface, api.EventType) {
		b.refreshHVAC()
	}
	h := &hvac{
		mdsf:  mamdsf.NewMDSF(local, cb),
		cdsf:  cacdsf.NewCDSF(local, cb),
		dhwSP: newDHWSetpointUC(local, b.refreshHVAC),
		mrhsf: mamrhsf.NewMRHSF(local, cb),
		crhsf: cacrhsf.NewCRHSF(local, cb),
		crht:  cacrht.NewCRHT(local, cb),
	}
	for _, uc := range []api.UseCaseInterface{h.mdsf, h.cdsf, h.dhwSP, h.mrhsf, h.crhsf, h.crht} {
		if err := b.svc.AddUseCase(uc); err != nil {
			log.Printf("Could not register an EEBUS use case: %v", err)
		}
	}
	// Device diagnosis (operating state) is read by the poller; it needs
	// the client feature.
	local.GetOrAddFeature(model.FeatureTypeTypeDeviceDiagnosis, model.RoleTypeClient)
	b.hvac = h
}

type scenarioLister interface {
	RemoteEntitiesScenarios() []api.RemoteEntityScenarios
}

// entityFor returns the remote entity of our device that supports a use case.
func (b *Bridge) entityFor(uc scenarioLister) spineapi.EntityRemoteInterface {
	if uc == nil {
		return nil
	}
	target := b.currentSKI()
	for _, rs := range uc.RemoteEntitiesScenarios() {
		if rs.Entity != nil && rs.Entity.Device() != nil && normalizeSKI(rs.Entity.Device().Ski()) == target {
			return rs.Entity
		}
	}
	return nil
}

func modeStrings(modes []ucapi.HvacOperationModeType) []string {
	out := []string{}
	for _, m := range modes {
		if s := string(m); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func (b *Bridge) readDHW() DHWState {
	h := b.hvac
	st := DHWState{}
	if e := b.entityFor(h.mdsf); e != nil {
		modes, errModes := h.mdsf.OperationModes(e)
		cur, errCur := h.mdsf.CurrentOperationMode(e)
		if errModes == nil && errCur == nil {
			st.ModeKnown, st.Mode, st.Modes = true, string(cur), modeStrings(modes)
		}
		if active, err := h.mdsf.IsOverrunActive(e); err == nil {
			st.BoostKnown, st.BoostActive = true, active
		}
		if status, err := h.mdsf.OverrunStatus(e); err == nil {
			st.BoostStatus = string(status)
		}
	}
	if e := b.entityFor(h.cdsf); e != nil {
		if caps, err := h.cdsf.WriteCapabilities(e); err == nil {
			st.ModeWritable = caps.OperationMode
			st.BoostWritable = caps.StartOneTimeDhw
		}
	}
	if e := b.entityFor(h.dhwSP); e != nil {
		if sp, err := h.dhwSP.State(e); err == nil {
			st.Setpoint = &sp
		}
	}
	return st
}

func (b *Bridge) readRoom() RoomState {
	h := b.hvac
	st := RoomState{}
	if e := b.entityFor(h.mrhsf); e != nil {
		modes, errModes := h.mrhsf.OperationModes(e)
		cur, errCur := h.mrhsf.CurrentOperationMode(e)
		if errModes == nil && errCur == nil {
			st.ModeKnown, st.Mode, st.Modes = true, string(cur), modeStrings(modes)
		}
	}
	st.ModeWritable = b.entityFor(h.crhsf) != nil
	if e := b.entityFor(h.crht); e != nil {
		if sp, err := h.crht.State(e); err == nil && sp.StepSize > 0 {
			st.SetpointKnown = true
			st.Setpoint, st.Min, st.Max, st.Step = sp.Value, sp.MinValue, sp.MaxValue, sp.StepSize
			st.SetpointWritable = sp.IsWritable
		}
	}
	return st
}

func (b *Bridge) refreshHVAC() {
	if b.hvac == nil {
		return
	}
	dhw, room := b.readDHW(), b.readRoom()

	b.mu.Lock()
	first := !b.hvacLogged && (dhw.ModeKnown || room.ModeKnown)
	if first {
		b.hvacLogged = true
	}
	b.mu.Unlock()
	if first {
		sp := "unknown"
		if dhw.Setpoint != nil {
			sp = formatValue(dhw.Setpoint.Value) + " °C"
		}
		log.Printf("Hot water: mode %q of %v (writable: %v), boost %q (writable: %v), setpoint %s",
			dhw.Mode, dhw.Modes, dhw.ModeWritable, dhw.BoostStatus, dhw.BoostWritable, sp)
		log.Printf("Heating: mode %q of %v (writable: %v), setpoint %s °C (writable: %v)",
			room.Mode, room.Modes, room.ModeWritable, formatValue(room.Setpoint), room.SetpointWritable)
	}
	if pub := b.publisher(); pub != nil {
		pub.HVAC(dhw, room)
	}
}

// writeResult logs a rejected write and refreshes the state either way, so
// Home Assistant shows what the device actually has.
func (b *Bridge) writeResult(what string) func(model.ResultDataType, model.MsgCounterType) {
	return func(res model.ResultDataType, _ model.MsgCounterType) {
		b.logResult(what, res)
	}
}

func (b *Bridge) logResult(what string, res model.ResultDataType) {
	if res.ErrorNumber != nil && *res.ErrorNumber != model.ErrorNumberTypeNoError {
		desc := ""
		if res.Description != nil {
			desc = " " + string(*res.Description)
		}
		log.Printf("Device rejected %s: error %d%s", what, *res.ErrorNumber, desc)
	}
	b.refreshHVAC()
}

func (b *Bridge) SetDHWMode(mode string) {
	e := b.entityFor(b.hvac.cdsf)
	if e == nil {
		log.Printf("Hot water mode cannot be set (not offered by the device)")
		return
	}
	log.Printf("Hot water mode → %s", mode)
	if _, err := b.hvac.cdsf.WriteOperationMode(e, ucapi.HvacOperationModeType(mode), b.writeResult("hot water mode "+mode)); err != nil {
		log.Printf("Setting hot water mode failed: %v", err)
	}
}

func (b *Bridge) SetDHWBoost(on bool) {
	e := b.entityFor(b.hvac.cdsf)
	if e == nil {
		log.Printf("Hot water boost not offered by the device")
		return
	}
	var err error
	if on {
		log.Printf("Hot water boost → start")
		_, err = b.hvac.cdsf.StartOneTimeDhw(e, b.writeResult("hot water boost start"))
	} else {
		log.Printf("Hot water boost → stop")
		_, err = b.hvac.cdsf.StopOneTimeDhw(e, b.writeResult("hot water boost stop"))
	}
	if err != nil {
		log.Printf("Hot water boost failed: %v", err)
	}
}

func (b *Bridge) SetDHWSetpoint(v float64) {
	e := b.entityFor(b.hvac.dhwSP)
	if e == nil {
		log.Printf("Hot water setpoint not offered by the device")
		return
	}
	log.Printf("Hot water setpoint → %s °C", formatValue(v))
	if err := b.hvac.dhwSP.Write(e, v, func(res model.ResultDataType) { b.logResult("hot water setpoint", res) }); err != nil {
		log.Printf("Setting hot water setpoint failed: %v", err)
		b.refreshHVAC()
	}
}

func (b *Bridge) SetRoomMode(mode string) {
	e := b.entityFor(b.hvac.crhsf)
	if e == nil {
		log.Printf("Heating mode cannot be set (not offered by the device)")
		return
	}
	log.Printf("Heating mode → %s", mode)
	if _, err := b.hvac.crhsf.WriteOperationMode(e, ucapi.HvacOperationModeType(mode), b.writeResult("heating mode "+mode)); err != nil {
		log.Printf("Setting heating mode failed: %v", err)
		b.refreshHVAC()
	}
}

func (b *Bridge) SetRoomSetpoint(v float64) {
	e := b.entityFor(b.hvac.crht)
	if e == nil {
		log.Printf("Room setpoint not offered by the device")
		return
	}
	st := b.readRoom()
	if !st.SetpointKnown {
		log.Printf("Room setpoint data not available yet")
		return
	}
	if err := validSetpoint(v, st.Min, st.Max, st.Step); err != nil {
		log.Printf("Room setpoint %s °C rejected: %v", formatValue(v), err)
		b.refreshHVAC()
		return
	}
	log.Printf("Room setpoint → %s °C", formatValue(v))
	if _, err := b.hvac.crht.WriteRoomAirTemperatureSetpoint(e, v, b.writeResult("room setpoint")); err != nil {
		log.Printf("Setting room setpoint failed: %v", err)
		b.refreshHVAC()
	}
}

// ---- operating state (DeviceDiagnosis) ----

func (b *Bridge) requestOperatingState(e spineapi.EntityRemoteInterface) {
	remote := e.FeatureOfTypeAndRole(model.FeatureTypeTypeDeviceDiagnosis, model.RoleTypeServer)
	local := b.local.FeatureOfTypeAndRole(model.FeatureTypeTypeDeviceDiagnosis, model.RoleTypeClient)
	if remote == nil || local == nil {
		return
	}
	if op := remote.Operations()[model.FunctionTypeDeviceDiagnosisStateData]; op == nil || !op.Read() {
		return
	}
	_, _ = local.RequestRemoteData(model.FunctionTypeDeviceDiagnosisStateData, nil, nil, remote)
}

func (b *Bridge) onDiagnosis(d *model.DeviceDiagnosisStateDataType) {
	if d == nil || d.OperatingState == nil {
		return
	}
	state := string(*d.OperatingState)
	b.mu.Lock()
	changed := state != b.operatingState
	b.operatingState = state
	b.mu.Unlock()
	if changed {
		log.Printf("Operating state: %s", state)
	}
	if pub := b.publisher(); pub != nil {
		pub.OperatingState(state)
	}
}
