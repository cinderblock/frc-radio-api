// This file is specific to the access point version of the API.
//go:build !robot

package radio

import (
	"fmt"
	"github.com/digineo/go-uci"
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	// Maximum number of times to retry configuring the radio.
	maxRetryCount = 3
)

// Radio holds the current state of the access point's configuration and any robot radios connected to it.
type Radio struct {
	// 5GHz or 6GHz channel number the radio is broadcasting on.
	Channel int `json:"channel"`

	// Channel bandwidth mode for the radio to use. Valid values are "20MHz" and "40MHz".
	ChannelBandwidth string `json:"channelBandwidth"`

	// VLANs to use for the teams of the red alliance. Valid values are "10_20_30", "40_50_60", and "70_80_90".
	RedVlans AllianceVlans `json:"redVlans"`

	// VLANs to use for the teams of the blue alliance. Valid values are "10_20_30", "40_50_60", and "70_80_90".
	BlueVlans AllianceVlans `json:"blueVlans"`

	// Enum representing the current configuration stage of the radio.
	Status radioStatus `json:"status"`

	// Map of team station names to their current status.
	StationStatuses map[string]*NetworkStatus `json:"stationStatuses"`

	// IP address of the syslog server to send logs to (via UDP on port 514).
	SyslogIpAddress string `json:"syslogIpAddress"`

	// Version of the radio software.
	Version string `json:"version"`

	// Queue for receiving and buffering configuration requests.
	ConfigurationRequestChannel chan ConfigurationRequest `json:"-"`

	// Hardware type of the radio.
	Type RadioType `json:"-"`

	// Name of the radio's Wi-Fi device, dependent on the hardware type.
	device string

	// Map of team station names to their Wi-Fi interface names, dependent on the hardware type.
	stationInterfaces map[station]string
}

// AllianceVlans represents which three VLANs are used for the teams of an alliance.
type AllianceVlans string

const (
	Vlans102030 AllianceVlans = "10_20_30"
	Vlans405060 AllianceVlans = "40_50_60"
	Vlans708090 AllianceVlans = "70_80_90"
)

// NewRadio creates a new Radio instance and initializes its fields to default values.
func NewRadio() *Radio {
	radio := Radio{
		RedVlans:                    Vlans102030,
		BlueVlans:                   Vlans405060,
		Status:                      statusBooting,
		ConfigurationRequestChannel: make(chan ConfigurationRequest, configurationRequestBufferSize),
	}
	radio.determineAndSetType()
	if radio.Type == TypeUnknown {
		log.Fatal("Unable to determine radio hardware type; exiting.")
	}
	log.Printf("Detected radio hardware type: %v", radio.Type)
	radio.determineAndSetVersion()

	// Initialize the device and station interface names that are dependent on the hardware type.
	switch radio.Type {
	case TypeLinksys:
		radio.device = "radio0"
		radio.stationInterfaces = map[station]string{
			red1:  "wlan0",
			red2:  "wlan0-1",
			red3:  "wlan0-2",
			blue1: "wlan0-3",
			blue2: "wlan0-4",
			blue3: "wlan0-5",
		}
	case TypeVividHosting:
		radio.device = "wifi1"
		radio.stationInterfaces = map[station]string{
			red1:  "ath1",
			red2:  "ath11",
			red3:  "ath12",
			blue1: "ath13",
			blue2: "ath14",
			blue3: "ath15",
		}
	}

	radio.StationStatuses = make(map[string]*NetworkStatus)
	for station := red1; station <= blue3; station++ {
		radio.StationStatuses[station.String()] = nil
	}

	return &radio
}

// getStationVlan returns the VLAN number for the given team station.
func (radio *Radio) getStationVlan(station station) int {
	var vlans AllianceVlans
	var position int
	if station == red1 || station == red2 || station == red3 {
		vlans = radio.RedVlans
		position = int(station) - int(red1) + 1
	} else if station == blue1 || station == blue2 || station == blue3 {
		vlans = radio.BlueVlans
		position = int(station) - int(blue1) + 1
	}

	switch vlans {
	case Vlans102030:
		return 10 * position
	case Vlans405060:
		return 10 * (position + 3)
	case Vlans708090:
		return 10 * (position + 6)
	default:
		// Invalid station.
		return -1
	}
}

// determineAndSetType determines the model of the radio.
func (radio *Radio) determineAndSetType() {
	model, _ := uciTree.GetLast("system", "@system[0]", "model")
	if strings.Contains(model, "VH") {
		radio.Type = TypeVividHosting
	} else {
		radio.Type = TypeLinksys
	}
}

// isStarted returns true if the Wi-Fi interface is up and running.
func (radio *Radio) isStarted() bool {
	_, err := shell.runCommand("iwinfo", radio.stationInterfaces[blue3], "info")
	return err == nil
}

// setInitialState initializes the in-memory state to match the radio's current configuration.
func (radio *Radio) setInitialState() {
	channel, _ := uciTree.GetLast("wireless", radio.device, "channel")
	radio.Channel, _ = strconv.Atoi(channel)
	htmode, _ := uciTree.GetLast("wireless", radio.device, "htmode")
	switch htmode {
	case "HT20":
		radio.ChannelBandwidth = "20MHz"
	case "HT40":
		radio.ChannelBandwidth = "40MHz"
	default:
		radio.ChannelBandwidth = "INVALID"
	}
	_ = radio.updateStationStatuses()

	radio.SyslogIpAddress, _ = uciTree.GetLast("system", "@system[0]", "log_ip")
}

// configure configures the radio with the given configuration.
func (radio *Radio) configure(request ConfigurationRequest) error {
	if request.Channel > 0 {
		uciTree.SetType("wireless", radio.device, "channel", uci.TypeOption, strconv.Itoa(request.Channel))
		radio.Channel = request.Channel
	}
	if request.ChannelBandwidth != "" {
		var htmode string
		switch request.ChannelBandwidth {
		case "20MHz":
			htmode = "HT20"
		case "40MHz":
			htmode = "HT40"
		default:
			return fmt.Errorf("invalid channel bandwidth: %s", request.ChannelBandwidth)
		}
		uciTree.SetType("wireless", radio.device, "htmode", uci.TypeOption, htmode)
		radio.ChannelBandwidth = request.ChannelBandwidth
	}
	if request.RedVlans != "" && request.BlueVlans != "" {
		radio.RedVlans = request.RedVlans
		radio.BlueVlans = request.BlueVlans
	}
	if request.SyslogIpAddress != "" {
		uciTree.SetType("system", "@system[0]", "log_ip", uci.TypeOption, request.SyslogIpAddress)
		if err := uciTree.Commit(); err != nil {
			return fmt.Errorf("failed to commit system configuration: %v", err)
		}
		radio.SyslogIpAddress = request.SyslogIpAddress
		if _, err := shell.runCommand("/etc/init.d/log", "restart"); err != nil {
			return fmt.Errorf("failed to restart syslog service: %v", err)
		}
	}

	if radio.Type == TypeLinksys {
		// Clear the state of the radio before loading teams; the Linksys AP is crash-prone otherwise.
		if err := radio.configureStations(map[string]*StationConfiguration{}); err != nil {
			return err
		}
		time.Sleep(wifiReloadBackoffDuration)
	}
	return radio.configureStations(request.StationConfigurations)
}

// configureStations configures the access point with the given team station configurations.
func (radio *Radio) configureStations(stationConfigurations map[string]*StationConfiguration) error {
	retryCount := 1

	for {
		// Only configure stations that are in the request
		for stationName, config := range stationConfigurations {
			// Skip stations that are being unconfigured (config is nil)
			if config == nil {
				continue
			}

			// Convert station name to station enum
			var station station
			for s := red1; s <= blue3; s++ {
				if s.String() == stationName {
					station = s
					break
				}
			}

			position := int(station) + 1
			wifiInterface := fmt.Sprintf("@wifi-iface[%d]", position)

			// Set the new configuration
			uciTree.SetType("wireless", wifiInterface, "ssid", uci.TypeOption, config.Ssid)
			uciTree.SetType("wireless", wifiInterface, "key", uci.TypeOption, config.WpaKey)
			if radio.Type == TypeVividHosting {
				uciTree.SetType("wireless", wifiInterface, "sae_password", uci.TypeOption, config.WpaKey)
			}
			vlan := fmt.Sprintf("vlan%d", radio.getStationVlan(station))
			uciTree.SetType("wireless", wifiInterface, "network", uci.TypeOption, vlan)
		}

		// Commit all changes at once
		if err := uciTree.Commit(); err != nil {
			return fmt.Errorf("failed to commit wireless configuration: %v", err)
		}

		if _, err := shell.runCommand("wifi", "reload", radio.device); err != nil {
			return fmt.Errorf("failed to reload configuration for device %s: %v", radio.device, err)
		}
		time.Sleep(wifiReloadBackoffDuration)

		err := radio.updateStationStatuses()
		if err != nil {
			return fmt.Errorf("error updating station statuses: %v", err)
		}

		if radio.stationSsidsAreCorrect(stationConfigurations) {
			return nil
		}

		if retryCount >= maxRetryCount {
			return fmt.Errorf("failed to configure stations after %d attempts", retryCount)
		}
		retryCount++
		time.Sleep(wifiReloadBackoffDuration)
	}
}

// updateStationStatuses fetches the current Wi-Fi status (SSID, WPA key, etc.) for each team station and updates the
// in-memory state.
func (radio *Radio) updateStationStatuses() error {
	for station := red1; station <= blue3; station++ {
		ssid, err := getSsid(radio.stationInterfaces[station])
		if err != nil {
			return err
		}
		if strings.HasPrefix(ssid, "no-team-") {
			radio.StationStatuses[station.String()] = nil
		} else {
			var status NetworkStatus
			status.Ssid = ssid
			status.HashedWpaKey, status.WpaKeySalt = radio.getHashedWpaKeyAndSalt(int(station) + 1)
			radio.StationStatuses[station.String()] = &status
		}
	}

	return nil
}

// stationSsidsAreCorrect returns true if the configured networks as read from the access point match the requested
// configuration.
func (radio *Radio) stationSsidsAreCorrect(stationConfigurations map[string]*StationConfiguration) bool {
	for stationName, stationStatus := range radio.StationStatuses {
		if config, ok := stationConfigurations[stationName]; ok {
			if stationStatus == nil || stationStatus.Ssid != config.Ssid {
				return false
			}
		} else {
			if stationStatus != nil {
				// This is an error case; we expect the station status to be nil if the station is not configured.
				return false
			}
		}
	}

	return true
}

// updateMonitoring polls the access point for the current bandwidth usage and link state of each team station and
// updates the in-memory state.
func (radio *Radio) updateMonitoring() {
	for station := red1; station <= blue3; station++ {
		stationStatus := radio.StationStatuses[station.String()]
		if stationStatus == nil {
			// Skip stations that don't have a team assigned.
			continue
		}

		stationStatus.updateMonitoring(radio.stationInterfaces[station])
	}
}
