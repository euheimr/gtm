package gtm

import (
	"encoding/json"
	"fmt"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"golang.org/x/sys/windows"
	"log/slog"
	"math"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// GIBIBYTE is the binary representation of gigabyte
const GIBIBYTE = 1_073_741_824 // binary base 2^30 or 1024^3
const GIGABYTE = 1_000_000_000 // decimal base 10^9

type FileSystemType int

const (
	APFS FileSystemType = iota
	exFAT
	FAT
	FAT32
	EXT
	EXT2
	EXT3
	EXT4
	NTFS
	JFS
	ZFS
)

type DiskInfo struct {
	FSType        FileSystemType `json:"fstype"`
	IsVirtualDisk bool           `json:"is_virtual_disk"`
	Free          uint64         `json:"free"`
	Used          uint64         `json:"used"`
	UsedPercent   uint64         `json:"used_percent"`
	Total         uint64         `json:"total"`
}

type GPUData struct {
	Id          int32   `json:"card-id"`
	Load        float64 `json:"load"`
	MemoryUsage float64 `json:"memoryUsage"`
	MemoryTotal float64 `json:"memoryTotal"`
	Power       float64 `json:"power"`
	Temperature int32   `json:"temperature"`
}

var (
	cpuInfo []cpu.InfoStat
	//diskInfo []DiskInfo
	diskInfo  []disk.PartitionStat
	diskUsage disk.UsageStat
	gpuInfo   []GPUData
	hostInfo  *host.InfoStat
	memInfo   *mem.VirtualMemoryStat
	netInfo   []net.IOCountersStat
)

var (
	lastFetchCPU  time.Time
	lastFetchDisk time.Time
	lastFetchGPU  time.Time
	lastFetchHost time.Time
	lastFetchMem  time.Time
	lastFetchNet  time.Time
	lastFetchProc time.Time
)

var (
	cpuModelName string
	hostname     string
	gpuName      string
)

var GPUVendor string

func init() {}

func ConvertBytesToGiB(bytes uint64, rounded bool) (result float64) {
	result = float64(bytes) / GIBIBYTE
	if rounded {
		// effectively return an integer via rounding the float to an int (ie. "11.0" GB)
		return math.RoundToEven(result)
	} else {
		return result
	}
}

func GetCPUInfo() []cpu.InfoStat {
	cInfo, err := cpu.Info()
	if err != nil {
		slog.Error("Failed to retrieve cpu.Info()! " + err.Error())
	}
	cpuInfo = cInfo
	slog.Debug("cpu.Info(): "+cpuInfo[0].String(), "socketCount", len(cpuInfo))

	// model name doesn't change with each syscall... so cache it here
	cpuModelName = cpuInfo[0].ModelName

	return cpuInfo
}

func GetCPUModel(formatName bool) string {
	if cpuModelName == "" {
		GetCPUInfo()
	}
	cpuModel := cpuModelName
	if formatName && cpuInfo[0].VendorID == "GenuineIntel" {
		cpuModel = strings.ReplaceAll(cpuModel, "(R)", "")
		cpuModel = strings.ReplaceAll(cpuModel, "(TM)", "")
		cpuModel = strings.ReplaceAll(cpuModel, "CPU @ ", "@")
		cpuModel = strings.ReplaceAll(cpuModel, "Core ", "")
	}
	// TODO: format AMD & ARM ?
	return cpuModel
}

func GetDiskInfo() []disk.PartitionStat {
	dInfo, err := disk.Partitions(false)
	if err != nil {
		slog.Error("Failed to retrieve disk.Partitions()! " + err.Error())
	}
	// FIXME: diskInfo as new var might be redundant?

	diskInfo = dInfo
	slog.Debug("disk.Partitions(): physical disk count = " + strconv.Itoa(len(diskInfo)))
	for i, dsk := range diskInfo {
		slog.Debug("disk.Partitions(): disk #" + strconv.Itoa(i) + ": " + dsk.String())
	}
	return diskInfo
}

func GetDiskUsage(path string) (disk.UsageStat, error) {
	if len(diskUsage.String()) > 0 && time.Since(lastFetchDisk) < time.Hour {
		return diskUsage, nil
	}

	dUsage, err := disk.Usage(path)
	if err != nil {
		slog.Error("Failed to retrieve disk.Usage(" + path + ")! " + err.Error())
		return disk.UsageStat{}, err
	}
	slog.Debug("disk.Usage(" + path + "): " + dUsage.String())
	diskUsage = *dUsage
	lastFetchDisk = time.Now()
	return diskUsage, err
}

func IsVirtualDisk(path string) bool {
	switch runtime.GOOS {
	case "windows":
		d, err := windows.UTF16PtrFromString(path)
		if err != nil {
			slog.Error("Failed to get UTF16 pointer from string: " + path + "! " + err.Error())
		}
		driveType := windows.GetDriveType(d)
		//slog.Debug("drive " + path + ", type=" + strconv.FormatUint(uint64(driveType), 10))

		// 2: DRIVE_REMOVABLE 3: DRIVE_FIXED 4: DRIVE_REMOTE 5: DRIVE_CDROM 6: DRIVE_RAMDISK
		switch driveType {
		case windows.DRIVE_RAMDISK:
			slog.Debug(path + " is a RAMDISK")
			return true
		case windows.DRIVE_FIXED:
			// disk.IOCounters(C:) ALWAYS errors out on Windows, BUT we do not get an
			//	empty struct on a valid DRIVE_FIXED device
			io, _ := disk.IOCounters(path)
			switch len(io) {
			case 0:
				// This is a VERY hacky way of working around detecting Google Drive.
				//	GDrive is seen as a "real" drive in Windows for some reason, and not
				//	as a RAMDISK (Virtual Hard Disk; aka. VHD).
				// But if we try to call disk.IOCounters() on it, we will just get an
				//	empty struct (length of 0) back, which indicates it IS a RAMDISK. This
				//  is the only way I've been able to detect a mounted Google Drive :(
				slog.Debug("drive " + path + " IS a RAMDISK")
				return true
			default:
				// Any other case that is len(io) > 0 means it is not a RAMDISK
				slog.Debug("disk.IOCounters(" + path + "): " + io[path].String())
				return false
			}
		default:
			slog.Debug(path + " is not a RAMDISK")
			return false
		}
	default:
		// TODO: do RAMDISK checks for macOS & Linux !
		slog.Debug("Not on windows... ignoring RAMDISK check ...")
		return false
	}
}

func HasGPU() bool {
	if err := exec.Command("nvidia-smi").Run(); err == nil {
		GPUVendor = "nvidia"
		return true
	}
	if err := exec.Command("rocm-smi").Run(); err == nil {
		GPUVendor = "amd"
		return true
	}
	slog.Error("HasGPU(): Could not find NVIDIA or AMD GPUs installed using SMI")
	return false
}

func (g *GPUData) String() string {
	// NVIDIA always reports memory usage in MiB
	memoryUsageGiB := fmt.Sprintf("%.0f", g.MemoryUsage) ///1024)
	memoryTotalGiB := fmt.Sprintf("%.0f", g.MemoryTotal) ///1024)

	//memoryUsageGiB = fmt.Sprintf("%.2f", (g.MemoryUsage/g.MemoryTotal))

	return fmt.Sprintf("gfx card #%v, %v%%, %v MiB, %v MiB, %vW, %v°C",
		g.Id, int(g.Load*100), memoryUsageGiB, memoryTotalGiB, g.Power, g.Temperature)
}

func (g *GPUData) JSON(indent bool) string {
	if indent {
		out, err := json.MarshalIndent(g, "", "  ")
		if err != nil {
			slog.Error("Failed to marshal indent JSON from struct GPUData{} ! " + err.Error())
		}
		return string(out)
	} else {
		out, err := json.Marshal(g)
		if err != nil {
			slog.Error("Failed to marshal JSON from struct GPUData{} ! " + err.Error())
		}
		return string(out)
	}
}

func parseGPUNvidiaData(output []byte) []GPUData {
	var gpuData []GPUData
	var (
		id          int64
		load        int64
		memoryUsage float64
		memoryTotal float64
		power       float64
		temp        int64
		err         error
	)

	info := strings.Split(string(output), "\n")
	for _, line := range info {
		if line != "" {
			data := strings.Split(line, ", ")
			gpuName = data[1]

			if id, err = strconv.ParseInt(data[0], 10, 32); err != nil {
				slog.Error("Failed to parse GPU Id from string -> int ! " + err.Error())
			}
			if load, err = strconv.ParseInt(data[2], 10, 32); err != nil {
				slog.Error("Failed to parse GPU Load from string -> int ! " + err.Error())
			}
			if memoryUsage, err = strconv.ParseFloat(data[3], 64); err != nil {
				slog.Error("Failed to parse float: memory.usage !" + err.Error())
				memoryUsage = 0.0
			}
			if memoryTotal, err = strconv.ParseFloat(data[4], 64); err != nil {
				slog.Error("Failed to parse float: memory.total !" + err.Error())
				memoryTotal = 0.0
			}
			if power, err = strconv.ParseFloat(data[5], 64); err != nil {
				slog.Error("Failed to parse float: power !" + err.Error())
			}

			// on windows, there's a carriage return on the last stat
			t := strings.ReplaceAll(data[6], "\r", "")
			if temp, err = strconv.ParseInt(t, 10, 32); err != nil {
				slog.Error("Failed to parse float: temp !" + err.Error())
			}

			gpu := GPUData{
				Id:          int32(id),
				Load:        float64(load) / 100,
				MemoryUsage: memoryUsage,
				MemoryTotal: memoryTotal,
				Power:       power,
				Temperature: int32(temp),
			}
			gpuData = append(gpuData, gpu)
		}
	}
	return gpuData
}

func GetGPUInfo() []GPUData {
	if !HasGPU() {
		return nil
	}

	// Limit getting device data to just once a second, and NOT with every UI update
	if time.Since(lastFetchGPU) <= time.Second && len(gpuInfo) > 0 {
		return gpuInfo
	}

	switch GPUVendor {
	case "nvidia":
		cmd := exec.Command(
			"nvidia-smi",
			"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,"+
				"power.draw,temperature.gpu",
			"--format=csv,noheader,nounits")
		data, err := cmd.Output()
		if err != nil {
			slog.Error("Failed to retrieve NVIDIA GPU data from nvidia-smi ! " + err.Error())
			return nil
		}
		//slog.Debug(data[len(data)-1].String())
		gpuInfo = parseGPUNvidiaData(data)
		lastFetchGPU = time.Now()
		return gpuInfo
	case "amd":
		// TODO: write rocm-smi code for AMD gpu detection and data parsing
		slog.Error("AMD GPU not implemented yet !")
	}
	return nil
}

func GetGPUName() string { return gpuName }

func GetHostInfo() *host.InfoStat {
	hInfo, err := host.Info()
	if err != nil {
		slog.Error("Failed to retrieve host.Info()! " + err.Error())
	}
	hostInfo = hInfo
	slog.Debug("host.Info(): " + hostInfo.String())

	hostname = hostInfo.Hostname
	return hostInfo
}

func GetHostname() string {
	if hostname != "" {
		return hostname
	} else {
		GetHostInfo()
		return hostname
	}
}

func GetMemoryInfo() *mem.VirtualMemoryStat {
	if time.Since(lastFetchMem) < time.Second && len(memInfo.String()) > 0 {
		return memInfo
	}

	mInfo, err := mem.VirtualMemory()
	if err != nil {
		slog.Error("Failed to retrieve mem.VirtualMemory()! " + err.Error())
	}
	lastFetchMem = time.Now()

	if memInfo == nil {
		// If this is the first time getting the memory usage, just populate/init memInfo
		memInfo = mInfo
		return memInfo
	}

	oldUsedPercent := memInfo.UsedPercent
	currentUsedPercent := mInfo.UsedPercent

	if oldUsedPercent == currentUsedPercent {
		// If we get the same results, just re-send the same data without updates
		//slog.Debug("gtm.GetMemoryInfo(): no changes... return last fetch")
		return memInfo
	} else {
		//  If the previous fetch is greater than or less than the last fetch in
		// 	Gigabytes, return the updated memory usage
		memInfo = mInfo
		slog.Debug("mem.VirtualMemory(): " + memInfo.String())
		return memInfo
	}
}

func GetNetworkInfo() []net.IOCountersStat {
	nInfo, err := net.IOCounters(false)
	if err != nil {
		slog.Error("Failed to retrieve net.IOCounters()! " + err.Error())
	}
	netInfo = nInfo
	for i, iface := range netInfo {
		slog.Debug("net.IOCounters(), interface #" + strconv.Itoa(i) + ": " +
			iface.String())
	}
	return netInfo
}
