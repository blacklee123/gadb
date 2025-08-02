package gadb

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const AdbServerPort = 5037
const AdbDaemonPort = 5555

type Client struct {
	host string
	port int
}

func NewClient() (Client, error) {
	return NewClientWith("localhost")
}

func NewClientWith(host string, port ...int) (adbClient Client, err error) {
	if len(port) == 0 {
		port = []int{AdbServerPort}
	}
	adbClient.host = host
	adbClient.port = port[0]

	var tp transport
	if tp, err = adbClient.createTransport(); err != nil {
		return Client{}, err
	}
	defer func() { _ = tp.Close() }()

	return
}

func (c Client) ServerVersion() (version int, err error) {
	var resp string
	if resp, err = c.executeCommand("host:version"); err != nil {
		return 0, err
	}

	var v int64
	if v, err = strconv.ParseInt(resp, 16, 64); err != nil {
		return 0, err
	}

	version = int(v)
	return
}

func (c Client) DeviceSerialList() (serials []string, err error) {
	var resp string
	if resp, err = c.executeCommand("host:devices"); err != nil {
		return
	}

	lines := strings.Split(resp, "\n")
	serials = make([]string, 0, len(lines))

	for i := range lines {
		fields := strings.Fields(lines[i])
		if len(fields) < 2 {
			continue
		}
		serials = append(serials, fields[0])
	}

	return
}

func (c Client) DeviceList() (devices []Device, err error) {
	var resp string
	if resp, err = c.executeCommand("host:devices-l"); err != nil {
		return
	}

	lines := strings.Split(resp, "\n")
	devices = make([]Device, 0, len(lines))

	for i := range lines {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 || len(fields[0]) == 0 {
			debugLog(fmt.Sprintf("can't parse: %s", line))
			continue
		}

		sliceAttrs := fields[2:]
		mapAttrs := map[string]string{}
		for _, field := range sliceAttrs {
			split := strings.Split(field, ":")
			if len(split) == 1 {
				continue
			}
			key, val := split[0], split[1]
			mapAttrs[key] = val
		}
		devices = append(devices, Device{adbClient: c, serial: fields[0], attrs: mapAttrs})
	}

	return
}

func (c Client) ForwardList() (deviceForward []DeviceForward, err error) {
	var resp string
	if resp, err = c.executeCommand("host:list-forward"); err != nil {
		return nil, err
	}

	lines := strings.Split(resp, "\n")
	deviceForward = make([]DeviceForward, 0, len(lines))

	for i := range lines {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		deviceForward = append(deviceForward, DeviceForward{Serial: fields[0], Local: fields[1], Remote: fields[2]})
	}

	return
}

func (c Client) ForwardKillAll() (err error) {
	_, err = c.executeCommand("host:killforward-all", true)
	return
}

func (c Client) Connect(ip string, port ...int) (err error) {
	if len(port) == 0 {
		port = []int{AdbDaemonPort}
	}

	var resp string
	if resp, err = c.executeCommand(fmt.Sprintf("host:connect:%s:%d", ip, port[0])); err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "connected to") && !strings.HasPrefix(resp, "already connected to") {
		return fmt.Errorf("adb connect: %s", resp)
	}
	return
}

func (c Client) Disconnect(ip string, port ...int) (err error) {
	cmd := fmt.Sprintf("host:disconnect:%s", ip)
	if len(port) != 0 {
		cmd = fmt.Sprintf("host:disconnect:%s:%d", ip, port[0])
	}

	var resp string
	if resp, err = c.executeCommand(cmd); err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "disconnected") {
		return fmt.Errorf("adb disconnect: %s", resp)
	}
	return
}

func (c Client) DisconnectAll() (err error) {
	var resp string
	if resp, err = c.executeCommand("host:disconnect:"); err != nil {
		return err
	}

	if !strings.HasPrefix(resp, "disconnected everything") {
		return fmt.Errorf("adb disconnect all: %s", resp)
	}
	return
}

func (c Client) KillServer() (err error) {
	var tp transport
	if tp, err = c.createTransport(); err != nil {
		return err
	}
	defer func() { _ = tp.Close() }()

	err = tp.Send("host:kill")
	return
}

func (c Client) createTransport() (tp transport, err error) {
	return newTransport(fmt.Sprintf("%s:%d", c.host, c.port))
}

func (c Client) executeCommand(command string, onlyVerifyResponse ...bool) (resp string, err error) {
	if len(onlyVerifyResponse) == 0 {
		onlyVerifyResponse = []bool{false}
	}

	var tp transport
	if tp, err = c.createTransport(); err != nil {
		return "", err
	}
	defer func() { _ = tp.Close() }()

	if err = tp.Send(command); err != nil {
		return "", err
	}
	if err = tp.VerifyResponse(); err != nil {
		return "", err
	}

	if onlyVerifyResponse[0] {
		return
	}

	if resp, err = tp.UnpackString(); err != nil {
		return "", err
	}
	return
}

// DeviceEvent 表示设备状态变化事件
type DeviceEvent struct {
	Present bool   // 设备是否出现（true=连接，false=断开）
	Serial  string // 设备序列号
	Status  string // 设备状态（"device", "offline", "unauthorized", "absent"等）
}

// TrackDevices 开始跟踪设备状态变化
// 返回事件通道和取消函数：
//   - events: 接收设备状态变化事件的通道
//   - cancel: 调用后停止跟踪并关闭连接
func (c Client) TrackDevices() (events <-chan DeviceEvent, cancel func() error, err error) {
	tp, err := c.createTransport()
	if err != nil {
		return nil, nil, err
	}

	if err := tp.Send("host:track-devices"); err != nil {
		_ = tp.Close()
		return nil, nil, err
	}

	if err := tp.VerifyResponse(); err != nil {
		_ = tp.Close()
		return nil, nil, err
	}

	// 创建事件通道和取消上下文
	eventCh := make(chan DeviceEvent)
	ctx, cancelCtx := context.WithCancel(context.Background())
	var closeOnce sync.Once

	// 存储上一次的设备状态（serial -> status）
	prevDevices := make(map[string]string)

	// 启动监听goroutine
	go func() {
		defer close(eventCh)
		defer closeOnce.Do(func() { _ = tp.Close() })

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// 读取设备状态更新
				resp, err := tp.UnpackString()
				if err != nil {
					// 连接关闭或错误，退出goroutine
					return
				}

				// 解析当前设备状态
				currDevices := make(map[string]string)
				for _, line := range strings.Split(resp, "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}

					fields := strings.Fields(line)
					if len(fields) < 2 {
						continue
					}
					serial := fields[0]
					status := fields[1]
					currDevices[serial] = status
				}

				// 检测设备消失
				for serial := range prevDevices {
					if _, exists := currDevices[serial]; !exists {
						eventCh <- DeviceEvent{
							Present: false,
							Serial:  serial,
							Status:  "absent",
						}
					}
				}

				// 检测设备出现或状态变化
				for serial, status := range currDevices {
					prevStatus, exists := prevDevices[serial]
					if !exists || prevStatus != status {
						eventCh <- DeviceEvent{
							Present: true,
							Serial:  serial,
							Status:  status,
						}
					}
				}

				// 更新上一次的设备状态
				prevDevices = currDevices
			}
		}
	}()

	// 创建取消函数
	cancelFn := func() error {
		var closeErr error
		closeOnce.Do(func() {
			cancelCtx() // 取消上下文
			closeErr = tp.Close()
		})
		return closeErr
	}

	return eventCh, cancelFn, nil
}
