package testlib

import "os"

func MqttUrl() string {
	if os.Getenv("GITHUB_ACTION") != "" {
		return os.Getenv("MQTT_TEST_SERVER")
	}
	return "mqtt://foo:bar@localhost:1883"
}
