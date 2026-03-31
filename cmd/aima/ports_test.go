package main

import (
	"context"
	"net"
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
