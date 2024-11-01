# MQTT D-Bus Home Assistant Integration

A Go service that connects your Linux system's screen brightness to Home Assistant via MQTT.

## Limitations

- Currently supports only one screen/monitor
- Does not verify maximum brightness values
- Assumes default D-Bus paths for screen brightness control (KDE)
- Fixed scaling from 0-10000 (KDE) to 0-255 (Home Assistant)

## Configuration

Create a configuration file at `/etc/go-mqtt-dbus.conf` with the following JSON structure:
{
    "mqtt_broker": "192.168.178.81",
    "mqtt_port": 1883,
    "client_id": "go-mqtt-dbus-client"
}

## Installation

Clone the repository and build the binary:

```bash
go install github.com/dgalli1/mqtt-dbus-ha-kde@latest
```


Test if everything works as expected:

```bash
go-mqtt-dbus-ha
```

if you don't have go setup correctly you can also run the binary directly:

```bash
~/go/bin/go-mqtt-dbus-ha
```

## Systemd Service

Create a systemd user service file at `~/.config/systemd/user/go-mqtt-dbus-ha.service` with the following content:

```ini
[Unit]
Description=MQTT D-Bus Home Assistant Integration
After=network.target

[Service]
ExecStart=/usr/local/bin/go-mqtt-dbus-ha
Restart=on-failure
Environment="DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%U/bus"

[Install]
WantedBy=default.target
```

Enable and start the service:

```bash
systemctl --user enable go-mqtt-dbus-ha.service
systemctl --user start go-mqtt-dbus-ha.service
```


## Homeassistant

Now you can create a new Automation with an external lux sensor or anything else to change the brightness of your screen.


This is en example configuration that works for me, you can adjust the values 2.8 to something depending on what value you want the brightness to scale.

```yaml
alias: Dynamic Screen Brightness
description: ""
triggers:
  - entity_id: sensor.tuya_rain_illuminance_average_20min
    trigger: state
conditions: []
actions:
  - target:
      entity_id: light.screen_brightness
    data:
      brightness: >
        {% set lux = states('sensor.tuya_rain_illuminance_average_20min') |
        float(default=0) %} {% set min_lux = 0 %} {% set max_lux = 5000 %} {%
        set min_brightness = 0 %} {% set max_brightness = 255 %} {% set
        clamped_lux = [max_lux, [min_lux, lux] | max] | min %}

        {% set adjusted_brightness = (
          min_brightness +
          (max_brightness - min_brightness) *
          ((clamped_lux / max_lux) ** 2.8)
        ) | round(0) %}

        {{ [max_brightness, [min_brightness, adjusted_brightness] | max] | min
        }}
    action: light.turn_on
mode: single
```