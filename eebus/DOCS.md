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
| `power_limit_max_minutes` | 240 | A switched-on power limit ends by itself after this many minutes. 0 = never. |
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

## Power limit

The VR940 accepts a limit on its electrical power (EEBUS *Limitation of Power
Consumption*). The add-on adds:

| Entity | |
|---|---|
| Power limit (number, W) | The limit value. 0 W means the heat pump does not run. |
| Power limit active (switch) | Turns the limit on or off. |
| Failsafe power limit (diagnostic) | What the device falls back to when the add-on stops sending heartbeats. |
| Power limit ends (diagnostic) | When an active limit switches itself off. |

Safety:

- An active limit switches off by itself after `power_limit_max_minutes`.
  Automations that need longer simply switch it on again.
- If the add-on or Home Assistant stops, the device notices the missing
  heartbeat within about two minutes and applies its **failsafe limit** for
  at least the failsafe duration. Check the *Failsafe power limit* sensor: on
  a VR940 without an energy-company contract it is normally very high (no
  limit).

Example: keep the heat pump below 1.5 kW while the car charges.

```yaml
automation:
  - alias: Heat pump limit while charging
    triggers:
      - trigger: state
        entity_id: binary_sensor.car_charging
    actions:
      - action: number.set_value
        target: {entity_id: number.vaillant_vr940_power_limit}
        data: {value: 1500}
      - action: "switch.turn_{{ 'on' if trigger.to_state.state == 'on' else 'off' }}"
        target: {entity_id: switch.vaillant_vr940_power_limit_active}
```

## Never silently stale

Each sensor has its own availability. If a value is not refreshed within
`stale_after`, it turns *unavailable* instead of showing an old number. If no
value at all arrives, the add-on reconnects; after three failed reconnects it
exits so the Supervisor restarts it.
