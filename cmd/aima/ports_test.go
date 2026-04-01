package main

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/runtime"
)

func TestAllocateDeploymentPortsAutoRebindsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	busyPort := ln.Addr().(*net.TCPAddr).Port
	req := &runtime.DeployRequest{
		Config: map[string]any{
			"port": busyPort,
		},
		PortSpecs: []knowledge.StartupPort{{Name: "http", Primary: true}},
	}

	if err := allocateDeploymentPorts(context.Background(), "deploy-a", "native", req, map[string]string{"port": "L0"}, nil); err != nil {
		t.Fatalf("allocateDeploymentPorts: %v", err)
	}
	if got := req.Config["port"]; got == busyPort {
		t.Fatalf("config.port = %v, want allocator to move off busy port", got)
	}
	if req.Labels["aima.dev/port"] == "" {
		t.Fatal("expected primary port label to be populated")
	}
}

func TestAllocateDeploymentPortsHonorsExplicitPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	busyPort := ln.Addr().(*net.TCPAddr).Port
	req := &runtime.DeployRequest{
		Config: map[string]any{
			"port": busyPort,
		},
		PortSpecs: []knowledge.StartupPort{{Name: "http", Primary: true}},
	}

	err = allocateDeploymentPorts(context.Background(), "deploy-a", "native", req, map[string]string{"port": "L1"}, nil)
	if err == nil {
		t.Fatal("expected explicit busy port to fail")
	}
}

func TestAllocateDeploymentPortsDockerBridgeReservesOnlyPrimary(t *testing.T) {
	req := &runtime.DeployRequest{
		Config: map[string]any{
			"grpc_port_v1beta1": 32108,
			"grpc_port":         32109,
			"port":              32110,
		},
		PortSpecs: []knowledge.StartupPort{
			{Name: "grpc-v1beta1", Flag: "--grpc_port_v1beta1", ConfigKey: "grpc_port_v1beta1"},
			{Name: "grpc", Flag: "--grpc_port", ConfigKey: "grpc_port"},
			{Name: "http", Flag: "--http_port", ConfigKey: "port", Primary: true},
		},
	}

	if err := allocateDeploymentPorts(context.Background(), "deploy-a", "docker", req, map[string]string{}, nil); err != nil {
		t.Fatalf("allocateDeploymentPorts: %v", err)
	}
	if _, ok := req.Labels["aima.dev/host-port"]; !ok {
		t.Fatal("expected primary host-port label")
	}
	if _, ok := req.Labels["aima.dev/host-port.grpc_port"]; ok {
		t.Fatal("bridge-mode extra ports should not be labeled as host ports")
	}
	if got := req.Config["grpc_port_v1beta1"]; got != 32108 {
		t.Fatalf("grpc_port_v1beta1 = %v, want unchanged 32108", got)
	}
}

func TestReservedHostPortsUsesDeploymentLabels(t *testing.T) {
	deployments := []*runtime.DeploymentStatus{{
		Name:    "running-native",
		Phase:   "running",
		Runtime: "native",
		Labels: map[string]string{
			"aima.dev/host-port":           "32110",
			"aima.dev/host-port.grpc_port": "32109",
		},
	}}

	reserved := reservedHostPorts(deployments, "other")
	if _, ok := reserved[32110]; !ok {
		t.Fatal("expected primary host port to be reserved")
	}
	if _, ok := reserved[32109]; !ok {
		t.Fatal("expected extra host port to be reserved")
	}
}

func TestReservedHostPortsFallsBackToLegacyPrimaryPortLabel(t *testing.T) {
	deployments := []*runtime.DeploymentStatus{{
		Name:    "legacy-native",
		Phase:   "running",
		Runtime: "native",
		Labels: map[string]string{
			"aima.dev/port": "32200",
		},
	}}

	reserved := reservedHostPorts(deployments, "other")
	if _, ok := reserved[32200]; !ok {
		t.Fatal("expected legacy aima.dev/port label to reserve the host port")
	}
}

func TestAllocateDeploymentPortsReusesExistingOwnerHostPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	busyPort := ln.Addr().(*net.TCPAddr).Port
	req := &runtime.DeployRequest{
		Config: map[string]any{
			"port": busyPort,
		},
		PortSpecs: []knowledge.StartupPort{{Name: "http", Primary: true}},
	}
	deployments := []*runtime.DeploymentStatus{{
		Name:    "deploy-a",
		Phase:   "running",
		Runtime: "docker",
		Labels: map[string]string{
			"aima.dev/host-port": strconv.Itoa(busyPort),
			"aima.dev/port":      strconv.Itoa(busyPort),
		},
	}}

	if err := allocateDeploymentPorts(context.Background(), "deploy-a", "docker", req, map[string]string{"port": "L0"}, deployments); err != nil {
		t.Fatalf("allocateDeploymentPorts: %v", err)
	}
	if got := req.Config["port"]; got != busyPort {
		t.Fatalf("config.port = %v, want reused owner port %d", got, busyPort)
	}
	if req.Labels["aima.dev/host-port"] != strconv.Itoa(busyPort) {
		t.Fatalf("unexpected host-port label: %+v", req.Labels)
	}
}

func TestAllocateDeploymentPortsNativeReservesAllHostPorts(t *testing.T) {
	req := &runtime.DeployRequest{
		Config: map[string]any{
			"grpc_port_v1beta1": 32108,
			"grpc_port":         32109,
			"port":              32110,
		},
		PortSpecs: []knowledge.StartupPort{
			{Name: "grpc-v1beta1", Flag: "--grpc_port_v1beta1", ConfigKey: "grpc_port_v1beta1"},
			{Name: "grpc", Flag: "--grpc_port", ConfigKey: "grpc_port"},
			{Name: "http", Flag: "--http_port", ConfigKey: "port", Primary: true},
		},
	}
	deployments := []*runtime.DeploymentStatus{{
		Name:    "other-native",
		Phase:   "running",
		Runtime: "native",
		Labels: map[string]string{
			"aima.dev/host-port.grpc_port_v1beta1": "32108",
			"aima.dev/host-port.grpc_port":         "32109",
			"aima.dev/host-port":                   "32110",
		},
	}}

	if err := allocateDeploymentPorts(context.Background(), "deploy-a", "native", req, map[string]string{}, deployments); err != nil {
		t.Fatalf("allocateDeploymentPorts: %v", err)
	}

	got := []int{
		req.Config["grpc_port_v1beta1"].(int),
		req.Config["grpc_port"].(int),
		req.Config["port"].(int),
	}
	for _, port := range got {
		if port == 32108 || port == 32109 || port == 32110 {
			t.Fatalf("native host-bound ports should be moved away from reserved ports, got %v", got)
		}
	}
	if len(map[int]struct{}{got[0]: {}, got[1]: {}, got[2]: {}}) != 3 {
		t.Fatalf("native host-bound ports should remain unique, got %v", got)
	}
	if req.Labels["aima.dev/host-port.grpc_port_v1beta1"] != strconv.Itoa(got[0]) {
		t.Fatalf("unexpected grpc v1beta1 host label: %+v", req.Labels)
	}
	if req.Labels["aima.dev/host-port.grpc_port"] != strconv.Itoa(got[1]) {
		t.Fatalf("unexpected grpc host label: %+v", req.Labels)
	}
	if req.Labels["aima.dev/host-port"] != strconv.Itoa(got[2]) {
		t.Fatalf("unexpected http host label: %+v", req.Labels)
	}
	if req.Labels["aima.dev/port"] != strconv.Itoa(got[2]) {
		t.Fatalf("unexpected primary port label: %+v", req.Labels)
	}
}

func TestAllocateDeploymentPortsDockerHostNetworkReservesAllHostPorts(t *testing.T) {
	req := &runtime.DeployRequest{
		Config: map[string]any{
			"grpc_port_v1beta1": 32308,
			"grpc_port":         32309,
			"port":              32310,
		},
		PortSpecs: []knowledge.StartupPort{
			{Name: "grpc-v1beta1", Flag: "--grpc_port_v1beta1", ConfigKey: "grpc_port_v1beta1"},
			{Name: "grpc", Flag: "--grpc_port", ConfigKey: "grpc_port"},
			{Name: "http", Flag: "--http_port", ConfigKey: "port", Primary: true},
		},
		Container: &knowledge.ContainerAccess{NetworkMode: "host"},
	}
	deployments := []*runtime.DeploymentStatus{{
		Name:    "other-docker",
		Phase:   "running",
		Runtime: "docker",
		Labels: map[string]string{
			"aima.dev/host-port.grpc_port_v1beta1": "32308",
			"aima.dev/host-port.grpc_port":         "32309",
			"aima.dev/host-port":                   "32310",
		},
	}}

	if err := allocateDeploymentPorts(context.Background(), "deploy-a", "docker", req, map[string]string{}, deployments); err != nil {
		t.Fatalf("allocateDeploymentPorts: %v", err)
	}

	if req.Config["grpc_port_v1beta1"].(int) == 32308 || req.Config["grpc_port"].(int) == 32309 || req.Config["port"].(int) == 32310 {
		t.Fatalf("docker host network should move all host-bound ports, got %+v", req.Config)
	}
	if _, ok := req.Labels["aima.dev/host-port.grpc_port_v1beta1"]; !ok {
		t.Fatalf("expected docker host-network labels for extra ports, got %+v", req.Labels)
	}
}
