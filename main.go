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

type BrightnessSensor struct {
	Name        string `json:"name"`
	StateTopic  string `json:"state_topic"`
	UnitOfMeas  string `json:"unit_of_measurement"`
	DeviceClass string `json:"device_class"`
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

func publishBrightness(client mqtt.Client, brightness int32) {
	// Publish current brightness
	topic := "homeassistant/sensor/screen_brightness/state"
	client.Publish(topic, 0, true, fmt.Sprintf("%d", brightness))
}

func publishConfig(client mqtt.Client) {
	// Publish sensor configuration
	configTopic := "homeassistant/sensor/screen_brightness/config"
	sensor := BrightnessSensor{
		Name:        "Screen Brightness",
		StateTopic:  "homeassistant/sensor/screen_brightness/state",
		UnitOfMeas:  "%",
		DeviceClass: "illuminance",
	}

	if payload, err := json.Marshal(sensor); err == nil {
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

	// Keep the program running
	select {}
}
