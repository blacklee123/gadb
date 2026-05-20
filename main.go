package main

import (
	"image/png"
	"log"
	"os"

	"github.com/blacklee123/go-adb/adb"
)

func main() {
	// for {
	// 	client, err := adb.NewClient()
	// 	if err != nil {
	// 		log.Println("连接 adb 服务失败:", err)
	// 		time.Sleep(time.Second * 3)
	// 		continue
	// 	}
	// 	events, cancel, err := client.TrackDevices()
	// 	if err != nil {
	// 		log.Println("启动设备跟踪失败:", err)
	// 		time.Sleep(time.Second * 3)
	// 		continue
	// 	}
	// 	defer cancel() // 确保程序退出时停止监听

	// 	for event := range events {
	// 		if event.Present {
	// 			log.Printf("设备连接: %s [状态: %s]", event.Serial, event.Status)
	// 		} else {
	// 			log.Printf("设备断开: %s", event.Serial)
	// 		}

	// 		// 特殊状态处理
	// 		switch event.Status {
	// 		case "unauthorized":
	// 			log.Printf("设备 %s 需要授权", event.Serial)
	// 		case "offline":
	// 			log.Printf("设备 %s 已离线", event.Serial)
	// 		}
	// 	}
	// }
	client, err := adb.NewClient()
	if err != nil {
		log.Fatal("连接 adb 服务失败:", err)
	}
	device, err := client.GetDevice("94P0220A15026515")
	if err != nil {
		log.Fatal("获取设备失败:", err)
	}
	log.Println(device.GetProp("ro.product.name"))
	log.Println(device.GetScreenSize())
	log.Println(device.WindowSize())
	img, err := device.Screenshot()
	if err != nil {
		// 处理错误
	}

	// 保存截图
	file, _ := os.Create("screenshot.png")
	png.Encode(file, img)
	file.Close()
	log.Println(device.Battery())
}
