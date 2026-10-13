# Changelog

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
