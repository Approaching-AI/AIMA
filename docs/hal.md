# HAL Domain Documentation

> AI-Inference-Managed-by-AI

本文档描述 AIMA 的硬件抽象层（HAL），包括硬件检测和实时监控。

## 接口定义

### MCP 工具

| 工具 | JSON-RPC 方法 | 功能 |
|------|---------------|------|
| `hardware.detect` | `hal.detect` | 检测 GPU/CPU/RAM，返回能力向量 |
| `hardware.metrics` | `hal.metrics` | 实时资源利用率 + 功耗 + 温度 |

---

## 数据结构

### HardwareInfo (internal/hal/detect.go)

```go
type HardwareInfo struct {
    GPU *GPUInfo `json:"gpu"`
    CPU *CPUInfo `json:"cpu"`
    RAM *RAMInfo `json:"ram"`
}

type GPUInfo struct {
    Vendor    string  `json:"vendor"`     // nvidia | amd | intel | none
    Arch      string  `json:"arch"`       // Blackwell | Ada | RDNA3 | ...
    Model     string  `json:"model"`      // RTX 4090 | GB10 | ...
    VRAMMiB   int     `json:"vram_mib"`
    ComputeID string  `json:"compute_id"`     // "8.9" (NV) | "gfx1151" (AMD) | "metal3" (Apple)
    ComputeUnits int   `json:"compute_units"`  // CUDA cores / stream processors / GPU cores
    Driver    string  `json:"driver"`
    ResourceName string `json:"resource_name"` // "nvidia.com/gpu" | "amd.com/gpu"
}

type CPUInfo struct {
    Arch   string  `json:"arch"`   // x86_64 | arm64 | arm
    Cores  int     `json:"cores"`
    FreqGHz float64 `json:"freq_ghz"`
    Model  string  `json:"model"`
}

type RAMInfo struct {
    TotalMiB     int `json:"total_mib"`
    AvailableMiB int `json:"available_mib"`
}

type StorageInfo struct {
    DataDirPath string       `json:"data_dir_path"`
    FreeMiB     int64        `json:"free_mib"`
    TotalMiB    int64        `json:"total_mib"`
    Volumes     []VolumeInfo `json:"volumes,omitempty"`
}

type VolumeInfo struct {
    MountPoint string `json:"mount_point"`
    Device     string `json:"device,omitempty"`
    TotalMiB   int64  `json:"total_mib"`
    FreeMiB    int64  `json:"free_mib"`
}
```

### Metrics (internal/hal/metrics.go)

```go
type Metrics struct {
    GPU *GPUMetrics `json:"gpu,omitempty"`
    CPU *CPUMetrics `json:"cpu,omitempty"`
    RAM *RAMMetrics `json:"ram,omitempty"`
    Power *PowerMetrics `json:"power,omitempty"`
}

type GPUMetrics struct {
    UtilizationPercent float64 `json:"utilization_percent"`
    MemoryUsedMiB     int     `json:"memory_used_mib"`
    MemoryTotalMiB    int     `json:"memory_total_mib"`
    TemperatureC      float64 `json:"temperature_c"`
    PowerDrawWatts    float64 `json:"power_draw_watts"`
}

type CPUMetrics struct {
    UtilizationPercent float64 `json:"utilization_percent"`
}

type RAMMetrics struct {
    UsedMiB  int     `json:"used_mib"`
    TotalMiB int     `json:"total_mib"`
}

type PowerMetrics struct {
    CurrentWatts float64 `json:"current_watts"`
    PowerMode   string  `json:"power_mode"` // 15W | 30W | 60W | 100W
}
```

---

## 硬件检测

### 检测流程

```
Detect()
  │
  ├── 1. CPU 检测 (runtime.GOARCH + /proc/cpuinfo 或 sysctl)
  │
  ├── 2. RAM 检测 (/proc/meminfo 或 sysctl)
  │
  ├── 3. GPU 检测 (按优先级)
  │      ├── nvidia-smi (NVIDIA)
  │      ├── rocm-smi (AMD)
  │      ├── intel_gpu_top (Intel)
  │      └── 无 GPU
  │
  ├── 4. 存储检测
  │      ├── AIMA 数据目录磁盘空间 (free + total)
  │      └── 所有挂载卷列表 (listVolumes)
  │          ├── Linux: /proc/mounts 解析
  │          ├── macOS: /Volumes 目录扫描
  │          └── Windows: GetLogicalDrives API
  │
  └── 5. OS 检测 (版本、内核、容器运行时)
```

### GPU 资源名映射

| Vendor | 资源名 |
|--------|--------|
| NVIDIA | `nvidia.com/gpu` |
| AMD | `amd.com/gpu` |
| Intel | `gpu.intel.com/i915` |
| 无 GPU | (空字符串) |

资源名用于 Pod YAML 生成时的 GPU 资源声明，支持多厂商 GPU。

---

## 配置解析桥接

HAL 检测的数据通过 `buildHardwareInfo()` 映射到 `knowledge.HardwareInfo`，用于配置解析：

```
hal.Detect()                             knowledge.HardwareInfo
  GPU.Arch          ──→                    GPUArch
  GPU.VRAMMiB       ──→                    GPUVRAMMiB        (静态层: VRAM 过滤)
  GPU.Count         ──→                    GPUCount
  GPU.UnifiedMemory ──→                    UnifiedMemory     (静态层: 统一显存过滤)
  CPU.Arch          ──→                    CPUArch
  CPU.Cores         ──→                    CPUCores
  RAM.TotalMiB      ──→                    RAMTotalMiB
  RAM.AvailableMiB  ──→                    RAMAvailMiB

hal.CollectMetrics()
  GPU.MemoryUsedMiB ──→                    GPUMemUsedMiB     (动态层: CheckFit)
  GPU.MemoryTotalMiB - MemoryUsedMiB ──→   GPUMemFreeMiB     (动态层: CheckFit)
```

所有新字段零值 = "未知" = 跳过检查（graceful degradation）。
`hal.Detect()` 或 `hal.CollectMetrics()` 失败时不阻止部署，仅降级到 gpu_arch-only 匹配。

---

## 实时监控

### nvidia-smi 解析

```go
func queryGPUMetrics() (*GPUMetrics, error) {
    out, err := exec.Command("nvidia-smi",
        "--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw",
        "--format=csv,noheader,nounits").Output()
    // 解析 CSV 行: "65, 8192, 16384, 72, 72.5"
    // ...
}
```

### 功耗模式

支持的功耗模式从 Hardware Profile 读取：

```yaml
constraints:
  power_modes: [15W, 30W, 60W, 100W]
```

---

## 使用示例

### 检测硬件

```bash
./aima hal detect

# 输出示例
{
  "gpu": {
    "vendor": "nvidia",
    "arch": "Blackwell",
    "model": "RTX 4090",
    "vram_mib": 24576,
    "compute_id": "8.9",
    "compute_units": 16384,
    "driver": "550.90.07",
    "resource_name": "nvidia.com/gpu"
  },
  "cpu": {
    "arch": "x86_64",
    "cores": 24,
    "freq_ghz": 3.0,
    "model": "Intel(R) Core(TM) i9-13980HX"
  },
  "ram": {
    "total_mib": 131072,
    "bandwidth_gbps": 200
  }
}
```

### 查询实时指标

```bash
./aima hal metrics

# 输出示例
{
  "gpu": {
    "utilization_percent": 65.2,
    "memory_used_mib": 8192,
    "memory_total_mib": 24576,
    "temperature_c": 72.0,
    "power_draw_watts": 350.5
  },
  "cpu": {
    "utilization_percent": 45.0
  },
  "ram": {
    "used_mib": 32768,
    "total_mib": 131072
  },
  "power": {
    "current_watts": 450.0,
    "power_mode": "100W"
  }
}
```

---

## 相关文件

- `internal/hal/detect.go` - 硬件检测实现
- `internal/hal/hal.go` - 数据结构定义（HardwareInfo, StorageInfo, VolumeInfo 等）
- `internal/hal/disk_linux.go` - Linux 磁盘/卷检测
- `internal/hal/disk_darwin.go` - macOS 磁盘/卷检测
- `internal/hal/disk_windows.go` - Windows 磁盘/卷检测
- `internal/hal/metrics.go` - 实时监控实现

---

*最后更新：2026-02-28*
