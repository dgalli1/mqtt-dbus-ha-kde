package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/godbus/dbus/v5"
)

type Config struct {
	MQTTBroker                    string            `json:"mqtt_broker"`
	MQTTPort                      int               `json:"mqtt_port"`
	ClientID                      string            `json:"client_id"`
	HomeAssistantPropertyIDsRegex map[string]string `json:"homeassistant_property_ids_regex"`
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

type DisplayInfo struct {
	Name         string // DBus display name (e.g., "display0")
	PropertyName string // Property name from config
	Label        string // Display label
}

var displayMappings []DisplayInfo

func getBrightness(conn *dbus.Conn) (int32, error) {

	obj := conn.Object("org.kde.ScreenBrightness", dbus.ObjectPath("/org/kde/ScreenBrightness/display0"))
	variant, err := obj.GetProperty("org.kde.ScreenBrightness.Display.Brightness")
	if err != nil {
		return 0, fmt.Errorf("getting brightness property: %w", err)
	}

	brightness, ok := variant.Value().(int32)
	if !ok {
		return 0, fmt.Errorf("unexpected type for brightness property: %T", variant.Value())
	}

	return brightness, nil
}

func setBrightness(conn *dbus.Conn, brightness int32) error {
	obj := conn.Object("org.kde.ScreenBrightness", dbus.ObjectPath("/org/kde/ScreenBrightness/display0"))
	// Use correct method name and signature: void SetBrightness(int brightness, uint flags)
	call := obj.Call("org.kde.ScreenBrightness.Display.SetBrightness", 0, brightness, uint32(1))
	if call.Err != nil {
		log.Printf("Failed to set brightness: %v", call.Err)
		return call.Err
	}
	return nil
}

type LightState struct {
	Brightness int    `json:"brightness"`
	State      string `json:"state"`
}

func scaleBrightnessToHA(brightness int32) int {
	// Convert from 0-10000 to 0-255
	return int((float64(brightness) / 10000.0) * 255)
}

func scaleBrightnessFromHA(brightness int) int32 {
	// Convert from 0-255 to 0-10000
	return int32((float64(brightness) / 255.0) * 10000)
}

func publishBrightness(client mqtt.Client, brightness int32) {
	state := LightState{
		Brightness: scaleBrightnessToHA(brightness),
		State:      "ON",
	}
	log.Printf("Scaled brightness to HA: %d", scaleBrightnessToHA(brightness))
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

func setupMQTTHandlers(client mqtt.Client, conn *dbus.Conn) {
	// Set up the message handler for brightness commands
	client.Subscribe("homeassistant/light/screen/set", 0, func(client mqtt.Client, msg mqtt.Message) {
		var cmd LightState
		if err := json.Unmarshal(msg.Payload(), &cmd); err != nil {
			log.Printf("Error parsing command: %v", err)
			return
		}
		log.Printf("Received command: %+v", cmd)

		if cmd.State == "OFF" {
			setBrightness(conn, 0)
		} else {
			setBrightness(conn, scaleBrightnessFromHA(cmd.Brightness))
		}
	})
}

func getDisplays(conn *dbus.Conn, config *Config) error {
	// qdbus org.kde.ScreenBrightness /org/kde/ScreenBrightness org.kde.ScreenBrightness.DisplaysDBusNames
	displayMappings = []DisplayInfo{} // Reset mappings

	obj := conn.Object("org.kde.ScreenBrightness", dbus.ObjectPath("/org/kde/ScreenBrightness"))
	variant, err := obj.GetProperty("org.kde.ScreenBrightness.DisplaysDBusNames")
	if err != nil {
		return fmt.Errorf("getting display names property: %w", err)
	}

	displayNames, ok := variant.Value().([]string)
	if !ok {
		return fmt.Errorf("unexpected type for display names: %T", variant.Value())
	}

	for _, name := range displayNames {
		// qdbus org.kde.ScreenBrightness /org/kde/ScreenBrightness/display11 org.kde.ScreenBrightness.Display.Label
		obj := conn.Object("org.kde.ScreenBrightness", dbus.ObjectPath("/org/kde/ScreenBrightness/"+name))
		label, err := obj.GetProperty("org.kde.ScreenBrightness.Display.Label")
		if err != nil {
			return fmt.Errorf("getting display label property: %w", err)
		}

		labelStr, ok := label.Value().(string)
		if !ok {
			continue
		}

		// Check each regex pattern from config
		for propertyName, pattern := range config.HomeAssistantPropertyIDsRegex {
			if regexp.MustCompile(pattern).MatchString(labelStr) {
				fmt.Printf("Found match for %s with pattern %s\n", propertyName, pattern)
				displayMappings = append(displayMappings, DisplayInfo{
					Name:         name,
					PropertyName: propertyName,
					Label:        labelStr,
				})
			}
		}
	}
	for _, display := range displayMappings {
		fmt.Printf("Display: %s, Property: %s, Label: %s\n", display.Name, display.PropertyName, display.Label)
	}

	return nil
}

func initializeMQTT(config *Config, conn *dbus.Conn) mqtt.Client {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", config.MQTTBroker, config.MQTTPort))
	opts.SetClientID(config.ClientID)

	// Add connection lost handler
	opts.SetConnectionLostHandler(func(client mqtt.Client, err error) {
		log.Printf("Connection lost: %v", err)
		// Attempt to reconnect
		for {
			if token := client.Connect(); token.Wait() && token.Error() != nil {
				log.Printf("Failed to reconnect: %v", token.Error())
				// Wait before retrying
				time.Sleep(5 * time.Second)
				continue
			}
			log.Println("Successfully reconnected to MQTT broker")
			// Republish config and current state after reconnection
			publishConfig(client)
			if brightness, err := getBrightness(conn); err == nil {
				publishBrightness(client, brightness)
			}
			// Reestablish subscriptions
			setupMQTTHandlers(client, conn)
			break
		}
	})

	// Add connect handler
	opts.SetOnConnectHandler(func(client mqtt.Client) {
		log.Println("Connected to MQTT broker")
		// Publish initial configuration and state
		publishConfig(client)
		if brightness, err := getBrightness(conn); err == nil {
			publishBrightness(client, brightness)
		}
		// Set up message handlers
		setupMQTTHandlers(client, conn)
	})

	// Create and connect client
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Fatal(token.Error())
	}
	return client
}

func main() {
	// Load configuration
	config, err := loadConfig("/etc/go-mqtt-dbus.conf")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	// Connect to the session bus
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	getDisplays(conn, config)
	// Initialize MQTT client with reconnection handling
	client := initializeMQTT(config, conn)

	// Subscribe to brightness changes via DBus
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
