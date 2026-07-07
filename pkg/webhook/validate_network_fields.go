package webhook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var bridgeNameRegexp = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var interfaceTypeRegexp = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func rejectJSONUnsafeChars(field, value string) error {
	if strings.ContainsAny(value, "\"\\") {
		return fmt.Errorf("%s must not contain quotes or backslashes", field)
	}
	return nil
}

func validateCapabilities(capabilities string) error {
	if capabilities == "" {
		return nil
	}
	var caps map[string]bool
	if err := json.Unmarshal([]byte(capabilities), &caps); err != nil {
		return fmt.Errorf("capabilities must be a JSON object with boolean values, e.g. '{\"mac\": true}': %w", err)
	}
	return nil
}

func validateIPAM(ipam string) error {
	if ipam == "" {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(ipam), &obj); err != nil {
		return fmt.Errorf("ipam must be a valid JSON object: %w", err)
	}
	t, ok := obj["type"]
	if !ok {
		return fmt.Errorf("ipam must contain a \"type\" field")
	}
	s, ok := t.(string)
	if !ok {
		return fmt.Errorf("ipam \"type\" must be a string")
	}
	if s == "" {
		return fmt.Errorf("ipam \"type\" must not be empty")
	}
	return nil
}

func validateMetaPlugins(metaPlugins string) error {
	if metaPlugins == "" {
		return nil
	}

	var plugins []map[string]any
	// MetaPluginsConfig is a comma-separated fragment inserted into the
	// template-generated plugins array. Wrap it so each fragment is decoded and
	// validated as an object without accepting a nested JSON array.
	if err := json.Unmarshal([]byte("["+metaPlugins+"]"), &plugins); err != nil {
		return fmt.Errorf("metaPlugins must be one or more JSON plugin objects: %w", err)
	}
	if len(plugins) == 0 {
		return fmt.Errorf("metaPlugins must contain at least one plugin object")
	}

	for i, p := range plugins {
		t, ok := p["type"]
		if !ok {
			return fmt.Errorf("metaPlugins[%d] must contain a \"type\" field", i)
		}
		s, ok := t.(string)
		if !ok {
			return fmt.Errorf("metaPlugins[%d] \"type\" must be a string", i)
		}
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("metaPlugins[%d] \"type\" must not be empty", i)
		}
	}
	return nil
}

func validateLogFile(logFile string) error {
	if logFile == "" {
		return nil
	}
	return rejectJSONUnsafeChars("logFile", logFile)
}

func validateBridgeName(bridge string) error {
	if bridge == "" {
		return nil
	}
	if err := rejectJSONUnsafeChars("bridge", bridge); err != nil {
		return err
	}
	if len(bridge) > 15 {
		return fmt.Errorf("bridge name must be at most 15 characters")
	}
	if !bridgeNameRegexp.MatchString(bridge) {
		return fmt.Errorf("bridge name must start with an alphanumeric character and contain only alphanumeric characters, dots, underscores, or dashes")
	}
	return nil
}

func validateInterfaceType(interfaceType string) error {
	if interfaceType == "" {
		return nil
	}
	if err := rejectJSONUnsafeChars("interfaceType", interfaceType); err != nil {
		return err
	}
	if len(interfaceType) > 32 {
		return fmt.Errorf("interfaceType must be at most 32 characters")
	}
	if !interfaceTypeRegexp.MatchString(interfaceType) {
		return fmt.Errorf("interfaceType must contain only alphanumeric characters, underscores, or dashes")
	}
	return nil
}
