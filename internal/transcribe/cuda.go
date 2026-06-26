// file: internal/transcribe/cuda.go
// version: 1.0.0
// guid: e9f0a1b2-c3d4-5e6f-7a8b-9c0d1e2f3a4b
// last-edited: 2026-06-26

package transcribe

import (
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// cudaConfig holds the torch package spec and supporting pins for a given GPU.
type cudaConfig struct {
	// TorchPkg is the full package+version+variant string, e.g. "torch==2.0.1+cu118".
	TorchPkg string
	// ExtraIndexURL is the PyTorch wheel index for this variant.
	ExtraIndexURL string
	// PythonVersion is the uv --python flag value (e.g. "3.11", "3.12").
	PythonVersion string
	// ExtraDeps are additional --with packages required by this torch version.
	ExtraDeps []string
}

var (
	cudaOnce   sync.Once
	cachedCUDA cudaConfig
)

// detectCUDA queries nvidia-smi to find the GPU compute capability and selects
// the best available torch+CUDA variant. Results are cached after the first call.
//
// Selection table (compute capability drives the torch ceiling):
//   - No GPU / nvidia-smi absent → torch (CPU-only, no CUDA suffix)
//   - CC ≤ 6.1 (Pascal, e.g. GTX 10xx) → torch==2.0.1+cu118
//     PyTorch 2.1+ dropped sm_61; 2.0.x also needs setuptools<67 + numpy<2.
//   - CC 7.x (Volta/Turing, e.g. RTX 20xx) and CUDA driver ≥ 12.4 → torch==2.5.1+cu124
//   - CC 7.x and CUDA driver ≥ 12.1 → torch==2.3.1+cu121
//   - CC 7.x and CUDA driver ≥ 11.8 → torch==2.0.1+cu118
//   - CC ≥ 8.0 (Ampere+) follows the same CUDA driver ladder as CC 7.x
func detectCUDA() cudaConfig {
	cudaOnce.Do(func() {
		cachedCUDA = probeCUDA()
		slog.Info("transcribe: CUDA probe", "torch", cachedCUDA.TorchPkg, "python", cachedCUDA.PythonVersion)
	})
	return cachedCUDA
}

func probeCUDA() cudaConfig {
	cpu := cudaConfig{
		TorchPkg:      "torch",
		ExtraIndexURL: "",
		PythonVersion: "3.12",
	}

	// Check for nvidia-smi.
	nvidiaSMI, err := exec.LookPath("nvidia-smi")
	if err != nil {
		slog.Info("transcribe: nvidia-smi not found, using CPU torch")
		return cpu
	}

	// Get compute capability of the first GPU.
	ccOut, err := exec.Command(nvidiaSMI, "--query-gpu=compute_cap", "--format=csv,noheader").Output()
	if err != nil {
		slog.Warn("transcribe: nvidia-smi compute_cap query failed, using CPU torch", "err", err)
		return cpu
	}
	ccStr := strings.TrimSpace(strings.SplitN(string(ccOut), "\n", 2)[0])
	// ccStr is like "6.1" or "7.5" or "8.6"
	parts := strings.SplitN(ccStr, ".", 2)
	if len(parts) != 2 {
		slog.Warn("transcribe: unexpected compute_cap format, using CPU torch", "raw", ccStr)
		return cpu
	}
	ccMajor, err1 := strconv.Atoi(parts[0])
	ccMinor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		slog.Warn("transcribe: cannot parse compute_cap, using CPU torch", "raw", ccStr)
		return cpu
	}
	cc := ccMajor*10 + ccMinor // e.g. 6.1 → 61, 7.5 → 75

	// Get driver's max supported CUDA version.
	driverOut, err := exec.Command(nvidiaSMI, "--query-gpu=driver_version", "--format=csv,noheader").Output()
	cudaDriverMajor := 0
	if err == nil {
		// CUDA version isn't directly in driver_version; use the CUDA column from
		// `nvidia-smi` plain output header ("CUDA Version: X.Y").
		headerOut, herr := exec.Command(nvidiaSMI).Output()
		if herr == nil {
			for _, line := range strings.Split(string(headerOut), "\n") {
				if idx := strings.Index(line, "CUDA Version:"); idx >= 0 {
					raw := strings.TrimSpace(line[idx+len("CUDA Version:"):])
					// raw is like "12.4" or "11.8 "
					raw = strings.Fields(raw)[0]
					majStr := strings.SplitN(raw, ".", 2)[0]
					if v, e := strconv.Atoi(majStr); e == nil {
						cudaDriverMajor = v
					}
					break
				}
			}
		}
	}
	_ = driverOut

	slog.Info("transcribe: GPU detected", "compute_cap", ccStr, "cuda_driver_major", cudaDriverMajor)

	// CC 6.1 and below: Pascal (GTX 10xx). PyTorch 2.1+ dropped sm_61.
	// Must use 2.0.1+cu118 regardless of CUDA driver version.
	// torch 2.0.x needs setuptools<67 (removed pkg_resources vendoring) + numpy<2 (ABI break).
	if cc <= 61 {
		return cudaConfig{
			TorchPkg:      "torch==2.0.1+cu118",
			ExtraIndexURL: "https://download.pytorch.org/whl/cu118",
			PythonVersion: "3.11", // torch 2.0.1 has no 3.12 wheels
			ExtraDeps:     []string{"setuptools<67", "numpy<2"},
		}
	}

	// CC 7.x and above (Turing RTX 20xx / Volta / Ampere+):
	// select best torch by CUDA driver version.
	switch {
	case cudaDriverMajor >= 12:
		return cudaConfig{
			TorchPkg:      "torch==2.5.1+cu124",
			ExtraIndexURL: "https://download.pytorch.org/whl/cu124",
			PythonVersion: "3.12",
		}
	case cudaDriverMajor >= 11:
		return cudaConfig{
			TorchPkg:      "torch==2.0.1+cu118",
			ExtraIndexURL: "https://download.pytorch.org/whl/cu118",
			PythonVersion: "3.11",
			ExtraDeps:     []string{"numpy<2"},
		}
	default:
		// Unknown CUDA version — safe fallback: CPU torch.
		slog.Warn("transcribe: unknown CUDA driver version, falling back to CPU torch", "cuda_major", cudaDriverMajor)
		return cpu
	}
}
