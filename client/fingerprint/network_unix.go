// +build linux darwin

package fingerprint

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/client/config"
	"github.com/hashicorp/nomad/nomad/structs"
)

// NetworkFingerprint is used to fingerprint the Network capabilities of a node
type NetworkFingerprint struct {
	logger *log.Logger
}

// NewNetworkFingerprinter returns a new NetworkFingerprinter with the given
// logger
func NewNetworkFingerprinter(logger *log.Logger) Fingerprint {
	f := &NetworkFingerprint{logger: logger}
	return f
}

func (f *NetworkFingerprint) Fingerprint(cfg *config.Config, node *structs.Node) (bool, error) {
	// newNetwork is populated and addded to the Nodes resources
	newNetwork := &structs.NetworkResource{}
	var ip string

	interfaces, err := f.findInterfaces(cfg.NetworkInterface)
	if err != nil {
		f.logger.Println(fmt.Sprintf("[DEBUG] Error while detecting network interface during fingerprinting: %s", err.Error()))
		return false, err
	}

	if len(interfaces) == 0 {
		f.logger.Println("[DEBUG] No network interfaces were detected")
		return false, errors.New("Unable to find any interface")
	}

	// Use the first interface that we have detected.
	intf := interfaces[0]
	if ip, err = f.ipAddress(intf); err != nil {
		f.logger.Println("[DEBUG] Unable to find IP address of interface ", intf.Name)
		return false, err
	}

	newNetwork.Device = intf.Name
	node.Attributes["network.ip-address"] = ip
	newNetwork.IP = ip
	newNetwork.CIDR = newNetwork.IP + "/32"

	f.logger.Println("[DEBUG] Detected interface ", intf.Name, " with IP ", ip, " during fingerprinting")

	if throughput := f.linkSpeed(intf.Name); throughput > 0 {
		newNetwork.MBits = throughput
	} else {
		f.logger.Printf("[DEBUG] fingerprint.network: Unable to read link speed; setting to default %v", cfg.NetworkSpeed)
		newNetwork.MBits = cfg.NetworkSpeed
	}

	if node.Resources == nil {
		node.Resources = &structs.Resources{}
	}

	node.Resources.Networks = append(node.Resources.Networks, newNetwork)

	// return true, because we have a network connection
	return true, nil
}

// linkSpeed returns link speed in Mb/s, or 0 when unable to determine it.
func (f *NetworkFingerprint) linkSpeed(device string) int {
	// Use LookPath to find the ethtool in the systems $PATH
	// If it's not found or otherwise errors, LookPath returns and empty string
	// and an error we can ignore for our purposes
	ethtoolPath, _ := exec.LookPath("ethtool")
	if ethtoolPath != "" {
		if speed := f.linkSpeedEthtool(ethtoolPath, device); speed > 0 {
			return speed
		}
	}

	// Fall back on checking a system file for link speed.
	return f.linkSpeedSys(device)
}

// linkSpeedSys parses link speed in Mb/s from /sys.
func (f *NetworkFingerprint) linkSpeedSys(device string) int {
	path := fmt.Sprintf("/sys/class/net/%s/speed", device)

	// Read contents of the device/speed file
	content, err := ioutil.ReadFile(path)
	if err != nil {
		f.logger.Printf("[WARN] fingerprint.network: Unable to read link speed from %s", path)
		return 0
	}

	lines := strings.Split(string(content), "\n")
	mbs, err := strconv.Atoi(lines[0])
	if err != nil || mbs <= 0 {
		f.logger.Printf("[WARN] fingerprint.network: Unable to parse link speed from %s", path)
		return 0
	}

	return mbs
}

// linkSpeedEthtool determines link speed in Mb/s with 'ethtool'.
func (f *NetworkFingerprint) linkSpeedEthtool(path, device string) int {
	outBytes, err := exec.Command(path, device).Output()
	if err != nil {
		f.logger.Printf("[WARN] fingerprint.network: Error calling ethtool (%s %s): %v", path, device, err)
		return 0
	}

	output := strings.TrimSpace(string(outBytes))
	re := regexp.MustCompile("Speed: [0-9]+[a-zA-Z]+/s")
	m := re.FindString(output)
	if m == "" {
		// no matches found, output may be in a different format
		f.logger.Printf("[WARN] fingerprint.network: Unable to parse Speed in output of '%s %s'", path, device)
		return 0
	}

	// Split and trim the Mb/s unit from the string output
	args := strings.Split(m, ": ")
	raw := strings.TrimSuffix(args[1], "Mb/s")

	// convert to Mb/s
	mbs, err := strconv.Atoi(raw)
	if err != nil || mbs <= 0 {
		f.logger.Printf("[WARN] fingerprint.network: Unable to parse Mb/s in output of '%s %s'", path, device)
		return 0
	}

	return mbs
}

func (f *NetworkFingerprint) ipAddress(intf *net.Interface) (string, error) {
	var (
		addrs []net.Addr
		err   error
	)
	if addrs, err = intf.Addrs(); err != nil {
		return "", err
	}

	if len(addrs) == 0 {
		return "", errors.New(fmt.Sprintf("Interface %s has no IP address", intf.Name))
	}
	var ipV4 net.IP
	for _, addr := range addrs {
		var ip net.IP
		switch v := (addr).(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip.To4() != nil {
			ipV4 = ip
			break
		}
	}

	if ipV4 == nil {
		return "", errors.New(fmt.Sprintf("Couldn't parse IP address for interface %s with addr %s", intf.Name))
	}
	return ipV4.String(), nil

}

func (f *NetworkFingerprint) isDeviceEnabled(intf *net.Interface) bool {
	return intf.Flags&net.FlagUp != 0
}

func (f *NetworkFingerprint) deviceHasIpAddress(intf *net.Interface) bool {
	if addrs, err := intf.Addrs(); err == nil {
		return len(addrs) > 0
	}
	return false
}

func (n *NetworkFingerprint) isDeviceLoopBackOrPointToPoint(intf *net.Interface) bool {
	return intf.Flags&(net.FlagLoopback|net.FlagPointToPoint) == 0
}

func (f *NetworkFingerprint) findInterfaces(deviceName string) ([]*net.Interface, error) {
	var (
		interfaces []*net.Interface
		err        error
	)
	if deviceName != "" {
		if intf, err := net.InterfaceByName(deviceName); err == nil {
			interfaces = append(interfaces, intf)
		}
		return interfaces, err
	}

	var intfs []net.Interface

	if intfs, err = net.Interfaces(); err != nil {
		return nil, err
	}

	for _, intf := range intfs {
		if f.isDeviceEnabled(&intf) && f.isDeviceLoopBackOrPointToPoint(&intf) && f.deviceHasIpAddress(&intf) {
			interfaces = append(interfaces, &intf)
		}
	}

	return interfaces, nil
}
