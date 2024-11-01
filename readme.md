# MQTT D-Bus Home Assistant Integration

A Go service that connects your Plasma 6 screen brightness controlle to Home Assistant via MQTT.



## Configuration

Create a configuration file at `~/.config/go-mqtt-dbus.conf` with the following JSON structure:
    
```json
{
    "mqtt_broker": "ip_of_your_mqtt_broker",
    "mqtt_port": 1883,
    "client_id": "mqtt-dbus-brightness-ha",
    "homeassistant_property_ids_regex": {
        "home_assistant_property_name": "Regex to match your Display Name",
    }
}
```

### Configuration Options
#### mqtt_broker:
Type: String

Purpose: The IP address or hostname of your MQTT broker (e.g., "192.168.1.100" or "homeassistant")

Example: "mqtt_broker": "192.168.1.10"

#### mqtt_port:
Type: Integer

Purpose: The port number on which the MQTT broker is listening

Default: 1883 (standard MQTT port)

Example: "mqtt_port": 1883

#### client_id:
Type: String

Purpose: A unique identifier for this MQTT client instance

Example: "client_id": "mqtt-dbus-brightness-ha"

Note: Should be unique across all clients connecting to your MQTT broker

#### homeassistant_property_ids_regex:
Type: Object (key-value pairs)

Purpose: Maps Home Assistant property names to regex patterns for matching display names

Structure:

Key: Home Assistant property name

Value: Regular expression pattern to match display names


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
ExecStart=%h/go/bin/go-mqtt-dbus-ha
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
A higher value will make the screen dimmer in darker environments and brighter in brighter environments.

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

## Known Bugs

- Im lazy and didn't implement a way to handle screen disconnects and connects the service will automaticly restart if a screen is disconnected aslong as you use systemd but it's kinda ugly.