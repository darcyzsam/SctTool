package sctservice

import (
	"fmt"
	"os"
	"scttool/loghelp"
	"syscall"
	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
	"os/signal"
)

// IsRunning 通过 OpenMutex 纯查询互斥体是否存在，不创建、不获取、不记日志
// 用于 -e、remove、Stop 等查询场景
func (s *SctDeamon) IsRunning() bool {
	namePtr, err := syscall.UTF16PtrFromString(MUTEX_NAME)
	if err != nil {
		return false
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, namePtr)
	if err != nil {
		return false
	}
	windows.CloseHandle(handle)
	return true
}

// AcquireMutex 创建全局互斥体获取单实例锁
// 成功获取返回 true，已有实例运行返回 false
// 用于 -c 和 Service Start 等启动场景
func (s *SctDeamon) AcquireMutex() bool {
	namePtr, err := syscall.UTF16PtrFromString(MUTEX_NAME)
	if err != nil {
		s.logger.Error(false, fmt.Sprintf("转换互斥量名称失败：%v", err))
		return false
	}
	s.hMutex, err = windows.CreateMutex(nil, false, namePtr)
	if s.hMutex == 0 {
		s.logger.Error(false, fmt.Sprintf("创建互斥量失败：%v", err))
		return false
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(s.hMutex)
		s.hMutex = 0
		return false
	}
	if err != nil {
		s.logger.Warn(false, fmt.Sprintf("创建互斥量返回非预期错误（handle有效，继续运行）：%v", err))
	}
	return true
}

func (s *SctDeamon) releaseSingleInstance() {
	if s.hMutex != 0 {
		if err := windows.CloseHandle(s.hMutex); err != nil {
			s.logger.Warn(false, fmt.Sprintf("释放互斥量句柄失败: %v", err))
		}
		s.hMutex = 0
	}
}

type SctDeamon struct {
	pipeServ *SctPipe
	logger   *loghelp.Logger
	control  *SctControl
	hMutex   windows.Handle // 互斥量句柄
}

func NewSctDeamon(logger *loghelp.Logger, control *SctControl) *SctDeamon {
	return &SctDeamon{logger: logger, control: control}
}

func (s *SctDeamon) startPipe() error {
	s.pipeServ = NewSctPipe(PIPE_NAME, s.control, s.logger)
	// 启动管道
	err := s.pipeServ.CreatePipe()
	if err != nil {
		s.logger.Error(false, fmt.Sprintf("启动管道服务失败：%v", err))
		return err
	}
	go func() {
		s.logger.Info(false, "管道服务已启动，等待客户端...")
		s.pipeServ.Listen()
	}()
	return nil
}

func (s *SctDeamon) stopPipe() error {
	s.pipeServ.Close()
	s.logger.Info(false, "管道服务已关闭")
	return nil
}

func (s *SctDeamon) ShellRun() (err error) {
	// console 模式：输出到控制台+日志文件
	s.logger.SetConsoleMode(true)
	if !s.AcquireMutex() {
		return fmt.Errorf("检测到后台服务或前台程序已运行，禁止重复启动")
	}
	defer s.releaseSingleInstance()
	stopChan := make(chan struct{})
	err = s.startPipe()
	if err != nil {
		return err
	}
	s.logger.Info(true, "管道服务已启动，等待客户端...ctrl+c 退出服务")
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("收到系统信号，正在关闭，请稍候...")
		s.stopPipe()
		close(stopChan)
	}()
	<-stopChan
	s.logger.Info(true, "服务已正常退出")
	return nil
}

// ==================== Windows 服务框架  SctDeamon ====================
type MyService struct {
	stopChan chan struct{}
	dm       *SctDeamon
}

func NewMyService(dm *SctDeamon) *MyService {
	return &MyService{dm: dm}
}

func (m *MyService) Start(s service.Service) error {
	 // service 模式：仅写日志文件，不输出 console
	 m.dm.logger.SetConsoleMode(false)
	 // 启动预检：conf 可读 + log 可写
	 if err := m.dm.control.CheckConfReadable(); err != nil {
		 m.dm.logger.Error(false, fmt.Sprintf("启动预检失败: %v", err))
		 return err
	 }
	 if err := m.dm.logger.CheckWritable(); err != nil {
		 m.dm.logger.Error(false, fmt.Sprintf("启动预检失败: %v", err))
		 return err
	 }
	 // 获取单实例锁
	 if !m.dm.AcquireMutex() {
        err := fmt.Errorf("检测到后台服务或前台程序已运行，禁止重复启动")
        m.dm.logger.Warn(false, err.Error())
        return err
    }
    // 启动管道服务（同步执行）
    err := m.dm.startPipe()
    if err != nil {
        m.dm.releaseSingleInstance()
        m.dm.logger.Error(false, fmt.Sprintf("启动管道服务失败：%v", err))
        return err  // Windows 会捕获这个错误
    }
    // 初始化成功，准备 stopChan（在主线程中初始化，避免与 Stop 竞态）
    m.stopChan = make(chan struct{})
    // 启动业务协程
    go func() {
        defer m.dm.releaseSingleInstance()
        m.dm.logger.Info(false, "服务已启动")
        <-m.stopChan
    }()
    return nil
}
func (m *MyService) Stop(s service.Service) error {
	if !m.dm.IsRunning() {
		return fmt.Errorf("服务未运行，无法停止")
	}
	// 防御性检查：Start 失败时 stopChan 可能未初始化
	if m.stopChan == nil {
		m.dm.logger.Warn(false, "Stop 被调用但 stopChan 未初始化，跳过")
		return nil
	}
	// 安全关闭管道、释放锁、退出循环
	close(m.stopChan)
	m.dm.stopPipe()
	m.dm.logger.Info(false, "服务已正常退出")
	return nil
}
