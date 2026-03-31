package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
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

func allocateDeploymentPorts(
	ctx context.Context,
	owner string,
	runtimeName string,
	req *runtime.DeployRequest,
	provenance map[string]string,
	deployments []*runtime.DeploymentStatus,
) error {
	if req == nil {
		return nil
	}

	bindings := requestPortBindings(req)
	if len(bindings) == 0 {
		applyPortLabels(runtimeName, req, nil)
		return nil
	}

	hostIndexes := hostPortBindingIndexes(runtimeName, req, bindings)
	if len(hostIndexes) == 0 {
		applyPortLabels(runtimeName, req, bindings)
		return nil
	}

	reservedPorts := reservedHostPorts(deployments, owner)
	selectedPorts := make(map[int]struct{}, len(hostIndexes))
	if req.Config == nil {
		req.Config = make(map[string]any)
	}

	for _, idx := range hostIndexes {
		binding := bindings[idx]
		explicit := strings.EqualFold(provenance[binding.ConfigKey], "L1")
		port, err := chooseHostPort(binding.Port, explicit, reservedPorts, selectedPorts)
		if err != nil {
			return err
		}
		bindings[idx].Port = port
		if binding.ConfigKey != "" {
			req.Config[binding.ConfigKey] = port
		}
		selectedPorts[port] = struct{}{}
	}

	applyPortLabels(runtimeName, req, bindings)
	return nil
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
