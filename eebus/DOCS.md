# SlimHuys EEBUS

Connects an EEBUS device on your LAN to Home Assistant through MQTT discovery.
Tested with the Vaillant VR940 gateway (aroTHERM plus). Other EEBUS heat pumps
and gateways that offer the SPINE Measurement feature should work too; every
value the device reports becomes a sensor.

## Setup

1. Make sure Home Assistant has MQTT. With the Mosquitto add-on nothing needs
   to be configured here; for an external broker fill in `mqtt_host`,
   `mqtt_username` and `mqtt_password`.
2. Start the add-on and open the log. It prints the local SKI:

   ```
   Local SKI: 0651…1c13 (trust this in the manufacturer app …)
   ```

3. Trust the bridge in the manufacturer app. For Vaillant: myVAILLANT →
   Settings → Network → EEBUS → Available devices → **SlimHuys EEBUS-Bridge**.
4. The device and its sensors appear under Settings → Devices & services →
   MQTT.

Most devices accept **one EEBUS energy manager at a time**. Stop other EEBUS
bridges (or evcc's EEBUS connection) before you start this one.

## Options

| Option | Default | Meaning |
|---|---|---|
| `device_ski` | empty | SKI of the device. Empty = use the only EEBUS device found. |
| `mqtt_host` … `mqtt_password` | empty | External broker. Empty = Mosquitto add-on. |
| `poll_interval` | 300 | Seconds between polls. The device pushes changes itself; polling is a safety net. |
| `stale_after` | 720 | A value not refreshed for this long becomes *unavailable*. Minimum: two poll intervals. |
| `port` | 4713 | TCP port for incoming EEBUS connections. |
| `debug` | false | Log every received value. |

## Entities

For the Vaillant VR940:

| Entity | Source |
|---|---|
| Power (W) | `HeatPumpAppliance` · `acPowerTotal` |
| Hot water temperature (°C) | `DHWCircuit` · `dhwTemperature` |
| Room temperature (°C) | `HVACRoom` · `roomAirTemperature` |
| Outdoor temperature (°C) | `TemperatureSensor` · `outsideAirTemperature` |
| Connected | EEBUS connection state (diagnostic) |
| Last update | Time of the newest value (diagnostic) |

Every sensor carries the attributes `last_update`, `source` (`notify` = pushed
by the device, `reply` = answer to a poll), `spine_entity` and
`measurement_id`.

The VR940 pushes power about once a minute when it changes and temperatures
every five minutes. It does not report energy counters; use a Riemann sum
integral helper on the power sensor for the Energy dashboard.

## Never silently stale

Each sensor has its own availability. If a value is not refreshed within
`stale_after`, it turns *unavailable* instead of showing an old number. If no
value at all arrives, the add-on reconnects; after three failed reconnects it
exits so the Supervisor restarts it.
