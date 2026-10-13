package main

import "testing"

func TestNormalizeSKI(t *testing.T) {
	in := " 58:59:FE:43:44:72:F4:3B:FD:73:EE:53:12:2A:60:AB:25:62:6C:04 "
	if got, want := normalizeSKI(in), "5859fe434472f43bfd73ee53122a60ab25626c04"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStaleAfterIsAtLeastTwoPolls(t *testing.T) {
	c := defaultConfig()
	c.MQTTHost = "broker"
	c.PollInterval, c.StaleAfter = 300, 60
	c.normalize()
	if c.StaleAfter != 660 {
		t.Errorf("stale_after = %d, want 660", c.StaleAfter)
	}
	c.PollInterval = 5
	c.normalize()
	if c.PollInterval != 30 {
		t.Errorf("poll_interval = %d, want minimum 30", c.PollInterval)
	}
}

func TestValidate(t *testing.T) {
	c := defaultConfig()
	if c.validate() == nil {
		t.Error("missing mqtt_host must fail")
	}
	c.MQTTHost = "broker"
	c.DeviceSKI = "abc"
	if c.validate() == nil {
		t.Error("short SKI must fail")
	}
	c.DeviceSKI = ""
	if err := c.validate(); err != nil {
		t.Errorf("empty SKI (auto-discovery) must be valid: %v", err)
	}
}
