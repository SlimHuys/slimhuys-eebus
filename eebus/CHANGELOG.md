# Changelog

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
