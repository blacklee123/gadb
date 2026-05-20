package adb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type DeviceFileInfo struct {
	Name         string
	Mode         os.FileMode
	Size         uint32
	LastModified time.Time
}

func (info DeviceFileInfo) IsDir() bool {
	return (info.Mode & (1 << 14)) == (1 << 14)
}

const DefaultFileMode = os.FileMode(0664)

type DeviceState string

const (
	StateUnknown      DeviceState = "UNKNOWN"
	StateOnline       DeviceState = "online"
	StateOffline      DeviceState = "offline"
	StateDisconnected DeviceState = "disconnected"
)

var deviceStateStrings = map[string]DeviceState{
	"":        StateDisconnected,
	"offline": StateOffline,
	"device":  StateOnline,
}

func deviceStateConv(k string) (deviceState DeviceState) {
	var ok bool
	if deviceState, ok = deviceStateStrings[k]; !ok {
		return StateUnknown
	}
	return
}

type DeviceForward struct {
	Serial string
	Local  string
	Remote string
	// LocalProtocol string
	// RemoteProtocol string
}

type Device struct {
	adbClient Client
	serial    string
	attrs     map[string]string
}

func (d Device) HasAttribute(key string) bool {
	_, ok := d.attrs[key]
	return ok
}

func (d Device) Product() (string, error) {
	if d.HasAttribute("product") {
		return d.attrs["product"], nil
	}
	return "", errors.New("does not have attribute: product")
}

func (d Device) Model() (string, error) {
	if d.HasAttribute("model") {
		return d.attrs["model"], nil
	}
	return "", errors.New("does not have attribute: model")
}

func (d Device) Usb() (string, error) {
	if d.HasAttribute("usb") {
		return d.attrs["usb"], nil
	}
	return "", errors.New("does not have attribute: usb")
}

func (d Device) transportId() (string, error) {
	if d.HasAttribute("transport_id") {
		return d.attrs["transport_id"], nil
	}
	return "", errors.New("does not have attribute: transport_id")
}

func (d Device) DeviceInfo() map[string]string {
	return d.attrs
}

func (d Device) Serial() string {
	// 	resp, err := d.adbClient.executeCommand(fmt.Sprintf("host-serial:%s:get-serialno", d.serial))
	return d.serial
}

func (d Device) IsUsb() (bool, error) {
	usb, err := d.Usb()
	if err != nil {
		return false, err
	}

	return usb != "", nil
}

func (d Device) State() (DeviceState, error) {
	resp, err := d.adbClient.executeCommand(fmt.Sprintf("host-serial:%s:get-state", d.serial))
	return deviceStateConv(resp), err
}

func (d Device) DevicePath() (string, error) {
	resp, err := d.adbClient.executeCommand(fmt.Sprintf("host-serial:%s:get-devpath", d.serial))
	return resp, err
}

func (d Device) Forward(localPort, remotePort int, noRebind ...bool) (err error) {
	command := ""
	local := fmt.Sprintf("tcp:%d", localPort)
	remote := fmt.Sprintf("tcp:%d", remotePort)

	if len(noRebind) != 0 && noRebind[0] {
		command = fmt.Sprintf("host-serial:%s:forward:norebind:%s;%s", d.serial, local, remote)
	} else {
		command = fmt.Sprintf("host-serial:%s:forward:%s;%s", d.serial, local, remote)
	}

	_, err = d.adbClient.executeCommand(command, true)
	return
}

func (d Device) ForwardList() (deviceForwardList []DeviceForward, err error) {
	var forwardList []DeviceForward
	if forwardList, err = d.adbClient.ForwardList(); err != nil {
		return nil, err
	}

	deviceForwardList = make([]DeviceForward, 0, len(deviceForwardList))
	for i := range forwardList {
		if forwardList[i].Serial == d.serial {
			deviceForwardList = append(deviceForwardList, forwardList[i])
		}
	}
	// resp, err := d.adbClient.executeCommand(fmt.Sprintf("host-serial:%s:list-forward", d.serial))
	return
}

func (d Device) ForwardKill(localPort int) (err error) {
	local := fmt.Sprintf("tcp:%d", localPort)
	_, err = d.adbClient.executeCommand(fmt.Sprintf("host-serial:%s:killforward:%s", d.serial, local), true)
	return
}

func (d Device) RunShellCommand(cmd string, args ...string) (string, error) {
	raw, err := d.RunShellCommandWithBytes(cmd, args...)
	return string(raw), err
}

func (d Device) RunShellCommandWithBytes(cmd string, args ...string) ([]byte, error) {
	if len(args) > 0 {
		cmd = fmt.Sprintf("%s %s", cmd, strings.Join(args, " "))
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, errors.New("adb shell: command cannot be empty")
	}
	raw, err := d.executeCommand(fmt.Sprintf("shell:%s", cmd))
	return raw, err
}

func (d Device) EnableAdbOverTCP(port ...int) (err error) {
	if len(port) == 0 {
		port = []int{AdbDaemonPort}
	}

	_, err = d.executeCommand(fmt.Sprintf("tcpip:%d", port[0]), true)
	return
}

func (d Device) createDeviceTransport() (tp transport, err error) {
	if tp, err = newTransport(fmt.Sprintf("%s:%d", d.adbClient.host, d.adbClient.port)); err != nil {
		return transport{}, err
	}

	if err = tp.Send(fmt.Sprintf("host:transport:%s", d.serial)); err != nil {
		return transport{}, err
	}
	err = tp.VerifyResponse()
	return
}

func (d Device) executeCommand(command string, onlyVerifyResponse ...bool) (raw []byte, err error) {
	if len(onlyVerifyResponse) == 0 {
		onlyVerifyResponse = []bool{false}
	}

	var tp transport
	if tp, err = d.createDeviceTransport(); err != nil {
		return nil, err
	}
	defer func() { _ = tp.Close() }()

	if err = tp.Send(command); err != nil {
		return nil, err
	}

	if err = tp.VerifyResponse(); err != nil {
		return nil, err
	}

	if onlyVerifyResponse[0] {
		return
	}

	raw, err = tp.ReadBytesAll()
	return
}

func (d Device) List(remotePath string) (devFileInfos []DeviceFileInfo, err error) {
	var tp transport
	if tp, err = d.createDeviceTransport(); err != nil {
		return nil, err
	}
	defer func() { _ = tp.Close() }()

	var sync syncTransport
	if sync, err = tp.CreateSyncTransport(); err != nil {
		return nil, err
	}
	defer func() { _ = sync.Close() }()

	if err = sync.Send("LIST", remotePath); err != nil {
		return nil, err
	}

	devFileInfos = make([]DeviceFileInfo, 0)

	var entry DeviceFileInfo
	for entry, err = sync.ReadDirectoryEntry(); err == nil; entry, err = sync.ReadDirectoryEntry() {
		if entry == (DeviceFileInfo{}) {
			break
		}
		devFileInfos = append(devFileInfos, entry)
	}

	return
}

func (d Device) PushFile(local *os.File, remotePath string, modification ...time.Time) (err error) {
	if len(modification) == 0 {
		var stat os.FileInfo
		if stat, err = local.Stat(); err != nil {
			return err
		}
		modification = []time.Time{stat.ModTime()}
	}

	return d.Push(local, remotePath, modification[0], DefaultFileMode)
}

func (d Device) Push(source io.Reader, remotePath string, modification time.Time, mode ...os.FileMode) (err error) {
	if len(mode) == 0 {
		mode = []os.FileMode{DefaultFileMode}
	}

	var tp transport
	if tp, err = d.createDeviceTransport(); err != nil {
		return err
	}
	defer func() { _ = tp.Close() }()

	var sync syncTransport
	if sync, err = tp.CreateSyncTransport(); err != nil {
		return err
	}
	defer func() { _ = sync.Close() }()

	data := fmt.Sprintf("%s,%d", remotePath, mode[0])
	if err = sync.Send("SEND", data); err != nil {
		return err
	}

	if err = sync.SendStream(source); err != nil {
		return
	}

	if err = sync.SendStatus("DONE", uint32(modification.Unix())); err != nil {
		return
	}

	if err = sync.VerifyStatus(); err != nil {
		return
	}
	return
}

func (d Device) Pull(remotePath string, dest io.Writer) (err error) {
	var tp transport
	if tp, err = d.createDeviceTransport(); err != nil {
		return err
	}
	defer func() { _ = tp.Close() }()

	var sync syncTransport
	if sync, err = tp.CreateSyncTransport(); err != nil {
		return err
	}
	defer func() { _ = sync.Close() }()

	if err = sync.Send("RECV", remotePath); err != nil {
		return err
	}

	err = sync.WriteStream(dest)
	return
}

func (d Device) Logcat(dst io.Writer, exitChan chan bool) error {
	var tp transport
	var err error
	if tp, err = d.createDeviceTransport(); err != nil {
		return err
	}
	defer func() { _ = tp.Close() }()

	if err = tp.Send("shell:logcat"); err != nil {
		return err
	}
	if err = tp.VerifyResponse(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		r := NewReader(ctx, tp.sock)
		io.Copy(dst, r)
	}()
	<-exitChan
	cancel()
	return err
}

func (d Device) Logcat2File(file string, exitChan chan bool) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_SYNC|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	defer f.Close()
	return d.Logcat(f, exitChan)
}

func (d Device) LogcatClear() error {
	_, err := d.executeCommand("shell:logcat -c")
	return err
}

func (d Device) GetProp(name string) string {
	// 执行 getprop 命令获取属性值
	output, err := d.RunShellCommand("getprop", name)
	if err != nil {
		// 错误时直接返回空字符串
		return ""
	}

	// 去除输出中的空格和换行符
	value := strings.TrimSpace(output)
	return value
}

func (d Device) GetScreenSize() string {
	width, height, err := d.WindowSize()
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("%dx%d", width, height)
}

func (d Device) WindowSize() (width, height int, err error) {
	output, err := d.RunShellCommand("wm", "size")
	if err != nil {
		return 0, 0, err
	}

	// 处理输出结果
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Override size") {
			// 提取覆盖尺寸部分
			if idx := strings.Index(line, ":"); idx != -1 {
				line = strings.TrimSpace(line[idx+1:])
			}
			line = strings.ReplaceAll(line, "Override size", "")
		} else if strings.Contains(line, "Physical size") {
			// 提取物理尺寸部分
			if idx := strings.Index(line, ":"); idx != -1 {
				line = strings.TrimSpace(line[idx+1:])
			}
		}

		// 清理字符串并解析尺寸
		line = strings.ReplaceAll(line, " ", "")
		parts := strings.Split(line, "x")
		if len(parts) != 2 {
			continue
		}

		width, err = strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		height, err = strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		return width, height, nil
	}

	return 0, 0, errors.New("failed to parse window size")
}

// getRealDisplayID 获取实际的显示ID
func (d Device) getRealDisplayID(displayID int) (string, error) {
	output, err := d.RunShellCommand("dumpsys", "SurfaceFlinger", "--display-id")
	if err != nil {
		return "", err
	}

	// 使用正则表达式提取所有显示ID
	re := regexp.MustCompile(`Display (\d+)`)
	matches := re.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return "", errors.New("no display found")
	}

	// 检查请求的displayID是否有效
	if displayID < 0 || displayID >= len(matches) {
		return "", fmt.Errorf("invalid display ID: %d", displayID)
	}

	return matches[displayID][1], nil
}

// Screenshot 捕获设备屏幕截图
// displayID: 可选参数，指定显示ID（默认为0）
// errorOk: 可选参数，是否在错误时返回黑色图像（默认为true）
func (d Device) Screenshot(displayIDOptional ...int) (img image.Image, err error) {
	displayID := 0
	if len(displayIDOptional) > 0 {
		displayID = displayIDOptional[0]
	}

	// 构建命令参数
	cmdArgs := []string{"screencap", "-p"}
	if displayID != 0 {
		realDisplayID, err := d.getRealDisplayID(displayID)
		if err != nil {
			return nil, err
		}
		cmdArgs = append(cmdArgs, "-d", realDisplayID)
	}

	// 执行截图命令
	pngBytes, err := d.RunShellCommandWithBytes(cmdArgs[0], cmdArgs[1:]...)
	if err != nil {
		return nil, err
	}

	// 解码PNG图像
	img, err = png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		// 错误处理：返回黑色图像
		width, height, _ := d.WindowSize()
		if width == 0 || height == 0 {
			width, height = 720, 1280 // 默认尺寸
		}

		blackImg := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				blackImg.Set(x, y, color.RGBA{0, 0, 0, 255})
			}
		}
		return blackImg, nil
	}

	return img, nil
}

// Battery 返回电池信息映射
func (d Device) Battery() (map[string]string, error) {
	output, err := d.RunShellCommand("dumpsys", "battery")
	if err != nil {
		return nil, fmt.Errorf("failed to get battery info: %w", err)
	}

	return parseBatteryOutput(output), nil
}

// parseBatteryOutput 解析 dumpsys battery 的输出
func parseBatteryOutput(output string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, ":") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		result[key] = value
	}

	// 添加标准化的状态描述
	if status, ok := result["status"]; ok {
		result["status_description"] = batteryStatusDescription(status)
	}

	if health, ok := result["health"]; ok {
		result["health_description"] = batteryHealthDescription(health)
	}

	return result
}

// 电池状态描述
func batteryStatusDescription(status string) string {
	switch status {
	case "1":
		return "unknown"
	case "2":
		return "charging"
	case "3":
		return "discharging"
	case "4":
		return "not charging"
	case "5":
		return "full"
	default:
		return "undefined"
	}
}

// 电池健康描述
func batteryHealthDescription(health string) string {
	switch health {
	case "1":
		return "unknown"
	case "2":
		return "good"
	case "3":
		return "overheat"
	case "4":
		return "dead"
	case "5":
		return "over voltage"
	case "6":
		return "unspecified failure"
	case "7":
		return "cold"
	default:
		return "undefined"
	}
}
