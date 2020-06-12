package testlib

import "os"

func GetMqttUrl() string {
	if os.Getenv("GITHUB_ACTION") != "" {
		return "mqtt://test.mosquitto.org:1883"
	}
	return "mqtt://foo:bar@localhost:1883"
}
