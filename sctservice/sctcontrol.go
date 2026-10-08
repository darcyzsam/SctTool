package sctservice

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/robfig/cron/v3"
)

// 预编译正则（避免每次调用重新编译）
var (
	tnRe    = regexp.MustCompile(`tn=\s*(\S+)`)
	tnValid = regexp.MustCompile(`^[\p{Han}a-zA-Z0-9_-]+$`)
	crRe    = regexp.MustCompile(`cr=\s*.+`)
	cmdRe   = regexp.MustCompile(`cmd=\s*.+`)
	tmRe    = regexp.MustCompile(`tm=\s*(\d+)`)
)

// Task 任务结构体
type Task struct {
	ID       int    `json:"id"`
	Taskname string `json:"taskname"`
	Cronstr  string `json:"cronstr"`
	Cmd      string `json:"cmd"`
	Timeout  int    `json:"timeout"` // 超时秒数，0=不限
}

// Config 配置文件结构
type Config struct {
	Tasks []Task `json:"tasks"`
}

// SctControl 核心业务类
type SctControl struct {
	cronParser cron.Parser // 成熟Cron解析器（全局复用）
}

// NewSctControl 初始化（注入Cron解析器）
func NewSctControl() *SctControl {
	// 使用【标准5位Crontab解析器】：分 时 日 月 周
	return &SctControl{
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
	}
}

// ------------------------------ 核心：Crontab 校验（行业标准库） ------------------------------
func (s *SctControl) ValidateCron(cronStr string) (bool, string) {
	// 空值校验
	cronStr = strings.TrimSpace(cronStr)
	if cronStr == "" {
		return false, "Crontab 表达式不能为空"
	}

	// 调用成熟库解析（自动校验所有语法、范围、格式）
	_, err := s.cronParser.Parse(cronStr)
	if err != nil {
		return false, fmt.Sprintf("Crontab 非法：%v", err)
	}

	return true, "校验通过"
}

// ------------------------------ 配置文件预检 ------------------------------

// CheckConfReadable 检查配置文件是否可读
// 仅检查不修改：文件不存在时返回 nil（允许启动，SaveConfig 会创建）
func (s *SctControl) CheckConfReadable() error {
	confPath := filepath.Join(".", "conf")
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		// 文件不存在，允许启动（-a 添加任务时会自动创建）
		return nil
	}
	// 文件存在，尝试读取
	if _, err := os.ReadFile(confPath); err != nil {
		return fmt.Errorf("配置文件读取失败: %v", err)
	}
	return nil
}

// ------------------------------ 配置文件加载 ------------------------------
func (s *SctControl) LoadConfig() (bool, string, Config, int) {
	confPath := filepath.Join(".", "conf")
	config := Config{}
	maxID := 0

	// 文件不存在
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		if writeErr := os.WriteFile(confPath, []byte{}, 0644); writeErr != nil {
			return false, fmt.Sprintf("配置文件不存在且创建失败：%v", writeErr), config, maxID
		}
		return false, "配置文件不存在，已创建空文件", config, maxID
	}

	// 读取文件
	content, err := os.ReadFile(confPath)
	if err != nil {
		return false, fmt.Sprintf("读取文件失败：%v", err), config, maxID
	}

	// Base64 解码
	decoded, err := base64.StdEncoding.DecodeString(string(content))
	if err != nil {
		return false, fmt.Sprintf("Base64解码失败：%v", err), config, maxID
	}

	// 空内容
	if len(decoded) == 0 {
		return false, "配置文件内容为空", config, maxID
	}

	// JSON 反序列化
	if err := json.Unmarshal(decoded, &config); err != nil {
		return false, fmt.Sprintf("JSON解析失败：%v", err), config, maxID
	}

	// 计算最大ID
	for _, task := range config.Tasks {
		if task.ID > maxID {
			maxID = task.ID
		}
	}

	return true, "", config, maxID
}

// ------------------------------ 保存（新增任务） ------------------------------
func (s *SctControl) SaveConfig(taskname, cronstr, cmd string, timeout int) (bool, string) {
	success, msg, config, maxID := s.LoadConfig()
	if !success && !strings.Contains(msg, "为空") && !strings.Contains(msg, "不存在") {
		return false, msg
	}

	// 校验任务数量上限
	if len(config.Tasks) >= 20 {
		return false, "任务数量已达上限（20个），无法继续添加"
	}

	// 校验任务名称是否重复
	for _, task := range config.Tasks {
		if task.Taskname == taskname {
			return false, fmt.Sprintf("任务名称 \"%s\" 已存在，请使用其他名称", taskname)
		}
	}

	// 新增任务
	newTask := Task{
		ID:       maxID + 1,
		Taskname: taskname,
		Cronstr:  cronstr,
		Cmd:      cmd,
		Timeout:  timeout,
	}
	config.Tasks = append(config.Tasks, newTask)

	// 序列化 + Base64
	jsonBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Sprintf("序列化失败：%v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(jsonBytes)

	// 写入文件
	if err := os.WriteFile(filepath.Join(".", "conf"), []byte(encoded), 0644); err != nil {
		return false, fmt.Sprintf("写入失败：%v", err)
	}

	return true, "新增任务成功"
}

// ------------------------------ 删除任务 ------------------------------
func (s *SctControl) DeleteTask(taskID int) (bool, string, string) {
	success, msg, config, _ := s.LoadConfig()
	if !success {
		return false, msg, ""
	}

	// 过滤掉要删除的ID
	newTasks := []Task{}
	found := false
	taskName := ""
	for _, task := range config.Tasks {
		if task.ID == taskID {
			found = true
			taskName = task.Taskname
			continue
		}
		newTasks = append(newTasks, task)
	}

	if !found {
		return false, fmt.Sprintf("任务ID %d 不存在", taskID), ""
	}

	config.Tasks = newTasks

	// 保存
	jsonBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Sprintf("序列化失败：%v", err), ""
	}
	encoded := base64.StdEncoding.EncodeToString(jsonBytes)
	if err := os.WriteFile(filepath.Join(".", "conf"), []byte(encoded), 0644); err != nil {
		return false, fmt.Sprintf("写入配置失败：%v", err), ""
	}

	return true, "删除成功", taskName
}

// ------------------------------ 删除全部任务 ------------------------------
func (s *SctControl) DeleteAllTasks() (bool, string, []Task) {
	success, msg, config, _ := s.LoadConfig()
	if !success {
		return false, msg, nil
	}

	if len(config.Tasks) == 0 {
		return false, "没有任务可删除", nil
	}

	// 记录被删除的任务
	deletedTasks := config.Tasks

	// 清空任务列表
	config.Tasks = []Task{}

	// 保存
	jsonBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Sprintf("序列化失败：%v", err), nil
	}
	encoded := base64.StdEncoding.EncodeToString(jsonBytes)
	if err := os.WriteFile(filepath.Join(".", "conf"), []byte(encoded), 0644); err != nil {
		return false, fmt.Sprintf("写入配置失败：%v", err), nil
	}

	return true, "已清空全部任务", deletedTasks
}

// ------------------------------ 参数解析（正则） ------------------------------
func (s *SctControl) ValidateParams(params string) (bool, string, string, string, string, int) {

	// ========== tn 解析：tn= 后直到空格，不许带空格 ==========
	tnMatch := tnRe.FindStringSubmatch(params)
	if tnMatch == nil {
		return false, "缺少 tn 参数", "", "", "", 0
	}
	tn := strings.TrimSpace(tnMatch[1])

	// 校验 tn：长度1~50，只允许字母/数字/下划线/中文/短横线
	if len(tn) == 0 {
		return false, "任务名称不能为空", "", "", "", 0
	}
	if len(tn) > 50 {
		return false, "任务名称过长（最多50个字符）", "", "", "", 0
	}
	if !tnValid.MatchString(tn) {
		return false, "任务名称只能包含中文、字母、数字、下划线和短横线", "", "", "", 0
	}

	// ========== cr 解析：不用前瞻，截取分割 ==========
	cr := ""
	crRaw := crRe.FindString(params)
	if crRaw != "" {
		// 去掉 cr= 前缀
		val := strings.TrimPrefix(crRaw, "cr=")
		// 先后按 tn=、cmd=、tm= 截断
		val = strings.Split(val, "tn=")[0]
		val = strings.Split(val, "cmd=")[0]
		val = strings.Split(val, "tm=")[0]
		cr = strings.TrimSpace(val)
	}
	if cr == "" {
		return false, "缺少 cr 参数", "", "", "", 0
	}

	// ========== cmd 解析：不用前瞻，截取分割 ==========
	cmd := ""
	cmdRaw := cmdRe.FindString(params)
	if cmdRaw != "" {
		val := strings.TrimPrefix(cmdRaw, "cmd=")
		val = strings.Split(val, "tn=")[0]
		val = strings.Split(val, "cr=")[0]
		val = strings.Split(val, "tm=")[0]
		cmd = strings.TrimSpace(val)
	}
	if cmd == "" {
		return false, "缺少 cmd 参数", "", "", "", 0
	}
	if len(cmd) > 500 {
		return false, "命令过长（最多500个字符）", "", "", "", 0
	}

	// 校验 Cron
	if ok, msg := s.ValidateCron(cr); !ok {
		return false, msg, "", "", "", 0
	}

	// ========== tm 解析（可选）：tm=秒数，默认0 ==========
	tm := 0
	tmMatch := tmRe.FindStringSubmatch(params)
	if tmMatch != nil {
		tmVal, err := strconv.Atoi(tmMatch[1])
		if err != nil || tmVal < 0 {
			return false, "tm 参数必须为非负整数", "", "", "", 0
		}
		tm = tmVal
	}

	return true, "参数校验通过", tn, cr, cmd, tm
}