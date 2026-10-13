# Changelog

## 0.1.0

- First release: read-only. Every SPINE measurement of the device becomes an
  MQTT discovery sensor (power, hot water, room and outdoor temperature on the
  Vaillant VR940).
- Values come from device pushes, with a poll every `poll_interval` as a
  safety net.
- Per-sensor availability: values older than `stale_after` become
  unavailable; the add-on reconnects when no data arrives at all.
