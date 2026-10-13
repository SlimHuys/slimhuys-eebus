# SlimHuys EEBUS

Home Assistant add-on that connects EEBUS devices, such as the Vaillant VR940
heat pump gateway, to Home Assistant over MQTT discovery. Fully local: no
cloud, no extra integration.

[![Add repository to Home Assistant](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2FSlimHuys%2Fslimhuys-eebus)

## Why

The Vaillant VR940 reports live electrical power and a few temperatures over
EEBUS, which the myVAILLANT cloud does not offer. This add-on reads those
values and keeps them honest: every sensor turns *unavailable* when it is not
refreshed, instead of freezing on an old value.

Built on [enbility/eebus-go](https://github.com/enbility/eebus-go), through
the [volschin/eebus-go](https://github.com/volschin/eebus-go) fork that adds
the hot water and heating use cases. The VR940 research in
[volschin/eebus-ha-bridge](https://github.com/volschin/eebus-ha-bridge) was a
great help.

## Status

| | |
|---|---|
| Read power and temperatures | ✅ 0.1.0 |
| Power limit (LPC) | 🧪 0.2.0, being tested |
| Hot water mode and setpoint | ✅ 0.3.0 |
| Hot water boost | 🧪 0.3.0, not yet tested |
| Heating mode and room setpoint (climate) | ✅ 0.3.0 |
| Grid data for Vaillant energy management (MGCP) | 🧪 0.3.0, experimental |

Tested with: Vaillant aroTHERM plus + VR940. Reports for other devices are
welcome as an issue (include the add-on log with `debug: true`).

## Install

1. Add this repository: Settings → Add-ons → Add-on store → ⋮ → Repositories →
   `https://github.com/SlimHuys/slimhuys-eebus`
2. Install **SlimHuys EEBUS**, start it and follow the
   [documentation](eebus/DOCS.md).

## Development

```sh
cd eebus
go test ./...
EEBUS_DATA_DIR=./data MQTT_HOST=127.0.0.1 EEBUS_DEVICE_SKI=<ski> go run .
```

## Nederlands

Add-on van [SlimHuys](https://slimhuys.nl) die je EEBUS-warmtepomp (getest:
Vaillant VR940) lokaal in Home Assistant zet: live vermogen en temperaturen
via MQTT, zonder cloud. Zie [DOCS.md](eebus/DOCS.md) voor de installatie.

## License

MIT
