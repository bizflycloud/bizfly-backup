package testlib

import "os"

func MqttUrl() string {
	if os.Getenv("GITHUB_ACTION") != "" {
		return "mqtt://mqtt.fluux.io:1883"
	}
	return "mqtt://foo:bar@localhost:1883"
}
