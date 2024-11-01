package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/godbus/dbus/v5"
)

const (
	// Config file path segments
	configDir  = ".config"
	configFile = "go-mqtt-dbus.json"

	// DBus Service
	dbusScreenBrightnessService = "org.kde.ScreenBrightness"

	// DBus Paths
	dbusScreenBrightnessPath = "/org/kde/ScreenBrightness"
	dbusDisplayPathPrefix    = "/org/kde/ScreenBrightness/"

	// DBus Properties
	dbusDisplaysNamesProperty = "org.kde.ScreenBrightness.DisplaysDBusNames"
	dbusBrightnessProperty    = "org.kde.ScreenBrightness.Display.Brightness"
	dbusMaxBrightnessProperty = "org.kde.ScreenBrightness.Display.MaxBrightness"
	dbusLabelProperty         = "org.kde.ScreenBrightness.Display.Label"

	// DBus Methods
	dbusSetBrightnessMethod = "org.kde.ScreenBrightness.Display.SetBrightness"

	// DBus Signals
	BrightnessChanged = "BrightnessChanged"
	DisplayAdded      = "DisplayAdded"
	DisplayRemoved    = "DisplayRemoved"
)

type Config struct {
	MQTTBroker                    string            `json:"mqtt_broker"`
	MQTTPort                      int               `json:"mqtt_port"`
	ClientID                      string            `json:"client_id"`
	HomeAssistantPropertyIDsRegex map[string]string `json:"homeassistant_property_ids_regex"`
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
	Name          string // DBus display name (e.g., "display0")
	PropertyName  string // Property name from config
	Label         string // Display label
	MaxBrightness int32  // Maximum brightness value
}

var displayMappings []DisplayInfo

func getBrightness(conn *dbus.Conn, display *DisplayInfo) (int32, error) {

	obj := conn.Object(dbusScreenBrightnessService, dbus.ObjectPath(dbusDisplayPathPrefix+display.Name))
	variant, err := obj.GetProperty(dbusBrightnessProperty)
	if err != nil {
		return 0, fmt.Errorf("getting brightness property: %w", err)
	}

	brightness, ok := variant.Value().(int32)
	if !ok {
		return 0, fmt.Errorf("unexpected type for brightness property: %T", variant.Value())
	}

	return brightness, nil
}

func setBrightness(conn *dbus.Conn, brightness int32, display *DisplayInfo) error {
	obj := conn.Object(dbusScreenBrightnessService, dbus.ObjectPath(dbusDisplayPathPrefix+display.Name))
	// Use correct method name and signature: void SetBrightness(int brightness, uint flags)
	call := obj.Call(dbusSetBrightnessMethod, 0, brightness, uint32(1))
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

func scaleBrightnessToHA(brightness int32, display *DisplayInfo) int {
	// Convert from 0-10000 to 0-255
	return int((float64(brightness) / float64(display.MaxBrightness)) * 255)
}

func scaleBrightnessFromHA(brightness int, display *DisplayInfo) int32 {
	// Convert from 0-255 to 0-10000
	return int32((float64(brightness) / 255.0) * float64(display.MaxBrightness))
}

func publishBrightness(client mqtt.Client, brightness int32, display *DisplayInfo) {
	state := LightState{
		Brightness: scaleBrightnessToHA(brightness, display),
		State:      "ON",
	}
	log.Printf("Scaled brightness to HA: %d", scaleBrightnessToHA(brightness, display))
	if brightness == 0 {
		state.State = "OFF"
	}

	payload, err := json.Marshal(state)
	if err != nil {
		log.Printf("Error marshaling state: %v", err)
		return
	}

	topic := "homeassistant/light/" + display.PropertyName + "/state"
	client.Publish(topic, 0, true, payload)
}

func publishConfig(client mqtt.Client) {
	// Publish config for each display
	for _, display := range displayMappings {
		configTopic := fmt.Sprintf("homeassistant/light/%s/config", display.PropertyName)
		device := LightDevice{
			Name:       fmt.Sprintf("%s Brightness", display.Label),
			UniqueID:   fmt.Sprintf("%s_brightness", display.PropertyName),
			BaseTopic:  fmt.Sprintf("homeassistant/light/%s", display.PropertyName),
			CommandT:   "~/set",
			StateT:     "~/state",
			Schema:     "json",
			Brightness: true,
		}

		if payload, err := json.Marshal(device); err == nil {
			client.Publish(configTopic, 0, true, payload)
		}
	}
}

func setupMQTTHandlers(client mqtt.Client, conn *dbus.Conn) {
	// Set up the message handler for brightness commands
	for _, display := range displayMappings {
		client.Subscribe("homeassistant/light/"+display.PropertyName+"/set", 0, func(client mqtt.Client, msg mqtt.Message) {
			displayRequestName := regexp.MustCompile(`homeassistant/light/(.*?)/set`).FindStringSubmatch(msg.Topic())[1]
			var currentDisplay DisplayInfo
			for _, d := range displayMappings {
				if d.PropertyName == displayRequestName {
					currentDisplay = d
					break
				}
			}
			var cmd LightState
			if err := json.Unmarshal(msg.Payload(), &cmd); err != nil {
				log.Printf("Error parsing command: %v", err)
				return
			}
			log.Printf("Received command for %s: %+v", currentDisplay.PropertyName, cmd)

			if cmd.State == "OFF" {
				setBrightness(conn, 0, &currentDisplay)
			} else {
				setBrightness(conn, scaleBrightnessFromHA(cmd.Brightness, &currentDisplay), &currentDisplay)
			}
		})
	}
}

func getDisplays(conn *dbus.Conn, config *Config) error {
	// qdbus org.kde.ScreenBrightness /org/kde/ScreenBrightness org.kde.ScreenBrightness.DisplaysDBusNames
	displayMappings = []DisplayInfo{} // Reset mappings

	obj := conn.Object(dbusScreenBrightnessService, dbus.ObjectPath(dbusScreenBrightnessPath))
	variant, err := obj.GetProperty(dbusDisplaysNamesProperty)
	if err != nil {
		return fmt.Errorf("getting display names property: %w", err)
	}

	displayNames, ok := variant.Value().([]string)
	if !ok {
		return fmt.Errorf("unexpected type for display names: %T", variant.Value())
	}

	for _, name := range displayNames {
		// qdbus org.kde.ScreenBrightness /org/kde/ScreenBrightness/display11 org.kde.ScreenBrightness.Display.Label
		obj := conn.Object(dbusScreenBrightnessService, dbus.ObjectPath(dbusDisplayPathPrefix+name))
		label, err := obj.GetProperty(dbusLabelProperty)
		if err != nil {
			return fmt.Errorf("getting display label property: %w", err)
		}

		labelStr, ok := label.Value().(string)
		if !ok {
			continue
		}
		maxBrightness, err2 := obj.GetProperty(dbusMaxBrightnessProperty)
		if err2 != nil {
			return fmt.Errorf("getting display label property: %w", err)
		}
		found := false

		// Check each regex pattern from config
		for propertyName, pattern := range config.HomeAssistantPropertyIDsRegex {
			if regexp.MustCompile(pattern).MatchString(labelStr) {
				displayMappings = append(displayMappings, DisplayInfo{
					Name:          name,
					PropertyName:  propertyName,
					Label:         labelStr,
					MaxBrightness: maxBrightness.Value().(int32),
				})
				found = true
			}
		}
		if !found {
			fmt.Printf("No match found for display %s with label \"%s\"\n",
				name, labelStr)
		}
	}
	for _, display := range displayMappings {
		fmt.Printf("Display: %s, Property: %s, Label: %s\n", display.Name, display.PropertyName, display.Label)
	}
	return nil
}

func publishOffForNonexistentDisplays(client mqtt.Client, config *Config) {

	// Check for any displays defined in config but not found
	for propertyName := range config.HomeAssistantPropertyIDsRegex {
		found := false
		for _, display := range displayMappings {
			if display.PropertyName == propertyName {
				found = true
				break
			}
		}
		if !found {
			state := LightState{
				Brightness: 0,
				State:      "OFF",
			}
			payload, err := json.Marshal(state)
			if err != nil {
				log.Printf("Error marshaling state: %v", err)
			} else {
				topic := "homeassistant/light/" + propertyName + "/state"
				client.Publish(topic, 0, true, payload)
			}
		}
	}
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
			for _, display := range displayMappings {
				if brightness, err := getBrightness(conn, &display); err == nil {
					publishBrightness(client, brightness, &display)
				}
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
		for _, display := range displayMappings {
			if brightness, err := getBrightness(conn, &display); err == nil {
				publishBrightness(client, brightness, &display)
			}
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

func getConfigPath() string {
	// Check if XDG_CONFIG_HOME is set
	configPath := os.Getenv("XDG_CONFIG_HOME")
	if configPath == "" {
		// If not, default to ~/.config
		homeDir, err := os.UserHomeDir()
		if err != nil {
			// Handle the error if the home directory cannot be determined
			log.Fatal("Could not determine home directory: " + err.Error())
		}
		configPath = filepath.Join(homeDir, configDir)
	}
	return configPath
}

func main() {
	// Then in the main function where the $SELECTION_PLACEHOLDER$ is:
	config, err := loadConfig(filepath.Join(getConfigPath(), configFile))
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

	// Publish OFF state for any displays defined in config but not found
	publishOffForNonexistentDisplays(client, config)

	// Subscribe to brightness changes via DBus
	signal := make(chan *dbus.Signal, 10)
	conn.Signal(signal)
	signals := []string{BrightnessChanged, DisplayAdded, DisplayRemoved}
	for _, signal := range signals {
		if err = conn.AddMatchSignal(
			dbus.WithMatchInterface(dbusScreenBrightnessService),
			dbus.WithMatchMember(signal),
			dbus.WithMatchObjectPath(dbusScreenBrightnessPath),
		); err != nil {
			log.Fatal(err)
		}
	}
	for v := range signal {
		if v.Name != dbusScreenBrightnessService+"."+BrightnessChanged {
			fmt.Println("Closing Service because Displays changed, it should be restarted by systemd")
			os.Exit(1)
		}
		displayName := v.Body[0]
		brightness := v.Body[1].(int32)

		// Find the display that changed
		var display *DisplayInfo
		for _, d := range displayMappings {
			if d.Name == displayName {
				display = &d
				break
			}
		}
		publishBrightness(client, brightness, display)

	}

	// Keep the program running
	select {}
}
