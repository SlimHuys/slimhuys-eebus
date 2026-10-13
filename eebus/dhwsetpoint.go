package main

// Hot water (DHW) target temperature.
//
// eebus-go's CDT use case writes a single setpoint entry, but the Vaillant
// VR940 only accepts a write that carries the complete SetpointListData. This
// small use case follows the approach that volschin/eebus-ha-bridge (MIT)
// validated against the VR940: subscribe to the DHW circuit's Setpoint
// server, read value + constraints from the cache, and write back the full
// list with only the DHW value changed.

import (
	"errors"
	"fmt"
	"math"

	"github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/usecases/usecase"
	spineapi "github.com/enbility/spine-go/api"
	"github.com/enbility/spine-go/model"
)

const dhwSetpointSupportUpdate api.EventType = "slimhuys-dhw-setpoint-support-update"

var errDHWSetpointUnavailable = errors.New("hot water setpoint data not available yet")

// DHWSetpoint is the hot water target with the device's constraints.
type DHWSetpoint struct {
	Value, Min, Max, Step float64
	Writable              bool
}

type dhwSetpointUC struct {
	*usecase.UseCaseBase
	local    spineapi.EntityLocalInterface
	onChange func()
}

func newDHWSetpointUC(local spineapi.EntityLocalInterface, onChange func()) *dhwSetpointUC {
	d := &dhwSetpointUC{local: local, onChange: onChange}
	d.UseCaseBase = usecase.NewUseCaseBase(
		local,
		model.UseCaseActorTypeConfigurationAppliance,
		model.UseCaseNameTypeConfigurationOfDhwTemperature,
		"1.0.0",
		"release",
		[]api.UseCaseScenario{{
			Scenario:       model.UseCaseScenarioSupportType(1),
			Mandatory:      true,
			ServerFeatures: []model.FeatureTypeType{model.FeatureTypeTypeSetpoint},
		}},
		func(string, spineapi.DeviceRemoteInterface, spineapi.EntityRemoteInterface, api.EventType) {
			onChange()
		},
		dhwSetpointSupportUpdate,
		[]model.UseCaseActorType{model.UseCaseActorTypeDHWCircuit},
		[]model.EntityTypeType{model.EntityTypeTypeDHWCircuit},
		false,
	)
	_ = local.Device().Events().Subscribe(d)
	return d
}

func (d *dhwSetpointUC) AddFeatures() error {
	if d.local.GetOrAddFeature(model.FeatureTypeTypeSetpoint, model.RoleTypeClient) == nil {
		return errors.New("could not add Setpoint client feature")
	}
	return nil
}

func (d *dhwSetpointUC) localFeature() spineapi.FeatureLocalInterface {
	return d.local.FeatureOfTypeAndRole(model.FeatureTypeTypeSetpoint, model.RoleTypeClient)
}

func setpointServer(e spineapi.EntityRemoteInterface) spineapi.FeatureRemoteInterface {
	if e == nil {
		return nil
	}
	return e.FeatureOfTypeAndRole(model.FeatureTypeTypeSetpoint, model.RoleTypeServer)
}

// HandleEvent subscribes when the DHW circuit appears and requests the data
// again when its metadata changes.
func (d *dhwSetpointUC) HandleEvent(p spineapi.EventPayload) {
	if p.Entity == nil || !d.IsCompatibleEntityType(p.Entity) {
		return
	}
	if p.EventType == spineapi.EventTypeEntityChange && p.ChangeType == spineapi.ElementChangeAdd {
		d.connect(p.Entity)
		return
	}
	if p.EventType != spineapi.EventTypeDataChange || p.ChangeType != spineapi.ElementChangeUpdate {
		return
	}
	switch p.Data.(type) {
	case *model.SetpointDescriptionListDataType, *model.SetpointConstraintsListDataType:
		d.request(p.Entity, model.FunctionTypeSetpointListData)
		d.onChange()
	case *model.SetpointListDataType:
		d.onChange()
	}
}

func (d *dhwSetpointUC) connect(e spineapi.EntityRemoteInterface) {
	remote, local := setpointServer(e), d.localFeature()
	if remote == nil || local == nil {
		return
	}
	if !local.HasSubscriptionToRemote(remote.Address()) {
		_, _ = local.SubscribeToRemote(remote.Address())
	}
	if !local.HasBindingToRemote(remote.Address()) {
		_, _ = local.BindToRemote(remote.Address())
	}
	d.Refresh(e)
}

// Refresh requests descriptions, constraints and values.
func (d *dhwSetpointUC) Refresh(e spineapi.EntityRemoteInterface) {
	for _, f := range []model.FunctionType{
		model.FunctionTypeSetpointDescriptionListData,
		model.FunctionTypeSetpointConstraintsListData,
		model.FunctionTypeSetpointListData,
	} {
		d.request(e, f)
	}
}

func (d *dhwSetpointUC) request(e spineapi.EntityRemoteInterface, f model.FunctionType) {
	remote, local := setpointServer(e), d.localFeature()
	if remote == nil || local == nil {
		return
	}
	if op := remote.Operations()[f]; op == nil || !op.Read() {
		return
	}
	_, _ = local.RequestRemoteData(f, nil, nil, remote)
}

func dhwSetpointID(remote spineapi.FeatureRemoteInterface) (model.SetpointIdType, bool) {
	data, ok := remote.DataCopy(model.FunctionTypeSetpointDescriptionListData).(*model.SetpointDescriptionListDataType)
	if !ok || data == nil {
		return 0, false
	}
	for _, desc := range data.SetpointDescriptionData {
		if desc.SetpointId != nil && desc.ScopeType != nil && *desc.ScopeType == model.ScopeTypeTypeDhwTemperature {
			return *desc.SetpointId, true
		}
	}
	return 0, false
}

// State reads the current target and constraints from the cache. Missing
// metadata fails: Home Assistant must never get an invented range for a
// physical temperature.
func (d *dhwSetpointUC) State(e spineapi.EntityRemoteInterface) (DHWSetpoint, error) {
	remote := setpointServer(e)
	if remote == nil {
		return DHWSetpoint{}, errDHWSetpointUnavailable
	}
	id, ok := dhwSetpointID(remote)
	if !ok {
		return DHWSetpoint{}, errDHWSetpointUnavailable
	}
	st := DHWSetpoint{}
	found := false
	if data, ok := remote.DataCopy(model.FunctionTypeSetpointListData).(*model.SetpointListDataType); ok && data != nil {
		for _, sp := range data.SetpointData {
			if sp.SetpointId != nil && *sp.SetpointId == id && sp.Value != nil {
				st.Value, found = sp.Value.GetValue(), true
			}
		}
	}
	if !found {
		return DHWSetpoint{}, errDHWSetpointUnavailable
	}
	found = false
	if data, ok := remote.DataCopy(model.FunctionTypeSetpointConstraintsListData).(*model.SetpointConstraintsListDataType); ok && data != nil {
		for _, c := range data.SetpointConstraintsData {
			if c.SetpointId == nil || *c.SetpointId != id || c.SetpointRangeMin == nil || c.SetpointRangeMax == nil || c.SetpointStepSize == nil {
				continue
			}
			st.Min, st.Max, st.Step = c.SetpointRangeMin.GetValue(), c.SetpointRangeMax.GetValue(), c.SetpointStepSize.GetValue()
			found = st.Step > 0 && st.Min <= st.Max
		}
	}
	if !found {
		return DHWSetpoint{}, errDHWSetpointUnavailable
	}
	op := remote.Operations()[model.FunctionTypeSetpointListData]
	st.Writable = op != nil && op.Write()
	return st, nil
}

// validSetpoint checks a value against the device's range and step.
func validSetpoint(v, min, max, step float64) error {
	if math.IsNaN(v) || v < min || v > max {
		return fmt.Errorf("%g is outside %g–%g", v, min, max)
	}
	if step > 0 {
		n := math.Round((v - min) / step)
		if math.Abs(min+n*step-v) > 1e-6 {
			return fmt.Errorf("%g is not a multiple of the step %g", v, step)
		}
	}
	return nil
}

// Write sends the complete setpoint list with only the DHW value changed.
func (d *dhwSetpointUC) Write(e spineapi.EntityRemoteInterface, value float64, resultCB func(model.ResultDataType)) error {
	st, err := d.State(e)
	if err != nil {
		return err
	}
	if !st.Writable {
		return errors.New("the device does not allow writing the hot water setpoint")
	}
	if err := validSetpoint(value, st.Min, st.Max, st.Step); err != nil {
		return err
	}
	remote, local := setpointServer(e), d.localFeature()
	id, _ := dhwSetpointID(remote)
	data, ok := remote.DataCopy(model.FunctionTypeSetpointListData).(*model.SetpointListDataType)
	if !ok || data == nil {
		return errDHWSetpointUnavailable
	}
	entries := make([]model.SetpointDataType, len(data.SetpointData))
	copy(entries, data.SetpointData)
	for i := range entries {
		if entries[i].SetpointId != nil && *entries[i].SetpointId == id {
			entries[i].Value = model.NewScaledNumberType(value)
		}
	}
	counter, err := e.Device().Sender().Write(local.Address(), remote.Address(),
		model.CmdType{SetpointListData: &model.SetpointListDataType{SetpointData: entries}})
	if err != nil {
		return err
	}
	if counter != nil && resultCB != nil {
		_ = local.AddResponseCallback(*counter, func(msg spineapi.ResponseMessage) {
			if res, ok := msg.Data.(*model.ResultDataType); ok && res != nil {
				resultCB(*res)
				d.request(e, model.FunctionTypeSetpointListData)
			}
		})
	}
	return nil
}
