package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/godbus/dbus/v5"
)

type Config struct {
	MQTTBroker string `json:"mqtt_broker"`
	MQTTPort   int    `json:"mqtt_port"`
	ClientID   string `json:"client_id"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return &config, nil
}

type LightDevice struct {
	Name       string `json:"name"`
	UniqueID   string `json:"uniq_id"`
	CommandT   string `json:"cmd_t"`
	StateT     string `json:"stat_t"`
	Schema     string `json:"schema"`
	Brightness bool   `json:"brightness"`
	BaseTopic  string `json:"~"`
}

func getBrightness(conn *dbus.Conn) (int32, error) {

	obj := conn.Object("org.kde.ScreenBrightness", dbus.ObjectPath("/org/kde/ScreenBrightness/display0"))
	variant, err := obj.GetProperty("org.kde.ScreenBrightness.Display.Brightness")
	if err != nil {
		return 0, fmt.Errorf("getting brightness property: %w", err)
	}

	brightness, ok := variant.Value().(int32)
	print(brightness)
	if !ok {
		return 0, fmt.Errorf("unexpected type for brightness property: %T", variant.Value())
	}

	return brightness, nil
}

func setBrightness(conn *dbus.Conn, brightness int32) error {
	print(brightness)
	obj := conn.Object("org.kde.ScreenBrightness", dbus.ObjectPath("/org/kde/ScreenBrightness/display0"))
	call := obj.Call("org.kde.ScreenBrightness.setBrightness", 0, brightness)
	return call.Err
}

type LightState struct {
	Brightness int    `json:"brightness"`
	State      string `json:"state"`
}

func publishBrightness(client mqtt.Client, brightness int32) {
	state := LightState{
		Brightness: int(brightness),
		State:      "ON",
	}
	if brightness == 0 {
		state.State = "OFF"
	}

	payload, err := json.Marshal(state)
	if err != nil {
		log.Printf("Error marshaling state: %v", err)
		return
	}

	topic := "homeassistant/light/screen/state"
	client.Publish(topic, 0, true, payload)
}

func publishConfig(client mqtt.Client) {
	configTopic := "homeassistant/light/screen/config"
	device := LightDevice{
		Name:       "Screen Brightness",
		UniqueID:   "screen_brightness",
		BaseTopic:  "homeassistant/light/screen",
		CommandT:   "~/set",
		StateT:     "~/state",
		Schema:     "json",
		Brightness: true,
	}

	if payload, err := json.Marshal(device); err == nil {
		client.Publish(configTopic, 0, true, payload)
	}
}

func main() {
	// Load configuration
	config, err := loadConfig("/etc/go-mqtt-dbus.conf")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	// Connect to the session bus instead of system bus
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// MQTT client options
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", config.MQTTBroker, config.MQTTPort))
	opts.SetClientID(config.ClientID)

	// Create and start a client
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal(token.Error())
	}

	fmt.Println("Connected to MQTT broker")

	// Publish initial configuration
	publishConfig(client)

	// Start monitoring brightness

	// Get initial brightness and publish it
	brightness, err := getBrightness(conn)
	if err != nil {
		log.Fatal("Failed to get initial brightness:", err)
	}
	publishBrightness(client, brightness)

	// Subscribe to brightness changes
	signal := make(chan *dbus.Signal, 10)
	conn.Signal(signal)
	conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0,
		fmt.Sprintf("type='signal',interface='org.kde.ScreenBrightness',member='BrightnessChanged',path='/org/kde/ScreenBrightness'"))
	go func() {
		for range signal {
			brightness, err := getBrightness(conn)
			if err != nil {
				log.Println("Failed to get brightness:", err)
				continue
			}
			publishBrightness(client, brightness)
		}
	}()

	// Add message handler for brightness commands
	client.Subscribe("homeassistant/light/screen/set", 0, func(client mqtt.Client, msg mqtt.Message) {
		var cmd LightState
		if err := json.Unmarshal(msg.Payload(), &cmd); err != nil {
			log.Printf("Error parsing command: %v", err)
			return
		}

		if cmd.State == "OFF" {
			setBrightness(conn, 0)
		} else {
			setBrightness(conn, int32(cmd.Brightness))
		}
	})

	// Keep the program running
	select {}
}
