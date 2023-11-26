package radio

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/digineo/go-uci"
	"log"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	bootPollIntervalSec            = 3
	statusPollIntervalSec          = 5
	configurationRequestBufferSize = 10
	linksysWifiReloadBackoffSec    = 5
	retryBackoffSec                = 3
	saltCharacters                 = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	saltLength                     = 16
	monitoringErrorCode            = -999
)

// Radio holds the current state of the access point's configuration and any robot radios connected to it.
type Radio struct {
	Channel                     int                       `json:"channel"`
	Status                      radioStatus               `json:"status"`
	StationStatuses             map[string]*StationStatus `json:"stationStatuses"`
	Type                        radioType                 `json:"-"`
	ConfigurationRequestChannel chan ConfigurationRequest `json:"-"`
	device                      string
	stationInterfaces           map[station]string
}

// radioType represents the hardware type of the radio.
//
//go:generate stringer -type=radioType
type radioType int

const (
	typeUnknown radioType = iota
	typeLinksys
	typeVividHosting
)

// radioStatus represents the configuration stage of the radio.
type radioStatus string

const (
	statusBooting     radioStatus = "BOOTING"
	statusConfiguring             = "CONFIGURING"
	statusActive                  = "ACTIVE"
	statusError                   = "ERROR"
)

var uciTree = uci.NewTree(uci.DefaultTreePath)
var shell shellWrapper = execShell{}
var ssidRe = regexp.MustCompile("ESSID: \"([-\\w ]*)\"")
var linksysWifiReloadBackoffDuration = time.Second * linksysWifiReloadBackoffSec
var retryBackoffDuration = time.Second * retryBackoffSec

// NewRadio creates a new Radio instance and initializes its fields to default values.
func NewRadio() *Radio {
	radio := Radio{
		Status:                      statusBooting,
		ConfigurationRequestChannel: make(chan ConfigurationRequest, configurationRequestBufferSize),
	}
	radio.determineAndSetType()
	if radio.Type == typeUnknown {
		log.Fatal("Unable to determine radio hardware type; exiting.")
	}
	log.Printf("Detected radio hardware type: %v", radio.Type)

	switch radio.Type {
	case typeLinksys:
		radio.device = "radio0"
		radio.stationInterfaces = map[station]string{
			red1:  "wlan0",
			red2:  "wlan0-1",
			red3:  "wlan0-2",
			blue1: "wlan0-3",
			blue2: "wlan0-4",
			blue3: "wlan0-5",
		}
	case typeVividHosting:
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

	radio.StationStatuses = make(map[string]*StationStatus)
	for i := 0; i < int(stationCount); i++ {
		radio.StationStatuses[station(i).String()] = nil
	}

	return &radio
}

// Run loops indefinitely, handling configuration requests and polling the Wi-Fi status.
func (radio *Radio) Run() {
	for !radio.isStarted() {
		log.Println("Waiting for radio to finish starting up...")
		time.Sleep(bootPollIntervalSec * time.Second)
	}
	log.Println("Radio ready with baseline Wi-Fi configuration.")

	// Initialize the in-memory state to match the radio's current configuration.
	channel, _ := uciTree.GetLast("wireless", radio.device, "channel")
	radio.Channel, _ = strconv.Atoi(channel)
	_ = radio.updateStationStatuses()
	radio.Status = statusActive

	for {
		// Check if there are any pending configuration requests; if not, periodically poll Wi-Fi status.
		select {
		case request := <-radio.ConfigurationRequestChannel:
			_ = radio.handleConfigurationRequest(request)
		case <-time.After(time.Second * statusPollIntervalSec):
			radio.updateStationMonitoring()
		}
	}
}

// determineAndSetType determines the model of the radio.
func (radio *Radio) determineAndSetType() {
	model, _ := uciTree.GetLast("system", "@system[0]", "model")
	if strings.Contains(model, "VH") {
		radio.Type = typeVividHosting
	} else {
		radio.Type = typeLinksys
	}
}

// isStarted returns true if the Wi-Fi interface is up and running.
func (radio *Radio) isStarted() bool {
	_, err := shell.runCommand("iwinfo", radio.stationInterfaces[blue3], "info")
	return err == nil
}

func (radio *Radio) handleConfigurationRequest(request ConfigurationRequest) error {
	// If there are multiple requests queued up, only consider the latest one.
	numExtraRequests := len(radio.ConfigurationRequestChannel)
	for i := 0; i < numExtraRequests; i++ {
		request = <-radio.ConfigurationRequestChannel
	}

	radio.Status = statusConfiguring
	log.Printf("Processing configuration request: %+v", request)
	if err := radio.configure(request); err != nil {
		log.Printf("Error configuring radio: %v", err)
		radio.Status = statusError
		return err
	} else if len(radio.ConfigurationRequestChannel) == 0 {
		radio.Status = statusActive
	}
	return nil
}

// configure configures the radio with the given configuration.
func (radio *Radio) configure(request ConfigurationRequest) error {
	if request.Channel > 0 {
		uciTree.SetType("wireless", radio.device, "channel", uci.TypeOption, strconv.Itoa(request.Channel))
		radio.Channel = request.Channel
	}

	if radio.Type == typeLinksys {
		// Clear the state of the radio before loading teams; the Linksys AP is crash-prone otherwise.
		if err := radio.configureStations(map[string]StationConfiguration{}); err != nil {
			return err
		}
		time.Sleep(linksysWifiReloadBackoffDuration)
	}
	return radio.configureStations(request.StationConfigurations)
}

// configureStations configures the access point with the given team station configurations.
func (radio *Radio) configureStations(stationConfigurations map[string]StationConfiguration) error {
	retryCount := 1

	for {
		for stationIndex := 0; stationIndex < 6; stationIndex++ {
			position := stationIndex + 1
			var ssid, wpaKey string
			if config, ok := stationConfigurations[station(stationIndex).String()]; ok {
				ssid = config.Ssid
				wpaKey = config.WpaKey
			} else {
				ssid = fmt.Sprintf("no-team-%d", position)
				wpaKey = ssid
			}

			wifiInterface := fmt.Sprintf("@wifi-iface[%d]", position)
			uciTree.SetType("wireless", wifiInterface, "ssid", uci.TypeOption, ssid)
			uciTree.SetType("wireless", wifiInterface, "key", uci.TypeOption, wpaKey)
			if radio.Type == typeVividHosting {
				uciTree.SetType("wireless", wifiInterface, "sae_password", uci.TypeOption, wpaKey)
			}

			if err := uciTree.Commit(); err != nil {
				return fmt.Errorf("failed to commit wireless configuration: %v", err)
			}
		}

		if _, err := shell.runCommand("wifi", "reload", radio.device); err != nil {
			return fmt.Errorf("failed to reload configuration for device %s: %v", radio.device, err)
		}

		if radio.Type == typeLinksys {
			// The Linksys AP returns immediately after 'wifi reload' but may not have applied the configuration yet;
			// sleep for a bit to compensate. (The Vivid AP waits for the configuration to be applied before returning.)
			time.Sleep(linksysWifiReloadBackoffDuration)
		}
		err := radio.updateStationStatuses()
		if err != nil {
			return fmt.Errorf("error updating station statuses: %v", err)
		} else if radio.stationSsidsAreCorrect(stationConfigurations) {
			log.Printf("Successfully configured Wi-Fi after %d attempts.", retryCount)
			break
		}

		log.Printf("Wi-Fi configuration still incorrect after %d attempts; trying again.", retryCount)
		time.Sleep(retryBackoffDuration)
		retryCount++
	}

	return nil
}

// updateStationStatuses fetches the current Wi-Fi status (SSID, WPA key, etc.) for each team station and updates the
// in-memory state.
func (radio *Radio) updateStationStatuses() error {
	for station, stationInterface := range radio.stationInterfaces {
		output, err := shell.runCommand("iwinfo", stationInterface, "info")
		if err != nil {
			return fmt.Errorf("error getting iwinfo for interface %s: %v", stationInterface, err)
		} else {
			matches := ssidRe.FindStringSubmatch(output)
			if len(matches) > 0 {
				ssid := matches[1]
				if strings.HasPrefix(ssid, "no-team-") {
					radio.StationStatuses[station.String()] = nil
				} else {
					var status StationStatus
					status.Ssid = ssid
					status.HashedWpaKey, status.WpaKeySalt = radio.getHashedWpaKeyAndSalt(station)
					radio.StationStatuses[station.String()] = &status
				}
			} else {
				return fmt.Errorf(
					"error parsing iwinfo output for interface %s: %s", stationInterface, output,
				)
			}
		}
	}

	return nil
}

// stationSsidsAreCorrect returns true if the configured networks as read from the access point match the requested
// configuration.
func (radio *Radio) stationSsidsAreCorrect(stationConfigurations map[string]StationConfiguration) bool {
	for stationName, stationStatus := range radio.StationStatuses {
		if config, ok := stationConfigurations[stationName]; ok {
			if radio.StationStatuses[stationName] == nil || radio.StationStatuses[stationName].Ssid != config.Ssid {
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

// getHashedWpaKeyAndSalt fetches the WPA key for the given station and returns its hashed value and the salt used for
// hashing.
func (radio *Radio) getHashedWpaKeyAndSalt(station station) (string, string) {
	wpaKey, ok := uciTree.GetLast("wireless", fmt.Sprintf("@wifi-iface[%d]", station+1), "key")
	if !ok {
		return "", ""
	}
	// Generate a random string of 16 characters to use as the salt.
	saltBytes := make([]byte, saltLength)
	for i := 0; i < saltLength; i++ {
		saltBytes[i] = saltCharacters[rand.Intn(len(saltCharacters))]
	}
	salt := string(saltBytes)
	hash := sha256.New()
	hash.Write([]byte(wpaKey + salt))
	hashedWpaKey := hex.EncodeToString(hash.Sum(nil))

	return hashedWpaKey, salt
}

// updateStationMonitoring polls the access point for the current bandwidth usage and link state of each team station
// and updates the in-memory state.
func (radio *Radio) updateStationMonitoring() {
	for station, stationInterface := range radio.stationInterfaces {
		stationStatus := radio.StationStatuses[station.String()]
		if stationStatus == nil {
			// Skip stations that don't have a team assigned.
			continue
		}

		output, err := shell.runCommand("luci-bwc", "-i", stationInterface)
		if err != nil {
			log.Printf("Error running 'luci-bwc -i %s': %v", stationInterface, err)
			stationStatus.BandwidthUsedMbps = monitoringErrorCode
		} else {
			stationStatus.parseBandwidthUsed(output)
		}
		output, err = shell.runCommand("iwinfo", stationInterface, "assoclist")
		if err != nil {
			log.Printf("Error running 'iwinfo %s assoclist': %v", stationInterface, err)
			stationStatus.RxRateMbps = monitoringErrorCode
			stationStatus.TxRateMbps = monitoringErrorCode
			stationStatus.SignalNoiseRatio = monitoringErrorCode
		} else {
			stationStatus.parseAssocList(output)
		}
	}
}
