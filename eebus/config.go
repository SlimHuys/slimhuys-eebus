package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds the add-on options. Inside Home Assistant they come from
// /data/options.json; for local development the same fields can be set
// through environment variables (see loadConfig).
type Config struct {
	DeviceSKI       string `json:"device_ski"`
	Port            int    `json:"port"`
	MQTTHost        string `json:"mqtt_host"`
	MQTTPort        int    `json:"mqtt_port"`
	MQTTUsername    string `json:"mqtt_username"`
	MQTTPassword    string `json:"mqtt_password"`
	DiscoveryPrefix string `json:"discovery_prefix"`
	TopicPrefix     string `json:"topic_prefix"`
	PollInterval    int    `json:"poll_interval"`
	StaleAfter      int    `json:"stale_after"`
	Debug           bool   `json:"debug"`

	// A switched-on power limit ends by itself after this many minutes;
	// 0 = never. Protects against a forgotten automation.
	PowerLimitMaxMinutes int `json:"power_limit_max_minutes"`

	// Grid connection point data offered to the device (EEBUS MGCP), read
	// from Home Assistant. Power in W, positive = taking from the grid,
	// negative = feeding in. Energy counters in kWh are optional.
	GridPowerEntity          string `json:"grid_power_entity"`
	GridEnergyConsumedEntity string `json:"grid_energy_consumed_entity"`
	GridEnergyFeedInEntity   string `json:"grid_energy_feed_in_entity"`

	DataDir string `json:"-"`
}

const optionsFile = "/data/options.json"

func defaultConfig() Config {
	return Config{
		Port:            4713,
		MQTTPort:        1883,
		DiscoveryPrefix: "homeassistant",
		TopicPrefix:     "slimhuys_eebus",
		PollInterval:    300,
		StaleAfter:      720,

		PowerLimitMaxMinutes: 240,
		DataDir:              "./data",
	}
}

func loadConfig() (Config, error) {
	cfg := defaultConfig()

	if b, err := os.ReadFile(optionsFile); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", optionsFile, err)
		}
		cfg.DataDir = filepath.Dir(optionsFile)
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}

	applyEnv(&cfg)

	if cfg.MQTTHost == "" && os.Getenv("SUPERVISOR_TOKEN") != "" {
		if err := supervisorMQTT(&cfg); err != nil {
			return cfg, fmt.Errorf("no mqtt_host configured and the Supervisor has no MQTT service: %w", err)
		}
	}

	cfg.normalize()
	return cfg, cfg.validate()
}

func applyEnv(cfg *Config) {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	num := func(key string, dst *int) {
		if v, ok := os.LookupEnv(key); ok {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	str("EEBUS_DEVICE_SKI", &cfg.DeviceSKI)
	str("EEBUS_DATA_DIR", &cfg.DataDir)
	num("EEBUS_PORT", &cfg.Port)
	str("MQTT_HOST", &cfg.MQTTHost)
	num("MQTT_PORT", &cfg.MQTTPort)
	str("MQTT_USERNAME", &cfg.MQTTUsername)
	str("MQTT_PASSWORD", &cfg.MQTTPassword)
	num("EEBUS_POLL_INTERVAL", &cfg.PollInterval)
	num("EEBUS_STALE_AFTER", &cfg.StaleAfter)
	if os.Getenv("EEBUS_DEBUG") == "true" {
		cfg.Debug = true
	}
}

// supervisorMQTT reads broker credentials from the Supervisor services API.
// This only works when the broker is the Mosquitto add-on; an external broker
// has to be configured through the mqtt_* options.
func supervisorMQTT(cfg *Config) error {
	req, err := http.NewRequest(http.MethodGet, "http://supervisor/services/mqtt", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SUPERVISOR_TOKEN"))
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("supervisor returned %s", resp.Status)
	}
	var body struct {
		Data struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	cfg.MQTTHost = body.Data.Host
	cfg.MQTTPort = body.Data.Port
	cfg.MQTTUsername = body.Data.Username
	cfg.MQTTPassword = body.Data.Password
	return nil
}

func normalizeSKI(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "", ":", "", "-", "").Replace(s)
}

func (c *Config) normalize() {
	c.DeviceSKI = normalizeSKI(c.DeviceSKI)
	c.TopicPrefix = strings.Trim(c.TopicPrefix, "/")
	c.DiscoveryPrefix = strings.Trim(c.DiscoveryPrefix, "/")
	if c.PollInterval < 30 {
		c.PollInterval = 30
	}
	// A value is only stale once it missed at least two polls; anything
	// tighter would flap on a single slow reply.
	if minimum := 2*c.PollInterval + 60; c.StaleAfter < minimum {
		c.StaleAfter = minimum
	}
}

func (c *Config) validate() error {
	if c.MQTTHost == "" {
		return errors.New("mqtt_host is not set")
	}
	if c.DeviceSKI != "" && len(c.DeviceSKI) != 40 {
		return fmt.Errorf("device_ski must be 40 hex characters, got %d", len(c.DeviceSKI))
	}
	if c.TopicPrefix == "" || c.DiscoveryPrefix == "" {
		return errors.New("topic_prefix and discovery_prefix must not be empty")
	}
	return nil
}

func (c *Config) pollInterval() time.Duration { return time.Duration(c.PollInterval) * time.Second }
func (c *Config) staleAfter() time.Duration   { return time.Duration(c.StaleAfter) * time.Second }
