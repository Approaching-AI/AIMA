package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/runtime"
)

const (
	minHostPort       = 1024
	maxHostPort       = 65535
	hostPortLabelBase = "aima.dev/host-port"
	portDialTimeout   = time.Second
)

// inFlightPorts tracks host ports that an in-flight deploy has already chosen but
// whose engine has not yet bound the socket. llama.cpp can take 40s+ to load a
// model before it listens, so during that window the port is neither reported by
// reservedHostPorts() (deployment metadata not persisted yet) nor detected by
// localPortAlive() (nothing is listening). Without this registry two near-
// simultaneous deploys both see the port as free and pick the same one, and the
// second engine fails to bind ("address already in use"). Entries are released
// once the deployment metadata is persisted, after which reservedHostPorts()
// covers the port instead. The mutex also serializes port selection across
// concurrent deploys.
var (
	inFlightPortsMu sync.Mutex
	inFlightPorts   = make(map[int]struct{})
)

// allocateDeploymentPorts resolves and reserves host ports for req. It returns a
// release func that the caller MUST invoke once the deployment metadata has been
// persisted (i.e. right after the runtime's Deploy returns, success or failure),
// which hands the reservation off to reservedHostPorts().
func allocateDeploymentPorts(
	ctx context.Context,
	owner string,
	runtimeName string,
	req *runtime.DeployRequest,
	provenance map[string]string,
	deployments []*runtime.DeploymentStatus,
) (func(), error) {
	noop := func() {}
	if req == nil {
		return noop, nil
	}

	bindings := requestPortBindings(req)
	if len(bindings) == 0 {
		applyPortLabels(runtimeName, req, nil)
		return noop, nil
	}

	hostIndexes := hostPortBindingIndexes(runtimeName, req, bindings)
	if len(hostIndexes) == 0 {
		applyPortLabels(runtimeName, req, bindings)
		return noop, nil
	}

	// Serialize selection and treat ports reserved by other in-flight deploys as
	// unavailable for the duration of the choice.
	inFlightPortsMu.Lock()
	defer inFlightPortsMu.Unlock()

	reservedPorts := reservedHostPorts(deployments, owner)
	for p := range inFlightPorts {
		reservedPorts[p] = struct{}{}
	}
	ownerPorts := ownerHostPorts(deployments, owner)
	selectedPorts := make(map[int]struct{}, len(hostIndexes))
	if req.Config == nil {
		req.Config = make(map[string]any)
	}

	for _, idx := range hostIndexes {
		binding := bindings[idx]
		explicit := strings.EqualFold(provenance[binding.ConfigKey], "L1")
		if !explicit {
			if reused, ok := ownerPorts[hostPortLabel(binding.ConfigKey)]; ok && reused >= minHostPort && reused <= maxHostPort {
				bindings[idx].Port = reused
				if binding.ConfigKey != "" {
					req.Config[binding.ConfigKey] = reused
				}
				selectedPorts[reused] = struct{}{}
				continue
			}
		}
		port, err := chooseHostPort(binding.Port, explicit, reservedPorts, selectedPorts)
		if err != nil {
			return noop, err
		}
		bindings[idx].Port = port
		if binding.ConfigKey != "" {
			req.Config[binding.ConfigKey] = port
		}
		selectedPorts[port] = struct{}{}
	}

	applyPortLabels(runtimeName, req, bindings)

	chosen := make([]int, 0, len(selectedPorts))
	for p := range selectedPorts {
		inFlightPorts[p] = struct{}{}
		chosen = append(chosen, p)
	}
	release := func() {
		inFlightPortsMu.Lock()
		defer inFlightPortsMu.Unlock()
		for _, p := range chosen {
			delete(inFlightPorts, p)
		}
	}
	return release, nil
}

func requestPortBindings(req *runtime.DeployRequest) []knowledge.PortBinding {
	if req == nil {
		return nil
	}
	bindings := knowledge.ResolvePortBindingsFromSpecs(req.PortSpecs, req.Config)
	if len(bindings) > 0 {
		return bindings
	}
	if req.Port > 0 {
		return []knowledge.PortBinding{{
			Name:      "http",
			Flag:      "--port",
			ConfigKey: "port",
			Port:      req.Port,
			Primary:   true,
		}}
	}
	return nil
}

func applyPortLabels(runtimeName string, req *runtime.DeployRequest, bindings []knowledge.PortBinding) {
	if req == nil {
		return
	}
	if req.Labels == nil {
		req.Labels = make(map[string]string)
	}

	for key := range req.Labels {
		if key == hostPortLabelBase || strings.HasPrefix(key, hostPortLabelBase+".") {
			delete(req.Labels, key)
		}
	}

	primaryPort := knowledge.PrimaryPort(bindings)
	if primaryPort == 0 {
		primaryPort = runtimePrimaryPort(req)
	}
	if primaryPort > 0 {
		req.Labels["aima.dev/port"] = strconv.Itoa(primaryPort)
	}

	for _, idx := range hostPortBindingIndexes(runtimeName, req, bindings) {
		binding := bindings[idx]
		if binding.Port <= 0 {
			continue
		}
		req.Labels[hostPortLabel(binding.ConfigKey)] = strconv.Itoa(binding.Port)
	}
}

func hostPortLabel(configKey string) string {
	if configKey == "" || configKey == "port" {
		return hostPortLabelBase
	}
	return hostPortLabelBase + "." + configKey
}

func reservedHostPorts(deployments []*runtime.DeploymentStatus, owner string) map[int]struct{} {
	reserved := make(map[int]struct{})
	for _, deployment := range deployments {
		if deployment == nil || deployment.Name == owner {
			continue
		}
		if deployment.Phase != "running" && deployment.Phase != "starting" {
			continue
		}
		usedLabels := false
		for key, value := range deployment.Labels {
			if key != hostPortLabelBase && !strings.HasPrefix(key, hostPortLabelBase+".") {
				continue
			}
			port, err := strconv.Atoi(value)
			if err != nil || port <= 0 {
				continue
			}
			reserved[port] = struct{}{}
			usedLabels = true
		}
		if usedLabels {
			continue
		}
		if deployment.Runtime != "native" && deployment.Runtime != "docker" {
			continue
		}
		if portValue, ok := deployment.Labels["aima.dev/port"]; ok {
			port, err := strconv.Atoi(portValue)
			if err == nil && port > 0 {
				reserved[port] = struct{}{}
			}
		}
	}
	return reserved
}

func ownerHostPorts(deployments []*runtime.DeploymentStatus, owner string) map[string]int {
	if owner == "" {
		return nil
	}
	ports := make(map[string]int)
	for _, deployment := range deployments {
		if deployment == nil || deployment.Name != owner {
			continue
		}
		for key, value := range deployment.Labels {
			if key != hostPortLabelBase && !strings.HasPrefix(key, hostPortLabelBase+".") {
				continue
			}
			port, err := strconv.Atoi(value)
			if err != nil || port <= 0 {
				continue
			}
			ports[key] = port
		}
		if _, ok := ports[hostPortLabelBase]; ok {
			continue
		}
		if portValue, ok := deployment.Labels["aima.dev/port"]; ok {
			port, err := strconv.Atoi(portValue)
			if err == nil && port > 0 {
				ports[hostPortLabelBase] = port
			}
		}
	}
	if len(ports) == 0 {
		return nil
	}
	return ports
}

func hostPortBindingIndexes(runtimeName string, req *runtime.DeployRequest, bindings []knowledge.PortBinding) []int {
	switch runtimeName {
	case "native":
		indexes := make([]int, 0, len(bindings))
		for i := range bindings {
			indexes = append(indexes, i)
		}
		return indexes
	case "docker":
		if req != nil && req.Container != nil && req.Container.NetworkMode == "host" {
			indexes := make([]int, 0, len(bindings))
			for i := range bindings {
				indexes = append(indexes, i)
			}
			return indexes
		}
		for i, binding := range bindings {
			if binding.Primary {
				return []int{i}
			}
		}
		if len(bindings) > 0 {
			return []int{0}
		}
	}
	return nil
}

func chooseHostPort(preferred int, explicit bool, reservedPorts, selectedPorts map[int]struct{}) (int, error) {
	if explicit {
		if preferred < minHostPort || preferred > maxHostPort {
			return 0, fmt.Errorf("explicit port %d is outside the valid range %d-%d", preferred, minHostPort, maxHostPort)
		}
		if hostPortUnavailable(preferred, reservedPorts, selectedPorts) {
			return 0, fmt.Errorf("explicit port %d is already in use", preferred)
		}
		return preferred, nil
	}

	start := preferred
	if start < minHostPort || start > maxHostPort {
		start = minHostPort
	}
	for port := start; port <= maxHostPort; port++ {
		if !hostPortUnavailable(port, reservedPorts, selectedPorts) {
			return port, nil
		}
	}
	for port := minHostPort; port < start; port++ {
		if !hostPortUnavailable(port, reservedPorts, selectedPorts) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free host port available starting from %d", start)
}

func hostPortUnavailable(port int, reservedPorts, selectedPorts map[int]struct{}) bool {
	if _, ok := reservedPorts[port]; ok {
		return true
	}
	if _, ok := selectedPorts[port]; ok {
		return true
	}
	return localPortAlive(port)
}

func localPortAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), portDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func runtimePrimaryPort(req *runtime.DeployRequest) int {
	if req == nil {
		return 0
	}
	if port := knowledge.PrimaryPort(requestPortBindings(req)); port > 0 {
		return port
	}
	if req.Port > 0 {
		return req.Port
	}
	return 8000
}
