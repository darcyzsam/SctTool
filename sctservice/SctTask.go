package sctservice

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"scttool/loghelp"

	"github.com/robfig/cron/v3"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// TaskController 定时调度引擎
// 使用 robfig/cron 管理 conf 中每个任务，支持热重载同步
type TaskController struct {
	cron          *cron.Cron
	control       *SctControl
	logger        *loghelp.Logger
	mu            sync.Mutex
	entryIDs      map[int]cron.EntryID   // taskID -> cron entryID
	running       bool
	stopChan      chan struct{}
	reloadTick    time.Duration            // conf 热重载周期
	runningTasks  map[int]time.Time        // taskID -> 开始执行时间（用于跳过重叠和状态查询）
	cmdProcesses  map[int]*exec.Cmd       // taskID -> 正在执行的 cmd（用于 stop 时主动 kill）
	cronExprs     map[int]string          // taskID -> 当前注册的 crontab 表达式（用于检测热重载时 crontab 变更）
	stopping      int32                    // 原子标志：1=正在停止，executeTask 据此判断是否被 stop kill
}

// NewTask 创建定时调度引擎实例
// control: 配置读写（加载 conf 中任务）
// logger: 日志（记录任务执行情况）
func NewTask(control *SctControl, logger *loghelp.Logger) *TaskController {
	return &TaskController{
		cron:         cron.New(), // 标准 5 位 crontab（分 时 日 月 周），与 SctControl 一致
		control:      control,
		logger:       logger,
		entryIDs:     make(map[int]cron.EntryID),
		reloadTick:   10 * time.Second,
		runningTasks: make(map[int]time.Time),
		cmdProcesses: make(map[int]*exec.Cmd),
		cronExprs:    make(map[int]string),
	}
}

// Start 启动调度引擎
func (t *TaskController) start() error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return nil
	}
	t.running = true
	t.stopChan = make(chan struct{})
	atomic.StoreInt32(&t.stopping, 0) // 重置停止标志
	t.mu.Unlock()

	// 启动 cron 调度器
	t.cron.Start()

	// 首次加载任务
	t.sync()

	// 启动 conf 热重载协程
	go func() {
		ticker := time.NewTicker(t.reloadTick)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.sync()
			case <-t.stopChan:
				return
			}
		}
	}()

	t.logger.Info(true, "定时调度引擎已启动")
	return nil
}

// Stop 停止调度引擎
// 先标记停止并 kill 正在执行的任务进程，再在锁外等待 cron 完全停止
func (t *TaskController) stop() {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return
	}
	t.running = false
	close(t.stopChan)
	atomic.StoreInt32(&t.stopping, 1)

	// 主动 kill 正在执行的任务进程树，使其尽快返回
	for taskID, cmd := range t.cmdProcesses {
		if cmd != nil && cmd.Process != nil {
			killProcessTree(cmd.Process, t.logger)
		}
		delete(t.cmdProcesses, taskID)
	}
	t.entryIDs = make(map[int]cron.EntryID)
	t.cronExprs = make(map[int]string)
	t.mu.Unlock()

	// 在锁外停止 cron 并等待正在执行的回调完成
	// 此时 executeTask 的 defer 需要拿锁，必须先释放
	ctx := t.cron.Stop()
	<-ctx.Done()

	t.logger.Info(true, "定时调度引擎已停止")
}

// Commander 命令入口：start / stop
func (t *TaskController) Commander(cmd string) error {
	switch cmd {
	case "start":
		return t.start()
	case "stop", "quit":
		t.stop()
		return nil
	default:
		return fmt.Errorf("未知命令 %s", cmd)
	}
}

// sync 加锁后同步 conf 任务到 cron（供热重载协程和 start 调用）
func (t *TaskController) sync() {
	// 先在不持锁的情况下读取配置（避免文件 I/O 阻塞锁）
	success, _, config, _ := t.control.LoadConfig()
	t.mu.Lock()
	invalidIDs := t.syncLocked(success, config)
	t.mu.Unlock()

	// 在锁外从 conf 中删除非法 crontab 的任务
	if len(invalidIDs) > 0 {
		t.removeInvalidTasksFromConf(invalidIDs)
	}
}

// removeInvalidTasksFromConf 从 conf 中删除指定 ID 的非法任务，并记录日志
// 此方法在锁外调用（文件 I/O），避免阻塞调度锁
func (t *TaskController) removeInvalidTasksFromConf(ids []int) {
	for _, id := range ids {
		success, msg, taskName := t.control.DeleteTask(id)
		if success {
			t.logger.Warnf("任务[%d:%s] 已从配置文件中删除（crontab 非法）", id, taskName)
		} else {
			t.logger.Errorf("任务[%d] 从配置文件中删除失败: %s", id, msg)
		}
	}
}

// syncLocked 内部同步逻辑（调用方需持有锁）
// 对比 conf 中的任务与当前已注册的 cron 任务，增删任务实现热更新
// 空任务时不输出日志，静默处理
// 返回：非法 crontab 的任务 ID 列表（供调用方在锁外从 conf 中删除）
func (t *TaskController) syncLocked(success bool, config Config) []int {
	if !success {
		// conf 不存在/为空：清空所有已注册任务（不输出日志）
		t.clearAllLocked()
		return nil
	}

	// 空任务列表：清空已注册任务，不输出日志
	if len(config.Tasks) == 0 {
		t.clearAllLocked()
		return nil
	}

	// 记录 conf 中所有任务 ID，用于找出被删除的任务
	liveIDs := make(map[int]bool)
	invalidIDs := make([]int, 0) // 非法 crontab 的任务 ID 列表
	for _, task := range config.Tasks {
		// cron 表达式预校验
		if ok, vmsg := t.control.ValidateCron(task.Cronstr); !ok {
			t.logger.Errorf("任务[%d:%s] crontab 非法: %s", task.ID, task.Taskname, vmsg)
			// 从 cron 调度中移除该任务
			if entryID, exists := t.entryIDs[task.ID]; exists {
				t.cron.Remove(entryID)
				delete(t.entryIDs, task.ID)
				delete(t.cronExprs, task.ID)
				t.logger.Warnf("任务[%d:%s] 已从调度中移除（crontab 非法）", task.ID, task.Taskname)
			}
			invalidIDs = append(invalidIDs, task.ID)
			continue
		}

		liveIDs[task.ID] = true

		isUpdate := false
		if existingExpr, exists := t.cronExprs[task.ID]; exists {
			if existingExpr == task.Cronstr {
				continue // crontab 未变更，跳过
			}
			// crontab 变更，移除旧的重新注册
			t.cron.Remove(t.entryIDs[task.ID])
			delete(t.entryIDs, task.ID)
			isUpdate = true
		}

		// 注册任务到 cron
		taskCopy := task // 捕获迭代副本，供闭包使用
		entryID, err := t.cron.AddFunc(task.Cronstr, func() {
			t.executeTask(taskCopy)
		})
		if err != nil {
			t.logger.Errorf("任务[%d:%s] 注册失败: %v", task.ID, task.Taskname, err)
			continue
		}
		t.entryIDs[task.ID] = entryID
		t.cronExprs[task.ID] = task.Cronstr
		if isUpdate {
			t.logger.Infof("任务[%d:%s] crontab 已更新为 %s", task.ID, task.Taskname, task.Cronstr)
		} else {
			t.logger.Infof("任务[%d:%s] 已加载，crontab=%s", task.ID, task.Taskname, task.Cronstr)
		}
	}

	// 移除 conf 中已删除的任务
	for id, entryID := range t.entryIDs {
		if !liveIDs[id] {
			t.cron.Remove(entryID)
			delete(t.entryIDs, id)
			delete(t.cronExprs, id)
			t.logger.Infof("任务[%d] 已从调度中移除", id)
		}
	}

	// 从 conf 中删除非法 crontab 的任务（由调用方在锁外执行）
	return invalidIDs
}

// clearAllLocked 清空所有已登记任务（调用方需持有锁）
func (t *TaskController) clearAllLocked() {
	for id, entryID := range t.entryIDs {
		t.cron.Remove(entryID)
		delete(t.entryIDs, id)
		delete(t.cronExprs, id)
	}
}

// executeTask 到点执行单个任务命令
// 支持超时控制和跳过重叠（上一次还在跑则跳过本次）
func (t *TaskController) executeTask(task Task) {
	// 检查是否正在执行（跳过重叠）
	t.mu.Lock()
	if _, running := t.runningTasks[task.ID]; running {
		t.mu.Unlock()
		t.logger.Warnf("任务[%d:%s] 上一次执行尚未结束，跳过本次触发", task.ID, task.Taskname)
		return
	}
	t.runningTasks[task.ID] = time.Now()
	t.mu.Unlock()

	// 确保执行结束后清理 running 状态和 cmd 引用
	defer func() {
		t.mu.Lock()
		delete(t.runningTasks, task.ID)
		delete(t.cmdProcesses, task.ID)
		t.mu.Unlock()
	}()

	t.logger.Infof("任务[%d:%s] 开始执行: %s", task.ID, task.Taskname, task.Cmd)

	// 构造命令
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", task.Cmd)
	} else {
		cmd = exec.Command("sh", "-c", task.Cmd)
	}

	// 超时控制：启动一个定时器，超时后 kill 整个进程树
	var timedOut int32 // 原子标志：0=正常，1=超时
	var timeoutTimer *time.Timer
	if task.Timeout > 0 {
		timeoutTimer = time.AfterFunc(time.Duration(task.Timeout)*time.Second, func() {
			atomic.StoreInt32(&timedOut, 1)
			killProcessTree(cmd.Process, t.logger)
		})
	}

	// 启动命令并记录 cmd 引用（供 stop 时主动 kill）
	// 手动管理 stdout/stderr 以便在 Start 后存入 cmdProcesses，再 Wait 收集输出
	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	if err := cmd.Start(); err != nil {
		if timeoutTimer != nil {
			timeoutTimer.Stop()
		}
		t.logger.Errorf("任务[%d:%s] 启动失败: %v", task.ID, task.Taskname, err)
		return
	}
	t.mu.Lock()
	t.cmdProcesses[task.ID] = cmd
	t.mu.Unlock()

	cmd.Wait()

	if timeoutTimer != nil {
		timeoutTimer.Stop()
	}

	// 超时检测：通过原子标志判断，而非字符串匹配
	if atomic.LoadInt32(&timedOut) == 1 {
		t.logger.Errorf("任务[%d:%s] 执行超时（已运行 %d 秒，进程已终止）",
			task.ID, task.Taskname, task.Timeout)
		return
	}

	// 停止检测：被 stop() 主动 kill 的任务记为"任务被停止"，不记录输出
	if atomic.LoadInt32(&t.stopping) == 1 {
		t.logger.Warnf("任务[%d:%s] 被停止（服务退出）", task.ID, task.Taskname)
		return
	}

	// 检查退出码：0xc000013a (STATUS_CONTROL_C_EXIT) 表示被 Ctrl+C 中断
	if cmd.ProcessState != nil {
		exitCode := cmd.ProcessState.ExitCode()
		if exitCode == 0xc000013a {
			t.logger.Warnf("任务[%d:%s] 被用户中断（Ctrl+C）", task.ID, task.Taskname)
			return
		}
		if exitCode != 0 {
			outStr := gbkToUTF8([]byte(outBuf.String()))
			t.logger.Errorf("任务[%d:%s] 执行失败: exit code %d\n输出: %s",
				task.ID, task.Taskname, exitCode, outStr)
			return
		}
	}

	// 成功仅记录执行成功，不记录输出
	t.logger.Infof("任务[%d:%s] 执行成功", task.ID, task.Taskname)
}

// GetRunningStatus 返回当前正在执行的任务状态信息
// 格式：每行一个任务 "taskid|taskname|已执行秒数|超时秒数(0=不限)"
func (t *TaskController) GetRunningStatus() string {
	// 先在不持锁的情况下读取配置（避免文件 I/O 阻塞锁）
	loadSuccess, _, config, _ := t.control.LoadConfig()
	taskMap := make(map[int]Task)
	for _, task := range config.Tasks {
		taskMap[task.ID] = task
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.runningTasks) == 0 {
		return "当前没有正在执行的任务"
	}

	now := time.Now()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("当前正在执行的任务（共 %d 个）:\n", len(t.runningTasks)))

	for taskID, startTime := range t.runningTasks {
		elapsed := int(now.Sub(startTime).Seconds())
		if !loadSuccess {
			// 配置文件不可读时只显示 taskID 和执行时长
			sb.WriteString(fmt.Sprintf("  任务[%d] 已执行 %d秒（配置信息不可用）\n", taskID, elapsed))
			continue
		}
		task := taskMap[taskID]
		timeoutStr := "不限"
		if task.Timeout > 0 {
			timeoutStr = fmt.Sprintf("%d秒", task.Timeout)
		}
		sb.WriteString(fmt.Sprintf("  任务[%d:%s] 已执行 %d秒（超时: %s）\n",
			taskID, task.Taskname, elapsed, timeoutStr))
	}

	return strings.TrimSpace(sb.String())
}

// gbkToUTF8 将可能的 GBK 输出转换为 UTF-8 字符串
// 优先检查是否已经是合法 UTF-8（如 console 模式下设置了 CP 65001），
// 如果是则直接返回；否则按 GBK 解码（service 模式下 cmd.exe 默认输出 GBK）
func gbkToUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	decoder := simplifiedchinese.GBK.NewDecoder()
	decoded, err := decoder.Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(decoded)
}

// killProcessTree 终止进程及其所有子进程
// Windows 上通过 taskkill /T /F /PID 杀掉整个进程树，失败则每5秒重试，共3次
func killProcessTree(proc *os.Process, logger *loghelp.Logger) {
	if proc == nil {
		return
	}
	pid := proc.Pid
	if runtime.GOOS == "windows" {
		// /T = 包含子进程, /F = 强制终止
		for attempt := 1; attempt <= 3; attempt++ {
			err := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", pid)).Run()
			if err == nil {
				return
			}
			logger.Error(false, fmt.Sprintf("killProcessTree: taskkill 尝试 %d/3 失败 (PID=%d): %v", attempt, pid, err))
			if attempt < 3 {
				time.Sleep(5 * time.Second)
			}
		}
	} else {
		if err := proc.Kill(); err != nil {
			logger.Error(false, fmt.Sprintf("killProcessTree: Kill 失败 (PID=%d): %v", pid, err))
		}
	}
}