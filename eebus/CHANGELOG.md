# Changelog

## 0.3.3

- Values the device flags as invalid (`valueState` other than `normal`) are
  shown as *unavailable* instead of as a number. The VR940 reports a room
  temperature of 0 °C with state `error` right after its EEBUS service
  restarts.
- The startup fix for a leftover 0 W power limit now waits 30 seconds after
  connecting and retries up to five times. Right after a connection the
  VR940 does not yet receive our heartbeat and answers `error 7 Write failed`.

## 0.3.2

- Fix: switching the power limit on right after changing its value
  activated the *old* value. The VR940 confirms a new limit only after about
  10 seconds; the switch now uses the value last asked for. Seen in a live
  test: a 1000 W limit became an active 10000 W limit.
- Fix: the power limit could keep showing *on* after it was switched off.
  The VR940 did not report that change by itself; the add-on now asks for
  the limit after every write and on every poll.

## 0.3.1

- Hot water and heating are logged whenever their state changes, instead of
  once on the (usually incomplete) first snapshot after connecting.
- Confirmed on a Vaillant VR940: writing the hot water setpoint, the room
  setpoint and the hot water mode; each change came back from the device
  within about a second.

## 0.3.0

- **Hot water**: mode (select), setpoint (number) and one-time boost
  (switch), plus a boost status sensor. Anything the device only offers
  read-only shows up as a plain sensor instead of a control that does
  nothing.
- **Heating**: a climate entity (auto / heat / off and the room setpoint),
  with the EEBUS room temperature as current temperature.
- **Operating state** of the device as a diagnostic sensor.
- **Grid data (experimental)**: with `grid_power_entity` set, the add-on
  offers your grid meter to the device (EEBUS MGCP) for Vaillant's own
  solar-surplus energy management. Requires the "Energy management" sliders
  in the myVAILLANT app; whether the VR940 acts on it is not confirmed yet.
- Now built on the eebus-go fork by volschin, which adds the hot water and
  heating use cases (MDSF, CDSF, MRHSF, CRHSF, CRHT, MGCP). The hot water
  setpoint write follows the approach volschin/eebus-ha-bridge validated on
  the VR940.

## 0.2.1

- Fix: the power limit was read the moment the device announced support,
  before it sent any values. The startup check then acted on empty data
  (logged as `failsafe 0 W for 0s`), its write failed with `data not
  available`, and a leftover 0 W limit stayed in place. The limit entities now
  stay unavailable until the device has sent real values, and the startup
  check runs only then.
- Safety net: the Power limit switch refuses to turn on while the limit is
  below 500 W, unless that value was set from Home Assistant in this run.

## 0.2.0

- Power limit (EEBUS LPC): a **Power limit** number and a **Power limit
  active** switch, plus diagnostic sensors for the device's failsafe limit and
  when the limit ends. The entities show what the device reports, not what was
  sent.
- A switched-on limit ends by itself after `power_limit_max_minutes`
  (default 240). Limits found active after a restart get the same maximum.
- An inactive limit below 500 W left in the device is raised to the device's
  nominal power at startup, so switching the limit on can't stop the heat pump
  by accident.
- The add-on now sends EEBUS heartbeats; if it stops, the device falls back to
  its own failsafe limit (shown as a sensor).

## 0.1.2

- SlimHuys icon and logo in the add-on store.
- Hourly log line with the number of values received and how many the device
  pushed by itself, so a quiet log can be told apart from a stuck bridge.
- No more `ERROR … http: Server closed` on a normal restart.

## 0.1.1

- Log the MQTT broker at startup and why connecting fails (wrong host,
  credentials); before, the add-on retried silently.
- Log an untrusted/rejected EEBUS connection once every five minutes instead
  of on every retry.

## 0.1.0

- First release: read-only. Every SPINE measurement of the device becomes an
  MQTT discovery sensor (power, hot water, room and outdoor temperature on the
  Vaillant VR940).
- Values come from device pushes, with a poll every `poll_interval` as a
  safety net.
- Per-sensor availability: values older than `stale_after` become
  unavailable; the add-on reconnects when no data arrives at all.
