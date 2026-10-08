package sctservice

import (
	"syscall"
	"golang.org/x/sys/windows"
	"fmt"
	"scttool/loghelp"
)

// SctPipe 命名管道封装
type SctPipe struct {
	pipeName string
	handle   windows.Handle
	running  bool
	task     *TaskController          // 定时任务实例
	logger   *loghelp.Logger
	control  *SctControl
}

// NewSctPipe 创建管道实例
func NewSctPipe(pipeName string, control *SctControl, logger *loghelp.Logger) *SctPipe {
	return &SctPipe{
		pipeName: pipeName,
		handle:   windows.InvalidHandle,
		running:  false,
		control:  control,
		logger:   logger,
	}
}
// Start 创建并启动管道
func (n *SctPipe) CreatePipe() error {
	if n.running {
		return nil
	}
	pipeMode := windows.PIPE_TYPE_MESSAGE | windows.PIPE_READMODE_MESSAGE | windows.PIPE_WAIT
	ptrPipeName, err := syscall.UTF16PtrFromString(n.pipeName)
	if err != nil {
		return err
	}
	handle, err := windows.CreateNamedPipe(
		ptrPipeName,
		windows.PIPE_ACCESS_DUPLEX,
		uint32(pipeMode),
		1,
		1024,
		1024,
		0,
		nil,
	)
	if err != nil {
		return err
	}
	n.task = NewTask(n.control, n.logger)
	err = n.task.Commander("start")
	if err != nil {
		windows.CloseHandle(handle)
		return err
	}
	n.handle = handle
	n.running = true
	return nil
}
// Listen 开始监听客户端（修复了 ReadFile / WriteFile 调用）
func (n *SctPipe) Listen() {

	if !n.running || n.handle == windows.InvalidHandle {
		return
	}
	for n.running {
		// 等待客户端连接
		err := windows.ConnectNamedPipe(n.handle, nil)
		//关闭时直接退出
		if !n.running {
			break
		}
		if err != nil {
			windows.DisconnectNamedPipe(n.handle) // 防御性：确保管道状态干净
			continue
		}
		// ==================== 修复 ReadFile ====================
		buf := make([]byte, 4096)
		var readLen uint32
		err = windows.ReadFile(n.handle, buf, &readLen, nil)
		if err != nil {
			n.logger.Warn(false, fmt.Sprintf("管道读取失败: %v", err))
			windows.DisconnectNamedPipe(n.handle)
			continue
		}
		msg := string(buf[:readLen])
		// 业务处理
		resp, procErr := n.processor(msg)
		if procErr != nil {
			resp = procErr.Error()
		}

		// ==================== 修复 WriteFile ====================
		if resp != "" {
			var writeLen uint32
			err = windows.WriteFile(n.handle, []byte(resp), &writeLen, nil)
			if err != nil {
				n.logger.Warn(false, fmt.Sprintf("管道写入失败: %v", err))
			}
		}
		windows.DisconnectNamedPipe(n.handle)
	}
}


// Close 安全关闭管道
func (n *SctPipe) Close() {
	n.task.Commander("stop")
	if !n.running || n.handle == windows.InvalidHandle {
		return
	}
	n.running = false
	//先取消阻塞的 ConnectNamedPipe
	windows.CancelIoEx(n.handle, nil)

	err := windows.CloseHandle(n.handle)
	n.handle = windows.InvalidHandle
	if err != nil {
		n.logger.Warn(false, fmt.Sprintf("关闭管道句柄失败: %v", err))
	}
}
func (n *SctPipe) processor(msg string) (string, error) {
	if msg == "status" {
		return n.task.GetRunningStatus(), nil
	}
	return "", fmt.Errorf("未知命令：%s", msg)
}
