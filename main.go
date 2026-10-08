package main

import (
	"fmt"
	"os"
	sctcrontab "scttool/Crontab"
	"scttool/loghelp"
	"scttool/sctservice"
	"strconv"
	"strings"
	"syscall"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

// isConsole 检测是否是控制台调用
// Windows服务运行时没有控制台，通过检查标准输入句柄来判断
func isConsole() bool {
	stdinHandle, _ := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if stdinHandle == syscall.InvalidHandle {
		return false
	}

	var mode uint32
	err := syscall.GetConsoleMode(stdinHandle, &mode)
	return err == nil
}

// SendPipeMessage 通过命名管道向运行中的服务发送命令
func SendPipeMessage(msg string) (string, error) {
	ptrPipeName, err := syscall.UTF16PtrFromString(sctservice.PIPE_NAME)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(
		ptrPipeName,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			fmt.Fprintf(os.Stderr, "关闭管道句柄失败: %v\n", err)
		}
	}()

	var wLen uint32
	err = windows.WriteFile(handle, []byte(msg), &wLen, nil)
	if err != nil {
		return "", err
	}

	buf := make([]byte, 4096)
	var rLen uint32
	err = windows.ReadFile(handle, buf, &rLen, nil)
	if err != nil {
		return "", err
	}

	return string(buf[:rLen]), nil
}

func show_append_help() {
	fmt.Println("添加任务")
	fmt.Println("参数:")
	fmt.Println("tn = [\"taskname\"] cr = [\"cronstr\"] cmd = [\"command\"] [tm=秒数]")
	fmt.Println("taskname: 任务名称")
	fmt.Println("cronstring: 任务表达式")
	fmt.Println("command: 任务命令")
	fmt.Println("tm: 超时秒数（可选，默认0=不限）")
}

func show_test_help() {
	fmt.Println("测试配置")
	fmt.Println("参数:")
	fmt.Println("tn = [\"taskname\"] cr = [\"cronstr\"] cmd = [\"command\"] [tm=秒数]")
	fmt.Println("taskname: 任务名称")
	fmt.Println("cronstring: 任务表达式")
	fmt.Println("command: 任务命令")
	fmt.Println("tm: 超时秒数（可选，默认0=不限）")
	os.Exit(0)
}

func show_first_help() {
	fmt.Println("")
	fmt.Println("描述:")
	fmt.Println("\tSCTool 是用来调度计划任务的命令行工具")
	fmt.Println("用法:")
	fmt.Println("sct <option> [command]")
	fmt.Println("<option>:")
	fmt.Println("\tinstall: 安装服务")
	fmt.Println("\tremove: 卸载服务")
	fmt.Println("\tstart: 启动服务")
	fmt.Println("\tstop: 停止服务")
	fmt.Println("\trestart: 重启服务")
	fmt.Println("\t-c: Shell运行服务")
	fmt.Println("\t-l: 列出所有任务")
	fmt.Println("\t-t: 测试任务表达式")
	fmt.Println("\t-a [Command]: 添加任务")
	fmt.Println("\t-d [TaskID|all]: 删除任务（all=清空全部）")
	fmt.Println("\t-p: 列出所有任务的最近10个执行时间点")
	fmt.Println("\t-e: 查看当前正在执行的任务")
}

func one_args(sctDm *sctservice.SctDeamon, sctCrl *sctservice.SctControl, logger *loghelp.Logger) {
	var err error
	svcConfig := &service.Config{
		Name:        sctservice.SERVICE_NAME,
		DisplayName: sctservice.SERVICE_DISPLAY_NAME,
		Description: sctservice.SERVICE_DESC,
		Arguments:   []string{"-s"},
	}
	mySvc := sctservice.NewMyService(sctDm)
	svc, err := service.New(mySvc, svcConfig)
	if err != nil {
		logger.Error(true, err.Error())
		fmt.Println("服务初始化失败:", err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "-d":
		fmt.Println("参数错误: -d 需要一个任务ID或 all")
		fmt.Println("sct -d [TaskID]  删除指定任务")
		fmt.Println("sct -d all       清空全部任务")
		os.Exit(0)
	case "-t":
		show_test_help()
		os.Exit(0)
	case "-a":
		show_append_help()
		os.Exit(0)
	case "install":
		fmt.Println("sct service is installing.....")
		err = svc.Install()
		if err != nil {
			logger.Error(true, err.Error())
			fmt.Println("安装失败:", err)
			os.Exit(1)
		}
		fmt.Println("Sct service was installed")
		os.Exit(0)

	case "remove":
		if sctDm.IsRunning() {
			fmt.Println("sct service is running, please stop it first")
			os.Exit(1)
		}
		err = svc.Uninstall()
		if err != nil {
			logger.Error(true, err.Error())
			fmt.Println("卸载失败:", err)
			os.Exit(1)
		}
		fmt.Println("SctService uninstall successfully")
		os.Exit(0)

	case "start":
		if sctDm.IsRunning() {
			fmt.Println("SctService 已在运行（可能是 -c 模式），无需重复启动")
			os.Exit(1)
		}
		fmt.Println("sct service is starting.....")
		err = svc.Start()
		if err != nil {
			logger.Error(true, err.Error())
			fmt.Println("启动失败:", err)
			os.Exit(1)
		}
		fmt.Println("SctService start successfully")
		os.Exit(0)

	case "stop":
		fmt.Println("sct service is stopping.....")
		err = svc.Stop()
		if err != nil {
			logger.Error(true, err.Error())
			fmt.Println("停止失败:", err)
			os.Exit(1)
		}
		fmt.Println("SctService stop successfully")
		os.Exit(0)

	case "restart":
		if sctDm.IsRunning() {
			fmt.Println("SctService 正在以 -c 模式运行，无法通过服务命令重启")
			os.Exit(1)
		}
		fmt.Println("sct service is restarting.....")
		err = svc.Restart()
		if err != nil {
			logger.Error(true, err.Error())
			fmt.Println("重启失败:", err)
			os.Exit(1)
		}
		fmt.Println("SctService restart successfully")
		os.Exit(0)

	case "-l": // -l 列出任务
		success, msg, config, _ := sctCrl.LoadConfig()
		if !success {
			fmt.Println(msg)
			os.Exit(0)
		}
		fmt.Println("列出所有任务:")
		for _, task := range config.Tasks {
			timeoutStr := "不限"
			if task.Timeout > 0 {
				timeoutStr = fmt.Sprintf("%d秒", task.Timeout)
			}
			fmt.Printf("\ttaskid:%d,taskname:%s,cronstr:%s,cmd:%s,timeout:%s\n",
				task.ID, task.Taskname, task.Cronstr, task.Cmd, timeoutStr)
		}
		os.Exit(0)

	case "-p":
		success, msg, config, _ := sctCrl.LoadConfig()
		if !success {
			fmt.Println(msg)
			os.Exit(0)
		}
		if len(config.Tasks) == 0 {
			fmt.Println("没有已注册的任务")
			os.Exit(0)
		}
		for _, task := range config.Tasks {
			timeoutStr := "不限"
			if task.Timeout > 0 {
				timeoutStr = fmt.Sprintf("%d秒", task.Timeout)
			}
			fmt.Printf("任务[%d:%s] 表达式=%s 超时=%s\n", task.ID, task.Taskname, task.Cronstr, timeoutStr)
			sOk, _, scheduleTimes := sctcrontab.GetAllTaskScheduleFullNext10(task.Cronstr)
			if sOk && len(scheduleTimes) > 0 {
				fmt.Println("		接下来10个执行时间点:")
				for i, t := range scheduleTimes {
					fmt.Printf("			%d. %s\n", i+1, t.Format("2006-01-02 15:04:05"))
				}
			} else {
				fmt.Println("		无法计算执行时间点（表达式可能不合法）")
			}
		}
		os.Exit(0)

	case "-e": // -e 查看正在执行的任务
		if !sctDm.IsRunning() {
			fmt.Println("程序未运行，无法查询执行状态")
			os.Exit(0)
		}
		resp, err := SendPipeMessage("status")
		if err != nil {
			fmt.Println("查询失败:", err)
			os.Exit(1)
		}
		fmt.Println(resp)
		os.Exit(0)

	case "-c": // -c Shell运行服务
		fmt.Println("sct service is running in console.....")
		// 设置控制台输出代码页为 UTF-8，防止中文乱码
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		setCP := kernel32.NewProc("SetConsoleOutputCP")
		ret, _, _ := setCP.Call(uintptr(65001))
		if ret == 0 {
			fmt.Fprintf(os.Stderr, "SetConsoleOutputCP(65001) failed, Chinese may display as garbled text\n")
		}
		// 启动预检：conf 可读 + log 可写
		if err := sctCrl.CheckConfReadable(); err != nil {
			fmt.Println("启动预检失败:", err)
			os.Exit(1)
		}
		if err := logger.CheckWritable(); err != nil {
			fmt.Println("启动预检失败:", err)
			os.Exit(1)
		}
		// 检查是否有任务，空任务直接退出
		success, _, config, _ := sctCrl.LoadConfig()
		if !success || len(config.Tasks) == 0 {
			fmt.Println("没有任务，请先使用 -a 添加任务后再运行")
			os.Exit(0)
		}
		err := sctDm.ShellRun()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		os.Exit(0)

	case "-s": //（由 Windows 服务管理器调用）
		if isConsole() {
			fmt.Println("-s 由windows服务内部调用")
			os.Exit(0)
		}
		if err := svc.Run(); err != nil {
			logger.Error(false, err.Error())
			os.Exit(1)
		}
		os.Exit(0)

	default:
		fmt.Printf("命令错误: 未识别的命令 \"%s\"\n", os.Args[1])
		show_first_help()
		os.Exit(0)
	}
}

func two_args(sctDm *sctservice.SctDeamon, sctCrl *sctservice.SctControl, logger *loghelp.Logger) {
	args := os.Args[1:]
	arg := strings.TrimSpace(args[0])

	// -t 测试任务表达式，校验参数并列出前10个定时时间点
	if arg == "-t" {
		// tn/cr/cmd 必填（3个），tm 可选（1个），共 3~4 个参数
		if len(args) < 4 || len(args) > 5 {
			show_test_help()
			os.Exit(0)
		}
		param := strings.Join(args[1:], " ")
		params := strings.TrimSpace(param)
		success, msg, tn, cr, cmd, tm := sctCrl.ValidateParams(params)
		if !success {
			fmt.Println(msg)
			os.Exit(0)
		}
		fmt.Println("测试配置成功:")
		fmt.Println("\t任务名称:", tn)
		fmt.Println("\t任务表达式:", cr)
		fmt.Println("\t任务命令:", cmd)
		if tm > 0 {
			fmt.Printf("\t超时: %d秒\n", tm)
		} else {
			fmt.Println("\t超时: 不限")
		}

		// 列出前10个定时时间点
		success, _, scheduleTimes := sctcrontab.GetAllTaskScheduleFullNext10(cr)
		if success && len(scheduleTimes) > 0 {
			fmt.Println("\t接下来10个执行时间点:")
			for i, t := range scheduleTimes {
				fmt.Printf("\t\t%d. %s\n", i+1, t.Format("2006-01-02 15:04:05"))
			}
		} else {
			fmt.Println("\t无法计算执行时间点（表达式可能不合法）")
		}
		os.Exit(0)
	}

	// -a 添加任务
	if arg == "-a" {
		// tn/cr/cmd 必填（3个），tm 可选（1个），共 3~4 个参数
		if len(args) < 4 || len(args) > 5 {
			show_append_help()
			os.Exit(0)
		}
		param := strings.Join(args[1:], " ")
		params := strings.TrimSpace(param)
		success, msg, tn, cr, cmd, tm := sctCrl.ValidateParams(params)
		if !success {
			fmt.Println(msg)
			os.Exit(0)
		}
		success, msg = sctCrl.SaveConfig(tn, cr, cmd, tm)
		if !success {
			fmt.Println(msg)
			os.Exit(0)
		}
		logger.Info(true, fmt.Sprintf("添加任务: tn=%s cr=%s cmd=%s tm=%d", tn, cr, cmd, tm))
		fmt.Println("添加任务成功")
		os.Exit(0)
	}

	// -d 删除任务
	if arg == "-d" {
		if len(args) != 2 {
			fmt.Println("参数错误: -d 需要一个任务ID或 all")
			fmt.Println("sct -d [TaskID] 或 sct -d all")
			os.Exit(0)
		}
		// -d all 清空全部任务
		if strings.ToLower(args[1]) == "all" {
			success, msg, deletedTasks := sctCrl.DeleteAllTasks()
			if !success {
				fmt.Println(msg)
				os.Exit(0)
			}
			for _, task := range deletedTasks {
				logger.Info(true, fmt.Sprintf("删除任务: [%d:%s]", task.ID, task.Taskname))
			}
			logger.Info(true, fmt.Sprintf("已清空全部任务（%d个）", len(deletedTasks)))
			fmt.Println(msg)
			os.Exit(0)
		}
		// -d N 删除指定任务
		id, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Println("参数错误: 任务ID必须为正整数或 all")
			fmt.Println("sct -d [TaskID] 或 sct -d all")
			os.Exit(0)
		}
		if id <= 0 {
			fmt.Println("参数错误: 任务ID必须为正整数")
			os.Exit(0)
		}
		success, msg, taskName := sctCrl.DeleteTask(id)
		if !success {
			fmt.Println(msg)
			os.Exit(0)
		}
		logger.Info(true, fmt.Sprintf("删除任务: [%d:%s]", id, taskName))
		fmt.Println("删除任务成功")
		os.Exit(0)
	}

	// 未匹配任何双参数命令
	fmt.Printf("参数错误: 未识别的命令 \"%s\"\n", arg)
	show_first_help()
	os.Exit(0)
}

func main() {
	args := os.Args[1:]
	logger := loghelp.NewLogger("scttool.log", 1)
	sctCrl := sctservice.NewSctControl()
	sctDm := sctservice.NewSctDeamon(logger, sctCrl)
	defer logger.Close()

	// 记录用户命令
	if len(args) > 0 {
		logger.Info(true, fmt.Sprintf("用户命令: scttool %s", strings.Join(args, " ")))
	}

	if len(args) == 0 {
		show_first_help()
		os.Exit(0)
	}

	if len(args) == 1 {
		one_args(sctDm, sctCrl, logger)
	}

	if len(args) >= 2 {
		two_args(sctDm, sctCrl, logger)
	}
}
